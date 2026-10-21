package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// namespaceDirs maps the crawled namespaces to their corpus directories and
// title prefixes.
var namespaceDirs = map[int]struct{ Dir, Prefix string }{
	0:   {"main", ""},
	4:   {"project", "Tolkien Gateway:"},
	6:   {"file", "File:"},
	12:  {"help", "Help:"},
	14:  {"category", "Category:"},
	102: {"index", "Index:"},
	104: {"portal", "Portal:"},
}

// runConvert turns every crawled page into one Markdown file in the corpus.
// Prose becomes plain Markdown; anything MediaWiki styled (infoboxes,
// navboxes, galleries, reference lists) is kept as an HTML block so the site
// can show it exactly as the wiki does.
func runConvert(args []string) error {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	crawlDir := fs.String("crawl", "crawl", "crawl working directory")
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	lang := fs.String("lang", "en", "output language directory")
	prune := fs.Bool("prune", true, "remove Markdown files for pages that no longer exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := readInventory(filepath.Join(*crawlDir, "inventory.jsonl"))
	if err != nil {
		return err
	}
	files, err := crawledFiles(*crawlDir)
	if err != nil {
		return err
	}
	images := map[string]imageRecord{}
	if records, err := readImageRecords(filepath.Join(*crawlDir, "images.jsonl")); err == nil {
		for _, r := range records {
			images[r.Name] = r
		}
	}
	paths := corpusPaths(items)
	links := newCorpusLinks(items, paths)
	outRoot := filepath.Join(*root, "content", *lang)
	written := map[string]bool{}
	missing, converted := 0, 0
	for _, item := range items {
		rel, ok := paths[item.PageID]
		if !ok {
			continue
		}
		var doc string
		if item.Redirect {
			doc = renderRedirect(item, links, rel)
		} else {
			path, ok := files[item.PageID]
			if !ok {
				missing++
				continue
			}
			page, err := readCrawledPage(path)
			if err != nil {
				return err
			}
			var file *imageRecord
			if item.Namespace == 6 {
				if r, ok := images[strings.ReplaceAll(strings.TrimPrefix(item.Title, "File:"), " ", "_")]; ok {
					file = &r
				}
			}
			doc = renderPageMarkdown(page, item.Props, file, links, rel)
		}
		target := filepath.Join(outRoot, filepath.FromSlash(rel))
		written[target] = true
		if old, err := os.ReadFile(target); err == nil && string(old) == doc {
			converted++
			continue
		}
		if err := atomicWrite(target, []byte(doc)); err != nil {
			return err
		}
		converted++
	}
	if err := links.writeTemplateStyles(*root); err != nil {
		return err
	}
	pruned := 0
	if *prune {
		filepath.WalkDir(outRoot, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") && !written[path] {
				if os.Remove(path) == nil {
					pruned++
				}
			}
			return nil
		})
	}
	fmt.Printf("convert: %d pages written, %d not crawled yet, %d pruned\n", converted, missing, pruned)
	return nil
}

// corpusPaths assigns each page its file path below content/<lang>/. Paths
// are compared case-insensitively, because the corpus is checked out on
// macOS and Windows too, and a page whose path would collide with an earlier
// one gets its page id appended.
func corpusPaths(items []inventoryItem) map[int]string {
	sorted := append([]inventoryItem(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PageID < sorted[j].PageID })
	taken := map[string]bool{}
	out := map[int]string{}
	for _, item := range sorted {
		ns, ok := namespaceDirs[item.Namespace]
		if !ok {
			continue
		}
		name := strings.TrimPrefix(item.Title, ns.Prefix)
		base := fileSafeTitle(name)
		rel := ns.Dir + "/" + firstLetter(name) + "/" + base + ".md"
		if taken[strings.ToLower(rel)] {
			rel = ns.Dir + "/" + firstLetter(name) + "/" + base + fmt.Sprintf("~%d.md", item.PageID)
		}
		taken[strings.ToLower(rel)] = true
		out[item.PageID] = rel
	}
	return out
}

// fileSafeTitle keeps a title readable as a file name: spaces become
// underscores, as in wiki URLs, and only characters that some file system
// rejects are percent-encoded, so the mapping can be reversed.
func fileSafeTitle(title string) string {
	var b strings.Builder
	for i, r := range strings.ReplaceAll(title, " ", "_") {
		switch {
		case r < 0x20, strings.ContainsRune(`/\:*?"<>|%`, r), i == 0 && r == '.':
			fmt.Fprintf(&b, "%%%02X", r)
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func wikiURL(title string) string {
	return "/wiki/" + strings.ReplaceAll(title, " ", "_")
}

func renderRedirect(item inventoryItem, links *corpusLinks, rel string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlQuote(item.Title))
	fmt.Fprintf(&b, "page_id: %d\nnamespace: %d\nrevision_id: %d\n", item.PageID, item.Namespace, item.Revision)
	fmt.Fprintf(&b, "source_url: %s\n", yamlQuote(baseURL+wikiURL(item.Title)))
	b.WriteString("license: CC BY-SA 4.0\nsource: Tolkien Gateway\nredirect: true\n")
	fmt.Fprintf(&b, "redirect_target: %s\n", yamlQuote(item.Target))
	if item.Fragment != "" {
		fmt.Fprintf(&b, "redirect_fragment: %s\n", yamlQuote(item.Fragment))
	}
	b.WriteString("---\n\n")
	target := wikiURL(item.Target)
	if item.Fragment != "" {
		target += "#" + strings.ReplaceAll(item.Fragment, " ", "_")
	}
	if links != nil {
		target = links.href(rel, target)
	}
	if strings.ContainsAny(target, "() ") {
		target = "<" + target + ">"
	}
	fmt.Fprintf(&b, "Redirect to [%s](%s).\n", escapeMarkdown(item.Target), target)
	return b.String()
}

// plainDisplayTitle reports whether a display title is only what the site
// shows for the title anyway, MediaWiki's default title spans.
func plainDisplayTitle(display, title string, ns int) bool {
	plain := html.UnescapeString(display)
	return plain == title || plain == html.UnescapeString(pageHeading(pageMeta{Title: title, Namespace: ns}))
}

// pageFlags are the behaviour switches carried over from the page
// properties, written as booleans named after their magic words.
var pageFlags = []string{"notoc", "forcetoc", "notitle", "nogallery"}

func renderPageMarkdown(page crawledPage, props map[string]string, file *imageRecord, links *corpusLinks, rel string) string {
	hash := sha256.Sum256([]byte(page.Wikitext))
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlQuote(page.Title))
	if display := page.DisplayTitle; display != "" && !plainDisplayTitle(display, page.Title, page.Namespace) {
		fmt.Fprintf(&b, "display_title: %s\n", yamlQuote(display))
	}
	fmt.Fprintf(&b, "page_id: %d\nnamespace: %d\nrevision_id: %d\n", page.PageID, page.Namespace, page.Revision)
	if page.Timestamp != "" {
		fmt.Fprintf(&b, "timestamp: %s\n", yamlQuote(page.Timestamp))
	}
	fmt.Fprintf(&b, "source_url: %s\n", yamlQuote(baseURL+wikiURL(page.Title)))
	fmt.Fprintf(&b, "content_sha256: %s\nlicense: CC BY-SA 4.0\nsource: Tolkien Gateway\n", hex.EncodeToString(hash[:]))
	if page.ShortDesc != "" {
		fmt.Fprintf(&b, "short_description: %s\n", yamlQuote(page.ShortDesc))
	}
	var visible, hidden []crawlCategory
	for _, c := range page.Categories {
		if c.Hidden {
			hidden = append(hidden, c)
		} else {
			visible = append(visible, c)
		}
	}
	writeCategoryList(&b, "categories", visible)
	writeCategoryList(&b, "hidden_categories", hidden)
	// A category lists its members by sort key, which is the title unless
	// DEFAULTSORT or the category link itself says otherwise.
	if key := props["defaultsort"]; key != "" {
		fmt.Fprintf(&b, "defaultsort: %s\n", yamlQuote(key))
	}
	wroteKeys := false
	for _, c := range page.Categories {
		if c.SortKey == "" || c.SortKey == props["defaultsort"] {
			continue
		}
		if !wroteKeys {
			b.WriteString("sort_keys:\n")
			wroteKeys = true
		}
		fmt.Fprintf(&b, "  %s: %s\n", yamlQuote(strings.ReplaceAll(c.Name, "_", " ")), yamlQuote(c.SortKey))
	}
	for _, flag := range pageFlags {
		if _, ok := props[flag]; ok {
			fmt.Fprintf(&b, "%s: true\n", flag)
		}
	}
	if len(page.LangLinks) > 0 {
		b.WriteString("langlinks:\n")
		for _, l := range page.LangLinks {
			fmt.Fprintf(&b, "  - lang: %s\n    name: %s\n    title: %s\n    url: %s\n",
				yamlQuote(l.Lang), yamlQuote(l.Autonym), yamlQuote(l.Title), yamlQuote(l.URL))
		}
	}
	if len(page.Images) > 0 {
		b.WriteString("images:\n")
		for _, name := range page.Images {
			fmt.Fprintf(&b, "  - %s\n", yamlQuote(name))
		}
	}
	if file != nil {
		b.WriteString("file:\n")
		fmt.Fprintf(&b, "  url: %s\n  mime: %s\n  size: %d\n  width: %d\n  height: %d\n  sha1: %s\n  timestamp: %s\n",
			yamlQuote(file.URL), yamlQuote(file.Mime), file.Size, file.Width, file.Height, yamlQuote(file.SHA1), yamlQuote(file.Timestamp))
		if file.User != "" {
			fmt.Fprintf(&b, "  user: %s\n", yamlQuote(file.User))
		}
	}
	body, infobox := htmlToMarkdown(page.HTML, links, rel)
	if len(infobox) > 0 {
		b.WriteString("infobox:\n")
		for _, field := range infobox {
			fmt.Fprintf(&b, "  - label: %s\n    value: %s\n", yamlQuote(field[0]), yamlQuote(field[1]))
		}
	}
	b.WriteString("---\n\n")
	b.WriteString(body)
	return b.String()
}

func writeCategoryList(b *strings.Builder, key string, categories []crawlCategory) {
	if len(categories) == 0 {
		return
	}
	b.WriteString(key + ":\n")
	for _, c := range categories {
		fmt.Fprintf(b, "  - %s\n", yamlQuote(strings.ReplaceAll(c.Name, "_", " ")))
	}
}

// htmlToMarkdown converts MediaWiki parser output. It also returns the
// label/value rows of the first infobox, as plain text, for the front matter.
// With links, URLs are rewritten for a page written at rel.
func htmlToMarkdown(source string, links *corpusLinks, rel string) (string, [][2]string) {
	nodes, err := html.ParseFragment(strings.NewReader(source), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return rawHTMLBlock(source) + "\n", nil
	}
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	if len(nodes) == 1 && nodes[0].DataAtom == atom.Div && hasClass(nodes[0], "mw-parser-output") {
		root = nodes[0]
	}
	cleanTree(root)
	if links != nil {
		links.rewrite(root, rel)
	}
	c := &converter{}
	c.blocks(root)
	out := strings.Join(c.out, "\n\n")
	return strings.TrimSpace(out) + "\n", infoboxFields(root)
}

// cleanTree drops what the static site cannot use: edit section links,
// the parser's HTML comments, and the table of contents the parser puts in
// the body, which the skin replaces with its own in the sidebar.
func cleanTree(n *html.Node) {
	for child := n.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == html.CommentNode || (child.Type == html.ElementNode && (hasClass(child, "mw-editsection") || attr(child, "id") == "toc")) {
			n.RemoveChild(child)
		} else {
			cleanTree(child)
		}
		child = next
	}
}

type converter struct {
	out []string
}

func (c *converter) emit(block string) {
	if strings.TrimSpace(block) != "" {
		c.out = append(c.out, block)
	}
}

// blocks walks the block-level children of n, gathering loose inline runs
// into paragraphs the way the browser would.
func (c *converter) blocks(n *html.Node) {
	var run []*html.Node
	flush := func() {
		if len(run) == 0 {
			return
		}
		text := inlineMarkdown(run)
		run = nil
		c.emit(paragraph(text))
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if isInline(child) {
			run = append(run, child)
			continue
		}
		flush()
		c.block(child)
	}
	flush()
}

func (c *converter) block(n *html.Node) {
	switch n.DataAtom {
	case atom.P:
		if len(n.Attr) > 0 {
			c.emit(rawHTMLBlock(renderNode(n)))
			return
		}
		c.emit(paragraph(inlineMarkdown(children(n))))
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		c.emit(heading(n))
	case atom.Ul, atom.Ol:
		if md, ok := listMarkdown(n, 0); ok {
			c.emit(strings.TrimRight(md, "\n"))
			return
		}
		c.emit(rawHTMLBlock(renderNode(n)))
	case atom.Hr:
		if len(n.Attr) == 0 {
			c.emit("---")
			return
		}
		c.emit(rawHTMLBlock(renderNode(n)))
	case atom.Blockquote:
		if len(n.Attr) == 0 && simpleBlocks(n) {
			inner := &converter{}
			inner.blocks(n)
			lines := strings.Split(strings.Join(inner.out, "\n\n"), "\n")
			for i, line := range lines {
				lines[i] = strings.TrimRight("> "+line, " ")
			}
			c.emit(strings.Join(lines, "\n"))
			return
		}
		c.emit(rawHTMLBlock(renderNode(n)))
	default:
		c.emit(rawHTMLBlock(renderNode(n)))
	}
}

// simpleBlocks reports whether every child converts to plain Markdown.
func simpleBlocks(n *html.Node) bool {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if isInline(child) {
			continue
		}
		if child.DataAtom != atom.P || len(child.Attr) > 0 {
			return false
		}
	}
	return true
}

var inlineAtoms = map[atom.Atom]bool{
	atom.A: true, atom.Abbr: true, atom.B: true, atom.Bdi: true, atom.Br: true, atom.Cite: true,
	atom.Code: true, atom.Del: true, atom.Dfn: true, atom.Em: true, atom.I: true, atom.Img: true,
	atom.Ins: true, atom.Kbd: true, atom.Q: true, atom.S: true, atom.Samp: true, atom.Small: true,
	atom.Span: true, atom.Strong: true, atom.Sub: true, atom.Sup: true, atom.Time: true, atom.U: true,
	atom.Var: true, atom.Wbr: true, atom.Font: true, atom.Big: true, atom.Strike: true, atom.Tt: true,
}

func isInline(n *html.Node) bool {
	switch n.Type {
	case html.TextNode:
		return true
	case html.ElementNode:
		if !inlineAtoms[n.DataAtom] {
			return false
		}
		// An inline element wrapping block content is treated as a block.
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && !isInline(child) {
				return false
			}
		}
		return true
	}
	return false
}

func children(n *html.Node) []*html.Node {
	var out []*html.Node
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		out = append(out, child)
	}
	return out
}

// paragraph guards a converted inline run against being read as some other
// Markdown block: a heading, a list item, a quote or a thematic break.
func paragraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if m := leadingBlockRE.FindStringIndex(text); m != nil {
		text = text[:m[1]-1] + `\` + text[m[1]-1:]
	}
	return text
}

var leadingBlockRE = regexp.MustCompile(`^(?:[#>+=~-]|\d{1,9}[.)])`)

func heading(n *html.Node) string {
	level := int(n.Data[1] - '0')
	id := ""
	content := children(n)
	if span := firstChildWithClass(n, "mw-headline"); span != nil {
		id = attr(span, "id")
		content = children(span)
	} else {
		id = attr(n, "id")
	}
	text := strings.TrimSpace(inlineMarkdown(content))
	extra := len(n.Attr) > 0 && attr(n, "id") == ""
	if text == "" || extra || strings.Contains(text, "\n") || (id != "" && !safeAnchor(id)) {
		return rawHTMLBlock(renderNode(n))
	}
	line := strings.Repeat("#", level) + " " + text
	if id != "" {
		line += " {#" + id + "}"
	}
	return line
}

// safeAnchor reports whether goldmark's {#id} attribute syntax can carry id
// unchanged.
func safeAnchor(id string) bool {
	for _, r := range id {
		if r <= ' ' || strings.ContainsRune(`{}"'#<>\`, r) {
			return false
		}
	}
	return true
}

// listMarkdown converts a plain list. A list carrying attributes, or whose
// items hold anything other than inline content and nested plain lists,
// stays HTML.
func listMarkdown(n *html.Node, depth int) (string, bool) {
	if len(n.Attr) > 0 || depth > 6 {
		return "", false
	}
	var b strings.Builder
	indent := strings.Repeat("    ", depth)
	number := 1
	if start := attr(n, "start"); start != "" {
		return "", false
	}
	for li := n.FirstChild; li != nil; li = li.NextSibling {
		if li.Type == html.TextNode && strings.TrimSpace(li.Data) == "" {
			continue
		}
		if li.DataAtom != atom.Li || len(li.Attr) > 0 {
			return "", false
		}
		var inline []*html.Node
		var nested []string
		for child := li.FirstChild; child != nil; child = child.NextSibling {
			switch {
			case child.DataAtom == atom.Ul || child.DataAtom == atom.Ol:
				md, ok := listMarkdown(child, depth+1)
				if !ok {
					return "", false
				}
				nested = append(nested, md)
			case isInline(child):
				if len(nested) > 0 && !(child.Type == html.TextNode && strings.TrimSpace(child.Data) == "") {
					return "", false
				}
				inline = append(inline, child)
			default:
				return "", false
			}
		}
		marker := "- "
		if n.DataAtom == atom.Ol {
			marker = fmt.Sprintf("%d. ", number)
			number++
		}
		text := strings.TrimSpace(inlineMarkdown(inline))
		if strings.Contains(text, "\n") {
			return "", false
		}
		if text == "" {
			text = "&#8203;"
		} else if leadingBlockRE.MatchString(text) {
			text = paragraph(text)
		}
		b.WriteString(indent + marker + text + "\n")
		for _, md := range nested {
			b.WriteString(md)
		}
	}
	return b.String(), b.Len() > 0
}

// inlineMarkdown converts a run of inline nodes.
func inlineMarkdown(nodes []*html.Node) string {
	var b strings.Builder
	for _, n := range nodes {
		writeInline(&b, n)
	}
	return collapseSpaces(b.String())
}

var spaceRunRE = regexp.MustCompile(`[ \t\r\n]+`)

func collapseSpaces(s string) string {
	// Hard breaks are emitted as "<br>" and survive; every other run of
	// whitespace, including the parser's newlines, becomes one space.
	return spaceRunRE.ReplaceAllString(s, " ")
}

func writeInline(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(escapeMarkdown(n.Data))
		return
	case html.ElementNode:
	default:
		return
	}
	switch n.DataAtom {
	case atom.B, atom.Strong:
		if len(n.Attr) == 0 {
			writeEmphasis(b, n, "**")
			return
		}
	case atom.I, atom.Em:
		if len(n.Attr) == 0 {
			writeEmphasis(b, n, "*")
			return
		}
	case atom.Br:
		if len(n.Attr) == 0 {
			b.WriteString("<br>")
			return
		}
	case atom.A:
		if md, ok := markdownLink(n); ok {
			b.WriteString(md)
			return
		}
	}
	b.WriteString(inlineHTML(n))
}

// writeEmphasis wraps converted content in a delimiter, falling back to the
// HTML element when the delimiter would not be recognised: leading or
// trailing spaces inside, or empty content.
func writeEmphasis(b *strings.Builder, n *html.Node, delim string) {
	inner := inlineMarkdown(children(n))
	trimmed := strings.TrimSpace(inner)
	if trimmed == "" || strings.Contains(trimmed, "*") || strings.HasPrefix(trimmed, "<") || strings.HasSuffix(trimmed, ">") {
		b.WriteString(inlineHTML(n))
		return
	}
	lead := inner[:len(inner)-len(strings.TrimLeft(inner, " "))]
	trail := inner[len(strings.TrimRight(inner, " ")):]
	b.WriteString(lead + delim + trimmed + delim + trail)
}

// markdownLink converts a plain internal or anchor link. Links whose class
// carries meaning to the stylesheet (external, red, interwiki, file) stay
// HTML.
func markdownLink(n *html.Node) (string, bool) {
	href := ""
	for _, a := range n.Attr {
		switch a.Key {
		case "href":
			href = a.Val
		case "title":
			// Left out: the site gives every wiki link its target's title
			// again, and the Markdown reads more easily without it.
		case "class":
			if a.Val != "mw-redirect" {
				return "", false
			}
		default:
			return "", false
		}
	}
	if !(strings.HasPrefix(href, "/wiki/") || strings.HasPrefix(href, wikiOrigin+"/wiki/") || strings.HasPrefix(href, "#") || isCorpusLink(href)) || strings.ContainsAny(href, " <>\n") {
		return "", false
	}
	text := strings.TrimSpace(inlineMarkdown(children(n)))
	if text == "" || strings.Contains(text, "<") {
		return "", false
	}
	if strings.ContainsAny(href, "()") {
		// Titles like "Ring (title)" are common; the pointy-bracket form
		// keeps their parentheses from closing the link early.
		href = "<" + href + ">"
	}
	return "[" + text + "](" + href + ")", true
}

var markdownSpecial = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`,
	`<`, `&lt;`, `>`, `&gt;`, `&`, `&amp;`, `|`, `\|`, `~`, `\~`,
)

func escapeMarkdown(s string) string {
	return markdownSpecial.Replace(s)
}

// inlineHTML serialises an element for use inside a Markdown paragraph,
// where a newline could end the paragraph early.
func inlineHTML(n *html.Node) string {
	return strings.ReplaceAll(renderNode(n), "\n", " ")
}

func renderNode(n *html.Node) string {
	var b strings.Builder
	html.Render(&b, n)
	return b.String()
}

var blankLineRE = regexp.MustCompile(`\n[ \t]*\n`)

// rawHTMLBlock makes a serialised element safe as a CommonMark HTML block:
// it must start at the margin and contain no blank line, which would end it.
func rawHTMLBlock(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "<pre") && !strings.Contains(s, "<textarea") {
		// Outside preformatted text a blank line is only whitespace.
		for blankLineRE.MatchString(s) {
			s = blankLineRE.ReplaceAllString(s, "\n")
		}
		return s
	}
	for blankLineRE.MatchString(s) {
		s = blankLineRE.ReplaceAllString(s, "\n<!-- -->\n")
	}
	return s
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func firstChildWithClass(n *html.Node, class string) *html.Node {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && hasClass(child, class) {
			return child
		}
	}
	return nil
}

func findFirst(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirst(child, match); found != nil {
			return found
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (n.DataAtom == atom.Style || n.DataAtom == atom.Sup && hasClass(n, "reference")) {
			return
		}
		if n.DataAtom == atom.Br {
			b.WriteString("; ")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.TrimSpace(collapseSpaces(b.String()))
}

// infoboxFields reads label/data rows from the first Tolkien Gateway infobox.
func infoboxFields(root *html.Node) [][2]string {
	box := findFirst(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.DataAtom == atom.Table && hasClass(n, "tg-infobox")
	})
	if box == nil {
		return nil
	}
	var fields [][2]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.DataAtom == atom.Tr {
			var label, value string
			for cell := n.FirstChild; cell != nil; cell = cell.NextSibling {
				if hasClass(cell, "tg-infobox-label") {
					label = textContent(cell)
				}
				if hasClass(cell, "tg-infobox-data") {
					value = textContent(cell)
				}
			}
			if label != "" && value != "" {
				fields = append(fields, [2]string{label, value})
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(box)
	return fields
}
