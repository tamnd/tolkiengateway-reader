package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// skinFS is the wiki's own stylesheet, fonts and icons, vendored by tgw skin.
//
//go:embed site/skin
var skinFS embed.FS

// siteFS holds the reader's own script and the few rules the wiki gets from
// modules this site does not load.
//
//go:embed site/tgw.js site/tgw.css
var siteFS embed.FS

// wikiOrigin is where everything the mirror does not carry still lives:
// talk pages, user pages, history, editing and the original files.
const wikiOrigin = "https://tolkiengateway.net"

// pageMeta is the front matter tgw convert writes.
type pageMeta struct {
	Title            string            `yaml:"title"`
	DisplayTitle     string            `yaml:"display_title"`
	PageID           int               `yaml:"page_id"`
	Namespace        int               `yaml:"namespace"`
	Revision         int               `yaml:"revision_id"`
	Timestamp        string            `yaml:"timestamp"`
	ShortDescription string            `yaml:"short_description"`
	Categories       []string          `yaml:"categories"`
	HiddenCategories []string          `yaml:"hidden_categories"`
	DefaultSort      string            `yaml:"defaultsort"`
	SortKeys         map[string]string `yaml:"sort_keys"`
	LangLinks        []struct {
		Lang  string `yaml:"lang" json:"l"`
		Name  string `yaml:"name" json:"n"`
		Title string `yaml:"title" json:"t"`
		URL   string `yaml:"url" json:"u"`
	} `yaml:"langlinks"`
	Images           []string  `yaml:"images"`
	File             *fileMeta `yaml:"file"`
	Redirect         bool      `yaml:"redirect"`
	RedirectTarget   string    `yaml:"redirect_target"`
	RedirectFragment string    `yaml:"redirect_fragment"`
	NoTOC            bool      `yaml:"notoc"`
	ForceTOC         bool      `yaml:"forcetoc"`
	NoTitle          bool      `yaml:"notitle"`
}

type fileMeta struct {
	URL       string `yaml:"url"`
	Mime      string `yaml:"mime"`
	Size      int64  `yaml:"size"`
	Width     int    `yaml:"width"`
	Height    int    `yaml:"height"`
	SHA1      string `yaml:"sha1"`
	Timestamp string `yaml:"timestamp"`
	User      string `yaml:"user"`
}

// sitePage is one corpus file and where it is published.
type sitePage struct {
	Meta   pageMeta
	Source string
	// Path is the output file below the site root, "wiki/Bilbo_Baggins.html".
	Path string
	// thumbs are the wiki image paths the page body refers to.
	thumbs []string
}

type categoryMember struct {
	page    *sitePage
	sortKey string
}

type site struct {
	base       string // URL path this language's pages are served under, without a trailing slash
	assets     string // URL path of the skin, images and search index all languages share
	out        string
	lang       string            // interface language
	langs      []siteLanguage    // every published language, the default first
	translated map[string]string // corpus page → its translation in lang, when there is one
	pages      []*sitePage
	byTitle    map[string]*sitePage
	categories map[string][]categoryMember // category name, spaces → members
	fileUsage  map[string][]*sitePage      // file name, underscores → pages showing it
	images     *imageBundle
	stats      siteStats
	built      time.Time
}

type siteStats struct {
	Articles, Files, Users, Edits int
}

// runPublish renders the corpus as a static copy of the wiki: the same
// Citizen skin, the same markup, local links between mirrored pages and the
// live wiki for everything else.
func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	check := fs.Bool("check", false, "check the corpus without writing a site")
	out := fs.String("out", "_site", "output directory for the built site")
	base := fs.String("base", "/tolkiengateway", "URL path the site is served under")
	lang := fs.String("lang", "en", "language to publish, when -langs is not given")
	langList := fs.String("langs", "", "comma-separated languages to publish; the first is served at -base, the others under -base/<code>")
	corpus := fs.String("corpus", corpusRoot(), "corpus repository")
	imagesDir := fs.String("images", "", "downloaded wiki images (the crawl's files directory); empty hotlinks every image")
	budget := fs.Int64("image-budget", 0, "megabytes of images to bundle, most used first; 0 bundles all")
	statsPath := fs.String("siteinfo", "", "siteinfo.json from tgw crawl inventory, for the drawer counters")
	if err := fs.Parse(args); err != nil {
		return err
	}
	codes := []string{*lang}
	if *langList != "" {
		codes = strings.Split(*langList, ",")
	}
	root := strings.TrimRight(*base, "/")
	var langs []siteLanguage
	for i, code := range codes {
		code = strings.TrimSpace(code)
		if languageNames[code] == "" {
			return fmt.Errorf("publish: unknown language %q", code)
		}
		l := siteLanguage{Code: code, Name: languageNames[code], Base: root}
		if i > 0 {
			l.Base = root + "/" + code
		}
		langs = append(langs, l)
	}

	// The English corpus is the site's structure: every language has the
	// same pages at the same paths, and shows a page's translation where
	// there is one.
	s := &site{base: root, assets: root, out: *out, langs: langs, built: time.Now().UTC()}
	if err := s.load(languageContentRoot(*corpus, "en")); err != nil {
		return err
	}
	if *check {
		fmt.Printf("publish check: %d pages, nothing broken\n", len(s.pages))
		return nil
	}
	s.loadStats(*statsPath)
	s.images = planImages(s, *imagesDir, *budget<<20)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := s.writeSharedAssets(); err != nil {
		return err
	}
	for i, l := range langs {
		ls := *s
		ls.lang, ls.base = l.Code, l.Base
		if i > 0 {
			ls.out = filepath.Join(*out, l.Code)
		}
		ls.translated = s.translations(*corpus, l.Code)
		if err := ls.writeFile("skin/chrome.js", []byte(ls.chromeScript())); err != nil {
			return err
		}
		if err := ls.writePages(); err != nil {
			return err
		}
		if err := ls.writeSpecialPages(i == 0); err != nil {
			return err
		}
		fmt.Printf("published %d %s pages to %s, %d translated\n", len(s.pages), l.Code, ls.out, len(ls.translated))
	}
	copied, err := s.images.copy(filepath.Join(*out, "images"))
	if err != nil {
		return err
	}
	fmt.Printf("%d images bundled (%.1f MB), the rest hotlinked\n", copied, float64(s.images.bytes)/1e6)
	return nil
}

// translations finds the pages translated into lang: content/<lang> holds
// them at the same paths as their English sources.
func (s *site) translations(corpus, lang string) map[string]string {
	out := map[string]string{}
	if lang == "en" {
		return out
	}
	enRoot := filepath.Join(corpus, "content", "en")
	langRoot := filepath.Join(corpus, "content", lang)
	for _, p := range s.pages {
		if p.Source == "" {
			continue
		}
		rel, err := filepath.Rel(enRoot, p.Source)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if target := filepath.Join(langRoot, rel); fileExists(target) {
			out[p.Source] = target
		}
	}
	return out
}

func languageContentRoot(root, lang string) string {
	preferred := filepath.Join(root, "content", lang)
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}
	return filepath.Join(root, "content", "en")
}

// load reads every page's front matter and indexes the corpus by title,
// category and file use. Bodies are read again when each page is written,
// so the whole corpus never sits in memory at once.
func (s *site) load(root string) error {
	var sources []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			sources = append(sources, p)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	sort.Strings(sources)
	s.pages = make([]*sitePage, len(sources))
	var failed atomic.Value
	parallelFor(len(sources), func(i int) {
		page, err := readSitePage(sources[i])
		if err != nil {
			failed.Store(err)
			return
		}
		s.pages[i] = page
	})
	if err, ok := failed.Load().(error); ok {
		return err
	}
	s.byTitle = make(map[string]*sitePage, len(s.pages))
	for _, p := range s.pages {
		if prev, ok := s.byTitle[p.Meta.Title]; ok {
			keep, drop := servedPage(prev, p)
			fmt.Fprintf(os.Stderr, "publish: %s and %s are both %q; keeping %s\n", prev.Source, p.Source, p.Meta.Title, keep.Source)
			drop.Path = ""
			p = keep
		}
		s.byTitle[p.Meta.Title] = p
		p.Path = "wiki/" + titleFilePath(p.Meta.Title) + ".html"
	}
	kept := s.pages[:0]
	for _, p := range s.pages {
		if p.Path != "" {
			kept = append(kept, p)
		}
	}
	s.pages = kept
	s.categories = map[string][]categoryMember{}
	s.fileUsage = map[string][]*sitePage{}
	for _, p := range s.pages {
		if p.Meta.Redirect {
			continue
		}
		for _, list := range [][]string{p.Meta.Categories, p.Meta.HiddenCategories} {
			for _, c := range list {
				key := p.Meta.SortKeys[c]
				if key == "" {
					key = p.Meta.DefaultSort
				}
				if key == "" {
					key = strings.TrimPrefix(p.Meta.Title, namespacePrefix(p.Meta.Namespace))
				}
				s.categories[c] = append(s.categories[c], categoryMember{page: p, sortKey: key})
			}
		}
		for _, name := range p.Meta.Images {
			s.fileUsage[name] = append(s.fileUsage[name], p)
		}
	}
	s.addGeneratedCategories()
	return nil
}

// readSitePage parses one corpus file's front matter and notes the images
// its body uses, for the image budget.
func readSitePage(source string) (*sitePage, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	front, body, err := splitFrontMatter(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	page := &sitePage{Source: source}
	if err := yaml.Unmarshal(front, &page.Meta); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if page.Meta.Title == "" {
		return nil, fmt.Errorf("%s: no title in front matter", source)
	}
	seen := map[string]bool{}
	for _, m := range thumbRE.FindAll(body, -1) {
		if rel, ok := imagePath("https://x" + strings.ReplaceAll(string(m), "&amp;", "&")); ok && !seen[rel] {
			seen[rel] = true
			page.thumbs = append(page.thumbs, rel)
		}
	}
	return page, nil
}

// leadImageRE matches a thumbnail at least 100 pixels wide, which leaves
// out icons and flags.
var leadImageRE = regexp.MustCompile(`^thumb/(.+)/[1-9][0-9]{2,}px-[^/]+$`)

// image is the original of the page's first sizeable thumbnail, below
// /w/images/, or "" when it has none.
func (p *sitePage) image() string {
	for _, rel := range p.thumbs {
		if m := leadImageRE.FindStringSubmatch(rel); m != nil {
			return m[1]
		}
	}
	return ""
}

func splitFrontMatter(data []byte) (front, body []byte, err error) {
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return nil, nil, fmt.Errorf("no front matter")
	}
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return nil, nil, fmt.Errorf("unterminated front matter")
	}
	return rest[:end+1], rest[end+5:], nil
}

func (s *site) loadStats(path string) {
	for _, p := range s.pages {
		if p.Meta.Redirect {
			continue
		}
		switch p.Meta.Namespace {
		case 0:
			s.stats.Articles++
		case 6:
			s.stats.Files++
		}
	}
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "publish: %v; counting the corpus instead\n", err)
		return
	}
	var info struct {
		Statistics struct {
			Articles int `json:"articles"`
			Images   int `json:"images"`
			Users    int `json:"users"`
			Edits    int `json:"edits"`
		} `json:"statistics"`
	}
	if json.Unmarshal(data, &info) == nil && info.Statistics.Articles > 0 {
		st := info.Statistics
		s.stats = siteStats{Articles: st.Articles, Files: st.Images, Users: st.Users, Edits: st.Edits}
	}
}

// namespaceNames are the canonical names of the namespaces the site links
// to, mirrored or not, so a title can be split into namespace and name.
var namespaceNames = map[int]string{
	-1: "Special", 1: "Talk", 2: "User", 3: "User talk", 4: "Tolkien Gateway", 5: "Tolkien Gateway talk",
	6: "File", 7: "File talk", 8: "MediaWiki", 9: "MediaWiki talk", 10: "Template", 11: "Template talk",
	12: "Help", 13: "Help talk", 14: "Category", 15: "Category talk", 102: "Index", 103: "Index talk",
	104: "Portal", 105: "Portal talk", 828: "Module", 829: "Module talk",
}

func namespacePrefix(ns int) string {
	if name, ok := namespaceNames[ns]; ok && ns != 0 {
		return name + ":"
	}
	return ""
}

// splitTitle returns a title's namespace id and the name without prefix.
func splitTitle(title string) (int, string) {
	if i := strings.Index(title, ":"); i > 0 {
		prefix := title[:i]
		for ns, name := range namespaceNames {
			if ns != 0 && strings.EqualFold(name, prefix) {
				return ns, title[i+1:]
			}
		}
	}
	return 0, title
}

// titleFilePath is a title as a path below wiki/: underscores for spaces,
// as in wiki URLs. Subpage slashes become directories, which is how the
// wiki's own URLs read too. Segments a file system cannot hold are encoded.
func titleFilePath(title string) string {
	parts := strings.Split(strings.ReplaceAll(title, " ", "_"), "/")
	for i, part := range parts {
		switch {
		case part == "":
			parts[i] = "%2F"
		case part == "." || part == "..":
			parts[i] = strings.ReplaceAll(part, ".", "%2E")
		case len(part) > 200:
			// Leave room for ".html" under the usual 255 byte limit.
			sum := sha1.Sum([]byte(part))
			cut := 180
			for cut > 0 && !utf8.RuneStart(part[cut]) {
				cut--
			}
			parts[i] = part[:cut] + "~" + hex.EncodeToString(sum[:4])
		}
	}
	return strings.Join(parts, "/")
}

// wikiEscape percent-encodes a path the way MediaWiki's wfUrlencode does,
// leaving the characters titles usually carry readable.
func wikiEscape(s string) string {
	const keep = "-_.~;@$!*(),/:"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pageURL is the site URL of a published page.
func (s *site) pageURL(p *sitePage) string {
	return s.base + "/" + wikiEscape(p.Path)
}

// remoteURL is the live wiki's URL for a title.
func remoteURL(title string) string {
	return wikiOrigin + "/wiki/" + wikiEscape(strings.ReplaceAll(title, " ", "_"))
}

// resolve follows redirects from a title to the page that is shown, with
// the fragment the last redirect points at.
func (s *site) resolve(title string) (*sitePage, string) {
	p := s.byTitle[title]
	fragment := ""
	for hops := 0; p != nil && p.Meta.Redirect && hops < 5; hops++ {
		if p.Meta.RedirectFragment != "" {
			fragment = p.Meta.RedirectFragment
		}
		p = s.byTitle[p.Meta.RedirectTarget]
	}
	if p != nil && p.Meta.Redirect {
		return nil, ""
	}
	return p, fragment
}

// titleURL links a title: to the mirrored page when there is one, and to
// the live wiki when there is not.
func (s *site) titleURL(title, fragment string) string {
	p, redirectFragment := s.resolve(title)
	if fragment == "" && redirectFragment != "" {
		fragment = anchorEncode(redirectFragment)
	}
	target := remoteURL(title)
	if p != nil {
		target = s.pageURL(p)
	}
	if fragment != "" {
		target += "#" + fragment
	}
	return target
}

// anchorEncode turns a heading or fragment into the id MediaWiki gives it.
func anchorEncode(fragment string) string {
	return strings.ReplaceAll(strings.TrimSpace(fragment), " ", "_")
}

// parallelFor runs fn for every index on all CPUs.
func parallelFor(n int, fn func(int)) {
	workers := max(1, min(16, n))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// writeFile writes one output file below the site root.
func (s *site) writeFile(rel string, data []byte) error {
	target := filepath.Join(s.out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

// writeSharedAssets copies the embedded skin and script and writes the
// search index, which every language shares.
func (s *site) writeSharedAssets() error {
	err := fs.WalkDir(skinFS, "site/skin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, "site/skin/samples/") {
			return err
		}
		data, err := skinFS.ReadFile(p)
		if err != nil {
			return err
		}
		return s.writeFile(path.Join("skin", strings.TrimPrefix(p, "site/skin/")), data)
	})
	if err != nil {
		return err
	}
	for _, name := range []string{"tgw.js", "tgw.css"} {
		data, err := siteFS.ReadFile("site/" + name)
		if err != nil {
			return err
		}
		if err := s.writeFile("skin/"+name, data); err != nil {
			return err
		}
	}
	return s.writeSearchIndex()
}

// writeSearchIndex lists every title for the search box and the random
// page: title, target path, short description, the target title when the
// entry is a redirect, and the page's lead image.
func (s *site) writeSearchIndex() error {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	w.WriteString("[")
	first := true
	for _, p := range s.pages {
		target, _ := s.resolve(p.Meta.Title)
		if target == nil {
			continue
		}
		entry := []any{p.Meta.Title, target.Path, target.Meta.ShortDescription, "", target.image()}
		if p.Meta.Redirect {
			entry[3] = target.Meta.Title
		}
		for len(entry) > 2 && entry[len(entry)-1] == "" {
			entry = entry[:len(entry)-1]
		}
		line, _ := json.Marshal(entry)
		if !first {
			w.WriteString(",\n")
		}
		first = false
		w.Write(line)
	}
	w.WriteString("]\n")
	w.Flush()
	return s.writeFile("search.json", b.Bytes())
}

// writePages renders every corpus page, in parallel.
func (s *site) writePages() error {
	var failed atomic.Value
	var done atomic.Int64
	start := time.Now()
	parallelFor(len(s.pages), func(i int) {
		p := s.pages[i]
		var doc string
		var err error
		if p.Meta.Redirect {
			doc = s.redirectDocument(p)
		} else {
			doc, err = s.renderPage(p)
		}
		if err == nil {
			err = s.writeFile(p.Path, []byte(doc))
		}
		if err != nil {
			failed.Store(fmt.Errorf("%s: %w", p.Source, err))
		}
		if d := done.Add(1); d%5000 == 0 {
			fmt.Printf("[%d/%d] %.0f pages/s\n", d, len(s.pages), float64(d)/time.Since(start).Seconds())
		}
	})
	if err, ok := failed.Load().(error); ok {
		return err
	}
	return nil
}

// renderPage turns one corpus page into a full HTML document.
func (s *site) renderPage(p *sitePage) (string, error) {
	if p.Source == "" {
		content, err := s.renderContent(p, nil)
		if err != nil {
			return "", err
		}
		return s.document(p, content), nil
	}
	source, untranslated := p.Source, false
	if s.lang != "en" {
		if t, ok := s.translated[p.Source]; ok {
			source = t
		} else {
			untranslated = true
		}
	}
	// An untranslated page whose English copy is also published borrows
	// that copy's body in the browser, so the site does not carry every
	// page twice.
	if untranslated {
		if en, ok := s.languageBase("en"); ok {
			return s.document(p, renderedPage{borrow: en + "/" + p.Path, untranslated: true}), nil
		}
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	_, body, err := splitFrontMatter(data)
	if err != nil {
		return "", err
	}
	content, err := s.renderContent(p, body)
	if err != nil {
		return "", err
	}
	content.untranslated = untranslated
	return s.document(p, content), nil
}

// redirectDocument sends the reader on to the page a redirect points at,
// keeping any fragment the reader asked for.
func (s *site) redirectDocument(p *sitePage) string {
	fragment := ""
	if p.Meta.RedirectFragment != "" {
		fragment = anchorEncode(p.Meta.RedirectFragment)
	}
	target := s.titleURL(p.Meta.RedirectTarget, fragment)
	plain := target
	if i := strings.Index(plain, "#"); i >= 0 {
		plain = plain[:i]
	}
	esc := htmlEscape(target)
	return `<!DOCTYPE html><html lang="` + s.lang + `"><head><meta charset="UTF-8"><title>` + htmlEscape(p.Meta.Title) + ` - Tolkien Gateway</title>` +
		`<meta name="robots" content="noindex"><link rel="canonical" href="` + htmlEscape(plain) + `">` +
		`<meta http-equiv="refresh" content="0; url=` + esc + `">` +
		`<script>location.replace(` + jsString(plain) + `+(location.hash||` + jsString(strings.TrimPrefix(target, plain)) + `))</script></head>` +
		`<body><p>` + s.uif("Redirect to %s.", `<a href="`+esc+`">`+htmlEscape(p.Meta.RedirectTarget)+`</a>`) + `</p></body></html>`
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// servedPage picks which of two pages with the same title the wiki serves.
// A main-namespace page created before its namespace existed, such as an
// old "Portal:Locations" article, is shadowed by the namespaced page, so
// the page whose namespace prefix matches its title wins. Otherwise the
// later revision does.
func servedPage(a, b *sitePage) (keep, drop *sitePage) {
	owns := func(p *sitePage) bool {
		return p.Meta.Namespace != 0 && strings.HasPrefix(p.Meta.Title, namespacePrefix(p.Meta.Namespace))
	}
	switch {
	case owns(a) && !owns(b):
		return a, b
	case owns(b) && !owns(a):
		return b, a
	case b.Meta.Revision > a.Meta.Revision:
		return b, a
	}
	return a, b
}

// languageBase returns the base URL a published language is served at.
func (s *site) languageBase(code string) (string, bool) {
	for _, l := range s.langs {
		if l.Code == code {
			return l.Base, true
		}
	}
	return "", false
}
