package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tamnd/tolkiengateway-reader/internal/fetch"
)

// defaultNamespaces are the reader-facing namespaces: articles, the project
// pages, file description pages, help, categories, indexes and portals.
// Talk, user, template and module pages are wiki plumbing, not content.
const defaultNamespaces = "0,4,6,12,14,102,104"

// The crawl is the full-fidelity counterpart of acquire. Instead of keeping
// only wikitext it keeps what MediaWiki itself renders, so templates,
// infoboxes and galleries come out exactly as the wiki shows them. Its output
// is a working directory, not the corpus: the rendered HTML and every image
// stay on the crawl machine, and only the converted Markdown is committed.
func runCrawl(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tgw crawl <inventory|pages|images|status> [flags]")
	}
	switch args[0] {
	case "inventory":
		return runCrawlInventory(args[1:])
	case "pages":
		return runCrawlPages(args[1:])
	case "images":
		return runCrawlImages(args[1:])
	case "status":
		return runCrawlStatus(args[1:])
	default:
		return fmt.Errorf("unknown crawl subcommand %q", args[0])
	}
}

// inventoryItem is one line of inventory.jsonl.
type inventoryItem struct {
	Title     string `json:"title"`
	PageID    int    `json:"page_id"`
	Namespace int    `json:"ns"`
	Revision  int    `json:"revision_id"`
	Timestamp string `json:"timestamp,omitempty"`
	Length    int    `json:"length,omitempty"`
	Redirect  bool   `json:"redirect,omitempty"`
	Target    string `json:"target,omitempty"`
	Fragment  string `json:"fragment,omitempty"`
	// Props are the page properties: notoc, notitle, defaultsort and the
	// like, which the parse output does not otherwise carry.
	Props map[string]string `json:"props,omitempty"`
}

// crawledPage is the on-disk record of one parsed page, gzipped JSON.
type crawledPage struct {
	Title        string          `json:"title"`
	DisplayTitle string          `json:"display_title"`
	PageID       int             `json:"page_id"`
	Namespace    int             `json:"ns"`
	Revision     int             `json:"revision_id"`
	Timestamp    string          `json:"timestamp"`
	ShortDesc    string          `json:"short_description,omitempty"`
	HTML         string          `json:"html"`
	Wikitext     string          `json:"wikitext"`
	Categories   []crawlCategory `json:"categories"`
	Sections     []crawlSection  `json:"sections"`
	Images       []string        `json:"images"`
	LangLinks    []crawlLangLink `json:"langlinks,omitempty"`
	FetchedAt    time.Time       `json:"fetched_at"`
}

type crawlCategory struct {
	Name    string `json:"name"`
	SortKey string `json:"sortkey,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
}

type crawlSection struct {
	Level  int    `json:"level"`
	Line   string `json:"line"`
	Number string `json:"number"`
	Anchor string `json:"anchor"`
}

type crawlLangLink struct {
	Lang     string `json:"lang"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	LangName string `json:"langname"`
	Autonym  string `json:"autonym"`
}

func newCrawlClient(ctx context.Context, base string) (*fetch.Client, error) {
	client, err := fetch.NewClient(ctx, strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("open a session: %w", err)
	}
	return client, nil
}

// apiJSON performs one API GET and decodes the JSON body into out.
func apiJSON(ctx context.Context, client pageFetcher, values url.Values, out any) error {
	values.Set("format", "json")
	values.Set("formatversion", "2")
	values.Set("maxlag", "5")
	for attempt := 0; ; attempt++ {
		resp, err := client.Get(ctx, apiPath+"?"+values.Encode())
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != http.StatusOK {
			if fetch.Challenged(resp) {
				return fmt.Errorf("the API is behind the Cloudflare challenge on this network")
			}
			return fmt.Errorf("API returned HTTP %d: %.200s", resp.StatusCode, body)
		}
		var envelope struct {
			Error *struct {
				Code string `json:"code"`
				Info string `json:"info"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return fmt.Errorf("decode API response: %w", err)
		}
		if envelope.Error != nil {
			if envelope.Error.Code == "maxlag" && attempt < 10 {
				time.Sleep(5 * time.Second)
				continue
			}
			return &apiError{Code: envelope.Error.Code, Info: envelope.Error.Info}
		}
		return json.Unmarshal(body, out)
	}
}

type apiError struct{ Code, Info string }

func (e *apiError) Error() string { return "API error " + e.Code + ": " + e.Info }

func parseNamespaces(list string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("bad namespace %q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func runCrawlInventory(args []string) error {
	fs := flag.NewFlagSet("crawl inventory", flag.ContinueOnError)
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	out := fs.String("out", "crawl", "crawl working directory")
	nsList := fs.String("ns", defaultNamespaces, "comma separated namespace ids")
	if err := fs.Parse(args); err != nil {
		return err
	}
	namespaces, err := parseNamespaces(*nsList)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := newCrawlClient(ctx, *base)
	if err != nil {
		return err
	}
	items, err := crawlInventory(ctx, client, namespaces)
	if err != nil {
		return err
	}
	var b strings.Builder
	redirects := 0
	for _, item := range items {
		line, _ := json.Marshal(item)
		b.Write(line)
		b.WriteByte('\n')
		if item.Redirect {
			redirects++
		}
	}
	if err := atomicWrite(filepath.Join(*out, "inventory.jsonl"), []byte(b.String())); err != nil {
		return err
	}
	fmt.Printf("inventory: %d pages, %d of them redirects\n", len(items), redirects)
	return nil
}

// crawlInventory lists every page in the namespaces with its latest revision,
// then resolves the redirect targets in a second pass.
func crawlInventory(ctx context.Context, client pageFetcher, namespaces []int) ([]inventoryItem, error) {
	var items []inventoryItem
	for _, ns := range namespaces {
		cont := url.Values{}
		for {
			values := url.Values{}
			values.Set("action", "query")
			values.Set("generator", "allpages")
			values.Set("gapnamespace", strconv.Itoa(ns))
			values.Set("gaplimit", "500")
			values.Set("prop", "info|revisions|pageprops")
			values.Set("rvprop", "ids|timestamp")
			for k, v := range cont {
				values[k] = v
			}
			var result struct {
				Continue map[string]any `json:"continue"`
				Query    struct {
					Pages []struct {
						PageID    int            `json:"pageid"`
						Namespace int            `json:"ns"`
						Title     string         `json:"title"`
						Redirect  bool           `json:"redirect"`
						Length    int            `json:"length"`
						LastRevID int            `json:"lastrevid"`
						PageProps map[string]any `json:"pageprops"`
						Revisions []struct {
							RevID     int    `json:"revid"`
							Timestamp string `json:"timestamp"`
						} `json:"revisions"`
					} `json:"pages"`
				} `json:"query"`
			}
			if err := apiJSON(ctx, client, values, &result); err != nil {
				return nil, fmt.Errorf("inventory of namespace %d: %w", ns, err)
			}
			for _, p := range result.Query.Pages {
				item := inventoryItem{Title: p.Title, PageID: p.PageID, Namespace: p.Namespace,
					Revision: p.LastRevID, Length: p.Length, Redirect: p.Redirect}
				if len(p.Revisions) > 0 {
					item.Timestamp = p.Revisions[0].Timestamp
				}
				for k, v := range p.PageProps {
					if item.Props == nil {
						item.Props = map[string]string{}
					}
					if str, ok := v.(string); ok {
						item.Props[k] = str
					} else {
						item.Props[k] = fmt.Sprint(v)
					}
				}
				items = append(items, item)
			}
			if len(result.Continue) == 0 {
				break
			}
			cont = url.Values{}
			for k, v := range result.Continue {
				cont.Set(k, fmt.Sprint(v))
			}
		}
		fmt.Printf("namespace %d: %d pages so far\n", ns, len(items))
	}

	// Resolve redirects fifty at a time.
	byTitle := map[string]*inventoryItem{}
	var redirects []string
	for i := range items {
		if items[i].Redirect {
			byTitle[items[i].Title] = &items[i]
			redirects = append(redirects, items[i].Title)
		}
	}
	for start := 0; start < len(redirects); start += 50 {
		end := min(start+50, len(redirects))
		values := url.Values{}
		values.Set("action", "query")
		values.Set("redirects", "1")
		values.Set("titles", strings.Join(redirects[start:end], "|"))
		var result struct {
			Query struct {
				Redirects []struct {
					From     string `json:"from"`
					To       string `json:"to"`
					Fragment string `json:"tofragment"`
				} `json:"redirects"`
			} `json:"query"`
		}
		if err := apiJSON(ctx, client, values, &result); err != nil {
			return nil, fmt.Errorf("resolve redirects: %w", err)
		}
		for _, r := range result.Query.Redirects {
			if item := byTitle[r.From]; item != nil {
				item.Target, item.Fragment = r.To, r.Fragment
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PageID < items[j].PageID })
	return items, nil
}

func readInventory(path string) ([]inventoryItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []inventoryItem
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var item inventoryItem
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		items = append(items, item)
	}
	return items, scanner.Err()
}

// crawledPath is where one revision of one page is stored. The revision is
// in the file name, so "already have it" is a single stat.
func crawledPath(dir string, pageID, revision int) string {
	return filepath.Join(dir, "pages", strconv.Itoa(pageID/1000), fmt.Sprintf("%d-%d.json.gz", pageID, revision))
}

func parseShard(spec string) (int, int, error) {
	if spec == "" {
		return 0, 1, nil
	}
	a, b, ok := strings.Cut(spec, "/")
	i, err1 := strconv.Atoi(a)
	n, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || n < 1 || i < 0 || i >= n {
		return 0, 0, fmt.Errorf("shard must look like 0/3, got %q", spec)
	}
	return i, n, nil
}

func runCrawlPages(args []string) error {
	fs := flag.NewFlagSet("crawl pages", flag.ContinueOnError)
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	out := fs.String("out", "crawl", "crawl working directory")
	inventory := fs.String("inventory", "", "inventory.jsonl (default <out>/inventory.jsonl)")
	shard := fs.String("shard", "", "take only shard i of n, as i/n, split by page id")
	have := fs.String("have", "", "extra directory to treat as already crawled (for drained shards)")
	maxPages := fs.Int("max-pages", 0, "stop after this many fetches; zero means all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	shardIndex, shardCount, err := parseShard(*shard)
	if err != nil {
		return err
	}
	if *inventory == "" {
		*inventory = filepath.Join(*out, "inventory.jsonl")
	}
	items, err := readInventory(*inventory)
	if err != nil {
		return err
	}
	var todo []inventoryItem
	for _, item := range items {
		if item.Redirect || item.PageID%shardCount != shardIndex {
			continue
		}
		if fileExists(crawledPath(*out, item.PageID, item.Revision)) ||
			(*have != "" && fileExists(crawledPath(*have, item.PageID, item.Revision))) {
			continue
		}
		todo = append(todo, item)
	}
	if *maxPages > 0 && *maxPages < len(todo) {
		todo = todo[:*maxPages]
	}
	fmt.Printf("crawl pages: %d to fetch in shard %d/%d\n", len(todo), shardIndex, shardCount)
	ctx := context.Background()
	client, err := newCrawlClient(ctx, *base)
	if err != nil {
		return err
	}
	failures := 0
	start := time.Now()
	for i, item := range todo {
		page, err := parsePage(ctx, client, item)
		if err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v\n", i+1, len(todo), item.Title, err)
			if failures > 200 && failures > i/10 {
				return fmt.Errorf("too many failures (%d), stopping", failures)
			}
			continue
		}
		if err := writeCrawledPage(*out, page); err != nil {
			return err
		}
		if (i+1)%100 == 0 || i+1 == len(todo) {
			rate := float64(i+1) / time.Since(start).Seconds()
			fmt.Printf("[%d/%d] %s (%.2f pages/s, %d failures)\n", i+1, len(todo), item.Title, rate, failures)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d pages failed; rerun to retry them", failures)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// parsePage asks MediaWiki to render one revision and keeps everything the
// converter and the site need: the HTML, the wikitext, the categories with
// their hidden flag, the table of contents and the interlanguage links.
func parsePage(ctx context.Context, client pageFetcher, item inventoryItem) (crawledPage, error) {
	values := url.Values{}
	values.Set("action", "parse")
	// Tolkien Gateway's Cloudflare rules challenge any request carrying
	// oldid, so the page is asked for by id and the revision it returns is
	// the one recorded.
	values.Set("pageid", strconv.Itoa(item.PageID))
	values.Set("prop", "text|wikitext|categories|displaytitle|sections|images|langlinks|properties|revid")
	values.Set("disableeditsection", "1")
	values.Set("disablelimitreport", "1")
	var result struct {
		Parse struct {
			Title        string `json:"title"`
			PageID       int    `json:"pageid"`
			RevID        int    `json:"revid"`
			DisplayTitle string `json:"displaytitle"`
			Text         string `json:"text"`
			Wikitext     string `json:"wikitext"`
			Categories   []struct {
				SortKey  string `json:"sortkey"`
				Category string `json:"category"`
				Hidden   bool   `json:"hidden"`
			} `json:"categories"`
			Sections []struct {
				Level  string `json:"level"`
				Line   string `json:"line"`
				Number string `json:"number"`
				Anchor string `json:"anchor"`
			} `json:"sections"`
			Images     []string        `json:"images"`
			LangLinks  []crawlLangLink `json:"langlinks"`
			Properties map[string]any  `json:"properties"`
		} `json:"parse"`
	}
	if err := apiJSON(ctx, client, values, &result); err != nil {
		return crawledPage{}, err
	}
	p := result.Parse
	page := crawledPage{
		Title: item.Title, DisplayTitle: p.DisplayTitle, PageID: item.PageID,
		Namespace: item.Namespace, Revision: p.RevID, Timestamp: item.Timestamp,
		ShortDesc: propertyString(p.Properties["shortdesc"]), HTML: p.Text, Wikitext: p.Wikitext,
		Images: p.Images, LangLinks: p.LangLinks, FetchedAt: time.Now().UTC(),
	}
	if page.Revision == 0 {
		page.Revision = item.Revision
	}
	for _, c := range p.Categories {
		page.Categories = append(page.Categories, crawlCategory{Name: c.Category, SortKey: c.SortKey, Hidden: c.Hidden})
	}
	for _, s := range p.Sections {
		level, _ := strconv.Atoi(s.Level)
		page.Sections = append(page.Sections, crawlSection{Level: level, Line: s.Line, Number: s.Number, Anchor: s.Anchor})
	}
	return page, nil
}

func propertyString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func writeCrawledPage(dir string, page crawledPage) error {
	path := crawledPath(dir, page.PageID, page.Revision)
	var buf strings.Builder
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(page); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := atomicWrite(path, []byte(buf.String())); err != nil {
		return err
	}
	// Drop older revisions of the same page.
	old, _ := filepath.Glob(filepath.Join(filepath.Dir(path), strconv.Itoa(page.PageID)+"-*.json.gz"))
	for _, o := range old {
		if o != path {
			os.Remove(o)
		}
	}
	return nil
}

func readCrawledPage(path string) (crawledPage, error) {
	var page crawledPage
	f, err := os.Open(path)
	if err != nil {
		return page, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return page, fmt.Errorf("%s: %w", path, err)
	}
	err = json.NewDecoder(zr).Decode(&page)
	if err != nil {
		err = fmt.Errorf("%s: %w", path, err)
	}
	return page, err
}

// crawledFiles lists the newest stored revision of each crawled page.
func crawledFiles(dir string) (map[int]string, error) {
	files := map[int]string{}
	revs := map[int]int{}
	err := filepath.WalkDir(filepath.Join(dir, "pages"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".json.gz") {
			return nil
		}
		idPart, revPart, ok := strings.Cut(strings.TrimSuffix(name, ".json.gz"), "-")
		id, err1 := strconv.Atoi(idPart)
		rev, err2 := strconv.Atoi(revPart)
		if !ok || err1 != nil || err2 != nil {
			return nil
		}
		if rev > revs[id] {
			revs[id], files[id] = rev, path
		}
		return nil
	})
	return files, err
}

func runCrawlStatus(args []string) error {
	fs := flag.NewFlagSet("crawl status", flag.ContinueOnError)
	out := fs.String("out", "crawl", "crawl working directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := readInventory(filepath.Join(*out, "inventory.jsonl"))
	if err != nil {
		return err
	}
	files, err := crawledFiles(*out)
	if err != nil {
		return err
	}
	want, have, stale := 0, 0, 0
	for _, item := range items {
		if item.Redirect {
			continue
		}
		want++
		if path, ok := files[item.PageID]; ok {
			if strings.HasSuffix(path, fmt.Sprintf("-%d.json.gz", item.Revision)) {
				have++
			} else {
				stale++
			}
		}
	}
	fmt.Printf("pages: %d of %d crawled, %d stale\n", have, want, stale)
	return nil
}

// imageRecord is one line of images.jsonl, straight from list=allimages.
type imageRecord struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	DescURL     string `json:"descriptionurl"`
	Size        int64  `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Mime        string `json:"mime"`
	SHA1        string `json:"sha1"`
	Timestamp   string `json:"timestamp"`
	Description string `json:"description,omitempty"`
	User        string `json:"user,omitempty"`
}

var thumbRE = regexp.MustCompile(`/w/images/(?:thumb/|archive/)?[0-9a-f]/[0-9a-f]{2}/[^"'\s,)]+`)

func runCrawlImages(args []string) error {
	fs := flag.NewFlagSet("crawl images", flag.ContinueOnError)
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	out := fs.String("out", "crawl", "crawl working directory")
	list := fs.Bool("list", true, "refresh images.jsonl from the API first")
	originals := fs.Bool("originals", true, "download the original files")
	thumbs := fs.Bool("thumbs", true, "download the thumbnails the crawled pages use")
	workers := fs.Int("workers", 4, "parallel downloads, sharing one rate limit")
	shard := fs.String("shard", "", "take only shard i of n, as i/n, of the file page thumbnails; the crawled pages' own thumbnails are always taken")
	if err := fs.Parse(args); err != nil {
		return err
	}
	shardIndex, shardCount, err := parseShard(*shard)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := newCrawlClient(ctx, *base)
	if err != nil {
		return err
	}
	listPath := filepath.Join(*out, "images.jsonl")
	if *list || !fileExists(listPath) {
		if err := listImages(ctx, client, listPath); err != nil {
			return err
		}
	}
	if err := writeSiteInfo(ctx, client, filepath.Join(*out, "siteinfo.json")); err != nil {
		return err
	}
	records, err := readImageRecords(listPath)
	if err != nil {
		return err
	}
	// Thumbnails come first: they are what the site shows, and they are
	// small next to the originals.
	var jobs []imageJob
	seen := map[string]bool{}
	add := func(job imageJob) {
		if !seen[job.rel] {
			seen[job.rel] = true
			jobs = append(jobs, job)
		}
	}
	if *thumbs {
		used, err := usedImagePaths(*out)
		if err != nil {
			return err
		}
		for _, rel := range used {
			if strings.HasPrefix(rel, "thumb/") {
				add(imageJob{rel: rel})
			}
		}
		for i, r := range records {
			if i%shardCount != shardIndex {
				continue
			}
			for _, t := range fileDisplayThumbs(r.URL, r.Mime, r.Width, r.Height) {
				if strings.HasPrefix(t.rel, "thumb/") {
					add(imageJob{rel: t.rel})
				}
			}
		}
	}
	if *originals {
		for _, r := range records {
			if rel, ok := imagePath(r.URL); ok {
				add(imageJob{rel: rel, size: r.Size, sha1: r.SHA1})
			}
		}
	}
	return downloadImages(ctx, client, *base, filepath.Join(*out, "files"), jobs, *workers)
}

func listImages(ctx context.Context, client pageFetcher, path string) error {
	var b strings.Builder
	cont := url.Values{}
	count := 0
	for {
		values := url.Values{}
		values.Set("action", "query")
		values.Set("list", "allimages")
		values.Set("ailimit", "500")
		values.Set("aiprop", "url|size|dimensions|mime|sha1|timestamp|user")
		for k, v := range cont {
			values[k] = v
		}
		var result struct {
			Continue map[string]any `json:"continue"`
			Query    struct {
				AllImages []imageRecord `json:"allimages"`
			} `json:"query"`
		}
		if err := apiJSON(ctx, client, values, &result); err != nil {
			return fmt.Errorf("list images: %w", err)
		}
		for _, r := range result.Query.AllImages {
			line, _ := json.Marshal(r)
			b.Write(line)
			b.WriteByte('\n')
			count++
		}
		if len(result.Continue) == 0 {
			break
		}
		cont = url.Values{}
		for k, v := range result.Continue {
			cont.Set(k, fmt.Sprint(v))
		}
	}
	fmt.Printf("images: %d files listed\n", count)
	return atomicWrite(path, []byte(b.String()))
}

// writeSiteInfo saves the wiki's statistics, which the site's drawer shows.
func writeSiteInfo(ctx context.Context, client pageFetcher, path string) error {
	values := url.Values{}
	values.Set("action", "query")
	values.Set("meta", "siteinfo")
	values.Set("siprop", "statistics")
	var result struct {
		Query json.RawMessage `json:"query"`
	}
	if err := apiJSON(ctx, client, values, &result); err != nil {
		return fmt.Errorf("siteinfo: %w", err)
	}
	return atomicWrite(path, append(result.Query, '\n'))
}

func readImageRecords(path string) ([]imageRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var records []imageRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var r imageRecord
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, scanner.Err()
}

// imagePath turns an image URL into its path below /w/images/, decoded, so
// the stored tree mirrors the wiki's own layout.
func imagePath(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	rel, ok := strings.CutPrefix(u.Path, "/w/images/")
	if !ok || rel == "" || strings.Contains(rel, "..") {
		return "", false
	}
	return rel, true
}

// usedImagePaths scans every crawled page for image URLs, src and srcset.
func usedImagePaths(dir string) ([]string, error) {
	files, err := crawledFiles(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, path := range files {
		page, err := readCrawledPage(path)
		if err != nil {
			return nil, err
		}
		for _, match := range thumbRE.FindAllString(page.HTML, -1) {
			if rel, ok := imagePath("https://x" + strings.ReplaceAll(match, "&amp;", "&")); ok {
				seen[rel] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

type imageJob struct {
	rel  string
	size int64
	sha1 string
}

// downloadImages fetches every job that is not already on disk with the
// right size. Originals are also checked against the SHA-1 the API reports.
func downloadImages(ctx context.Context, client *fetch.Client, base, root string, jobs []imageJob, workers int) error {
	var todo []imageJob
	for _, job := range jobs {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(job.rel)))
		if err == nil && (job.size == 0 || info.Size() == job.size) {
			continue
		}
		todo = append(todo, job)
	}
	fmt.Printf("images: %d of %d files to download\n", len(todo), len(jobs))
	var done, failed atomic.Int64
	var bytes atomic.Int64
	start := time.Now()
	queue := make(chan imageJob)
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				n, err := downloadImage(ctx, client, base, root, job)
				if err != nil {
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "image %s: %v\n", job.rel, err)
				}
				bytes.Add(n)
				if d := done.Add(1); d%200 == 0 || int(d) == len(todo) {
					fmt.Printf("[%d/%d] %.1f MB, %.1f files/s, %d failed\n", d, len(todo),
						float64(bytes.Load())/1e6, float64(d)/time.Since(start).Seconds(), failed.Load())
				}
			}
		}()
	}
	for _, job := range todo {
		queue <- job
	}
	close(queue)
	wg.Wait()
	if failed.Load() > 0 {
		return fmt.Errorf("%d images failed; rerun to retry them", failed.Load())
	}
	return nil
}

func downloadImage(ctx context.Context, client *fetch.Client, base, root string, job imageJob) (int64, error) {
	escaped := (&url.URL{Path: "/w/images/" + job.rel}).EscapedPath()
	resp, err := client.Get(ctx, strings.TrimRight(base, "/")+escaped)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if job.size > 0 && int64(len(data)) != job.size {
		return 0, fmt.Errorf("size %d, want %d", len(data), job.size)
	}
	if job.sha1 != "" {
		sum := sha1.Sum(data)
		if hex.EncodeToString(sum[:]) != job.sha1 {
			return 0, fmt.Errorf("sha1 mismatch")
		}
	}
	return int64(len(data)), atomicWrite(filepath.Join(root, filepath.FromSlash(job.rel)), data)
}
