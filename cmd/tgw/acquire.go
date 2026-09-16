package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tamnd/tolkiengateway-reader/internal/fetch"
)

const baseURL = "https://tolkiengateway.net"

const apiPath = "/w/api.php"

func runAcquire(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tgw acquire <ping|pages> [flags]")
	}
	subcommand := args[0]
	switch subcommand {
	case "ping":
		return runAcquirePing()
	case "pages":
		return runAcquirePages(args[1:])
	case "page":
		return runAcquirePage(args[1:])
	default:
		return fmt.Errorf("unknown acquire subcommand %q", subcommand)
	}
}

// rawPage is the revision-bearing input shared by acquire page and extract.
// Keeping this small JSON record on disk makes extraction and translation
// resumable without another network request.
type rawPage struct {
	Title     string `json:"title"`
	PageID    int    `json:"page_id"`
	Namespace int    `json:"namespace"`
	Revision  int    `json:"revision_id"`
	Wikitext  string `json:"wikitext"`
	SourceURL string `json:"source_url"`
	SHA256    string `json:"sha256"`
}

func runAcquirePage(args []string) error {
	fs := flag.NewFlagSet("acquire page", flag.ContinueOnError)
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	outRoot := fs.String("out", filepath.Join(corpusRoot(), "raw"), "directory for raw page records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return fmt.Errorf("usage: tgw acquire page [flags] <title>")
	}
	title := fs.Arg(0)
	ctx := context.Background()
	client, err := fetch.NewClient(ctx, strings.TrimRight(*base, "/"))
	if err != nil {
		return fmt.Errorf("warm a session: %w", err)
	}
	values := url.Values{}
	values.Set("action", "query")
	values.Set("prop", "revisions|info")
	values.Set("rvprop", "content|ids")
	values.Set("rvslots", "main")
	values.Set("inprop", "url")
	values.Set("titles", title)
	values.Set("format", "json")
	resp, err := client.Get(ctx, apiPath+"?"+values.Encode())
	if err != nil {
		return fmt.Errorf("fetch page %q: %w", title, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read page %q: %w", title, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if fetch.Challenged(resp) {
			return fmt.Errorf("page %q is still behind the Cloudflare challenge", title)
		}
		return fmt.Errorf("page %q returned HTTP %d: %s", title, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Query struct {
			Pages map[string]struct {
				Title     string `json:"title"`
				PageID    int    `json:"pageid"`
				Namespace int    `json:"ns"`
				FullURL   string `json:"fullurl"`
				Revisions []struct {
					Revision int `json:"revid"`
					Slots    struct {
						Main struct {
							Content string `json:"*"`
						} `json:"main"`
					} `json:"slots"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode page %q: %w", title, err)
	}
	var found rawPage
	for _, p := range result.Query.Pages {
		if p.PageID == 0 {
			return fmt.Errorf("page %q was not found", title)
		}
		if len(p.Revisions) == 0 {
			return fmt.Errorf("page %q has no readable revision", title)
		}
		found = rawPage{Title: p.Title, PageID: p.PageID, Namespace: p.Namespace,
			Revision: p.Revisions[0].Revision, Wikitext: p.Revisions[0].Slots.Main.Content,
			SourceURL: p.FullURL}
		break
	}
	if found.PageID == 0 {
		return fmt.Errorf("page %q was not found", title)
	}
	sum := sha256.Sum256([]byte(found.Wikitext))
	found.SHA256 = hex.EncodeToString(sum[:])
	name := slugTitle(found.Title) + ".json"
	if err := atomicWrite(filepath.Join(*outRoot, name), mustJSON(found)); err != nil {
		return err
	}
	fmt.Printf("acquired %s revision %d\n", found.Title, found.Revision)
	return nil
}

func mustJSON(value any) []byte {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func slugTitle(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "page"
	}
	return strings.Trim(b.String(), "_")
}

// page is the small, stable inventory record kept in the corpus repository.
// The raw API shape is deliberately not written to disk: this is the input
// contract for later extract and sync stages.
type page struct {
	Title     string
	PageID    int
	Namespace int
	Redirect  bool
}

type pageFetcher interface {
	Get(context.Context, string) (*http.Response, error)
}

// allPages walks MediaWiki's cursor-based allpages API. It never starts a
// second request until the previous one has completed; fetch.Client supplies
// the browser-earned session and the request spacing.
func allPages(ctx context.Context, client pageFetcher, limit int) ([]page, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var pages []page
	namespaces, err := pageNamespaces(ctx, client)
	if err != nil {
		return nil, err
	}
	for _, namespace := range namespaces {
		continueToken := ""
		for {
			values := url.Values{}
			values.Set("action", "query")
			values.Set("list", "allpages")
			values.Set("apnamespace", fmt.Sprint(namespace))
			values.Set("apfilterredir", "all")
			values.Set("aplimit", fmt.Sprint(limit))
			values.Set("format", "json")
			if continueToken != "" {
				values.Set("apcontinue", continueToken)
			}

			resp, err := client.Get(ctx, apiPath+"?"+values.Encode())
			if err != nil {
				return nil, fmt.Errorf("fetch page inventory: %w", err)
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read page inventory: %w", readErr)
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				if fetch.Challenged(resp) {
					return nil, fmt.Errorf("page inventory is still behind the Cloudflare challenge")
				}
				return nil, fmt.Errorf("page inventory returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}

			var result struct {
				Query struct {
					AllPages []struct {
						Title     string          `json:"title"`
						PageID    int             `json:"pageid"`
						Namespace int             `json:"ns"`
						Redirect  json.RawMessage `json:"redirect"`
					} `json:"allpages"`
				} `json:"query"`
				Continue struct {
					Next string `json:"apcontinue"`
				} `json:"continue"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return nil, fmt.Errorf("decode page inventory: %w", err)
			}
			for _, item := range result.Query.AllPages {
				pages = append(pages, page{
					Title: item.Title, PageID: item.PageID,
					Namespace: item.Namespace, Redirect: len(item.Redirect) > 0,
				})
			}
			if result.Continue.Next == "" {
				break
			}
			continueToken = result.Continue.Next
		}
	}

	sort.Slice(pages, func(i, j int) bool { return pages[i].Title < pages[j].Title })
	return pages, nil
}

func pageNamespaces(ctx context.Context, client pageFetcher) ([]int, error) {
	values := url.Values{}
	values.Set("action", "query")
	values.Set("meta", "siteinfo")
	values.Set("siprop", "namespaces")
	values.Set("format", "json")
	resp, err := client.Get(ctx, apiPath+"?"+values.Encode())
	if err != nil {
		return nil, fmt.Errorf("fetch namespaces: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read namespaces: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if fetch.Challenged(resp) {
			return nil, fmt.Errorf("namespace inventory is still behind the Cloudflare challenge")
		}
		return nil, fmt.Errorf("namespace inventory returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Query struct {
			Namespaces map[string]struct {
				ID int `json:"id"`
			} `json:"namespaces"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode namespaces: %w", err)
	}
	set := map[int]bool{}
	for _, namespace := range result.Query.Namespaces {
		if namespace.ID >= 0 {
			set[namespace.ID] = true
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("siteinfo returned no enumerable namespaces")
	}
	namespaces := make([]int, 0, len(set))
	for namespace := range set {
		namespaces = append(namespaces, namespace)
	}
	sort.Ints(namespaces)
	return namespaces, nil
}

func runAcquirePages(args []string) error {
	fs := flag.NewFlagSet("acquire pages", flag.ContinueOnError)
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	limit := fs.Int("limit", 500, "allpages batch size, from 1 to 500")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	client, err := fetch.NewClient(ctx, strings.TrimRight(*base, "/"))
	if err != nil {
		return fmt.Errorf("warm a session: %w", err)
	}
	pages, err := allPages(ctx, client, *limit)
	if err != nil {
		return err
	}
	root := corpusRoot()
	if err := writePageManifest(root, pages); err != nil {
		return err
	}
	if err := writePageReport(root, len(pages)); err != nil {
		return err
	}
	fmt.Printf("acquired %d pages\n", len(pages))
	return nil
}

func writePageManifest(root string, pages []page) error {
	var b strings.Builder
	b.WriteString("# The page inventory. Written by tgw acquire pages.\n")
	if len(pages) == 0 {
		b.WriteString("pages: []\n")
		return atomicWrite(filepath.Join(root, "manifests", "pages.yaml"), []byte(b.String()))
	}
	b.WriteString("pages:\n")
	for _, p := range pages {
		fmt.Fprintf(&b, "  - title: %s\n", yamlQuote(p.Title))
		fmt.Fprintf(&b, "    page_id: %d\n", p.PageID)
		fmt.Fprintf(&b, "    namespace: %d\n", p.Namespace)
		fmt.Fprintf(&b, "    redirect: %t\n", p.Redirect)
	}
	return atomicWrite(filepath.Join(root, "manifests", "pages.yaml"), []byte(b.String()))
}

func writePageReport(root string, count int) error {
	body := fmt.Sprintf("# Page inventory\n\nPages in the inventory: %d\n\nGenerated by `tgw acquire pages`.\n", count)
	return atomicWrite(filepath.Join(root, "reports", "pages.md"), []byte(body))
}

func yamlQuote(value string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\""), "\n", "\\n") + "\""
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tgw-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// runAcquirePing warms a browser session against the live wiki and asks the
// query API a small, harmless question, to prove the transport actually
// gets past the Cloudflare challenge instead of just theorizing about it.
func runAcquirePing() error {
	ctx := context.Background()

	client, err := fetch.NewClient(ctx, baseURL)
	if err != nil {
		return fmt.Errorf("warm a session: %w", err)
	}

	resp, err := client.Get(ctx, "/api.php?action=query&meta=siteinfo&format=json")
	if err != nil {
		return fmt.Errorf("call the query api: %w", err)
	}
	defer resp.Body.Close()

	if fetch.Challenged(resp) {
		return fmt.Errorf("the query api is still behind the Cloudflare challenge")
	}

	var siteInfo struct {
		Query struct {
			General struct {
				SiteName  string `json:"sitename"`
				Generator string `json:"generator"`
			} `json:"general"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&siteInfo); err != nil {
		return fmt.Errorf("decode the query api response: %w", err)
	}

	fmt.Printf("connected to %s (%s)\n", siteInfo.Query.General.SiteName, siteInfo.Query.General.Generator)
	return nil
}
