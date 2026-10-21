package main

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// markdown renders corpus bodies. The HTML blocks the converter kept must
// pass through untouched, and headings carry their wiki anchors as
// {#attributes}.
var markdown = goldmark.New(
	goldmark.WithParserOptions(parser.WithAttribute()),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

// renderedPage is a page body ready for the shell.
type renderedPage struct {
	content string // inside #mw-content-text, before the print footer
	toc     string // the sidebar table of contents, or ""
	// untranslated marks a page shown in English in another language's site.
	untranslated bool
	// borrow is the URL of the English page whose body tgw.js loads in
	// place of content, for a page not yet translated.
	borrow string
}

type tocEntry struct {
	level  int // MediaWiki's toclevel, 1 for the outermost
	number string
	anchor string
	line   string
}

// renderContent turns a Markdown body into the wiki's parser output, then
// adds what MediaWiki puts around it for category and file pages.
func (s *site) renderContent(p *sitePage, body []byte) (renderedPage, error) {
	var buf bytes.Buffer
	if err := markdown.Convert(body, &buf); err != nil {
		return renderedPage{}, err
	}
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(&buf, root)
	if err != nil {
		return renderedPage{}, err
	}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	var toc []tocEntry
	s.rewriteTree(root, &toc, s.corpusRel(p))
	numberTOC(toc)
	wrapSections(root)

	var b strings.Builder
	parserOutput := `<div class="mw-parser-output">` + renderChildren(root) + `</div>`
	switch p.Meta.Namespace {
	case 6:
		b.WriteString(s.filePageContent(p, parserOutput))
	case 14:
		b.WriteString(parserOutput)
		b.WriteString(s.categoryListing(p.Meta.Title))
	default:
		b.WriteString(parserOutput)
	}
	out := renderedPage{content: b.String()}
	if !p.Meta.NoTOC && (len(toc) >= 4 || p.Meta.ForceTOC && len(toc) > 0) {
		out.toc = s.tocHTML(toc)
	}
	return out, nil
}

// rewriteTree points links and images at the mirror, restores MediaWiki's
// heading markup and collects the table of contents. from is the page's
// corpus path, which relative links start from.
func (s *site) rewriteTree(n *html.Node, toc *[]tocEntry, from string) {
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			if entry, ok := headline(n); ok {
				*toc = append(*toc, entry)
			}
		}
		linkTitle := ""
		for i := range n.Attr {
			a := &n.Attr[i]
			switch a.Key {
			case "href":
				if n.DataAtom == atom.A || n.DataAtom == atom.Area {
					a.Val, linkTitle = s.rewriteHref(a.Val, from)
				}
			case "src", "poster", "data-src":
				a.Val = s.imageURL(a.Val)
			case "srcset":
				a.Val = s.rewriteSrcset(n, a.Val)
			case "style":
				if strings.Contains(a.Val, "/w/") {
					a.Val = styleURLRE.ReplaceAllStringFunc(a.Val, func(m string) string {
						inner := strings.Trim(styleURLRE.FindStringSubmatch(m)[1], `"' `)
						return "url(" + s.imageURL(inner) + ")"
					})
				}
			}
		}
		// The wiki titles every link to a page; the corpus leaves that out.
		if linkTitle != "" && attr(n, "title") == "" && !hasClass(n, "external") {
			n.Attr = append(n.Attr, html.Attribute{Key: "title", Val: linkTitle})
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.rewriteTree(c, toc, from)
	}
}

var styleURLRE = regexp.MustCompile(`url\(([^)]*)\)`)

// headline gives a Markdown heading back the span MediaWiki wraps heading
// text in, which the skin styles and anchors to, and reports it for the
// table of contents. Headings written as plain HTML on the wiki have no
// headline span and are not listed, as on the wiki.
func headline(h *html.Node) (tocEntry, bool) {
	var span *html.Node
	for c := h.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Span && hasClass(c, "mw-headline") {
			span = c
			break
		}
	}
	if span == nil {
		id := attr(h, "id")
		if id == "" {
			return tocEntry{}, false
		}
		span = &html.Node{Type: html.ElementNode, Data: "span", DataAtom: atom.Span,
			Attr: []html.Attribute{{Key: "class", Val: "mw-headline"}, {Key: "id", Val: id}}}
		for c := h.FirstChild; c != nil; {
			next := c.NextSibling
			h.RemoveChild(c)
			span.AppendChild(c)
			c = next
		}
		h.AppendChild(span)
		removeAttr(h, "id")
	}
	anchor := attr(span, "id")
	if anchor == "" {
		return tocEntry{}, false
	}
	level, _ := strconv.Atoi(h.Data[1:])
	return tocEntry{level: level, anchor: anchor, line: tocLine(span)}, true
}

// tocLine is a heading's text as the table of contents shows it: inline
// emphasis kept, links and everything else reduced to their text.
func tocLine(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				b.WriteString(html.EscapeString(c.Data))
			case html.ElementNode:
				switch c.DataAtom {
				case atom.I, atom.B, atom.Em, atom.Strong, atom.Sup, atom.Sub, atom.S, atom.Del, atom.Ins, atom.U, atom.Bdi:
					b.WriteString("<" + c.Data + ">")
					walk(c)
					b.WriteString("</" + c.Data + ">")
				case atom.Style, atom.Script, atom.Img:
				default:
					walk(c)
				}
			}
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// numberTOC assigns toclevels and section numbers the way MediaWiki's
// Parser::formatHeadings does, where a level-4 heading under a level-2 one
// is still only one level deeper.
func numberTOC(toc []tocEntry) {
	levelCount := map[int]int{}
	sublevelCount := map[int]int{}
	toclevel, prevLevel := 0, 0
	for i := range toc {
		level := toc[i].level
		if toclevel == 0 {
			toclevel = 1
		} else if level > prevLevel {
			toclevel++
			sublevelCount[toclevel] = 0
		} else if level < prevLevel && toclevel > 1 {
			found := false
			for j := toclevel; j > 0; j-- {
				if levelCount[j] == level {
					toclevel, found = j, true
					break
				} else if levelCount[j] < level {
					toclevel, found = j+1, true
					break
				}
			}
			if !found {
				toclevel = 1
			}
		}
		levelCount[toclevel] = level
		sublevelCount[toclevel]++
		var parts []string
		for j := 1; j <= toclevel; j++ {
			if sublevelCount[j] > 0 {
				parts = append(parts, strconv.Itoa(sublevelCount[j]))
			}
		}
		toc[i].level = toclevel
		toc[i].number = strings.Join(parts, ".")
		prevLevel = level
	}
}

// tocHTML is Citizen's sidebar table of contents.
func (s *site) tocHTML(toc []tocEntry) string {
	var b strings.Builder
	b.WriteString(`<div id="citizen-toc" class="citizen-toc citizen-dropdown"><details class="citizen-dropdown-details">` +
		`<summary class="citizen-dropdown-summary" title="` + s.ui("Contents") + `" aria-details="mw-panel-toc"><span class="citizen-ui-icon mw-ui-icon-wikimedia-listBullet"></span><span>` + s.ui("Contents") + `</span></summary></details>` +
		`<nav id="mw-panel-toc" class="citizen-toc-card citizen-menu__card" role="navigation" aria-labelledby="mw-panel-toc-label">` +
		`<a class="citizen-toc-top citizen-toc-link" title="" href="#top"><div class="citizen-ui-icon mw-ui-icon-first mw-ui-icon-wikimedia-first"></div><div class="citizen-toc-text">` + s.ui("Back to top") + `</div></a>` +
		`<div class="citizen-menu__heading">` + s.ui("Contents") + `</div><ul class="citizen-toc-contents" id="mw-panel-toc-list">`)
	var write func(i int) int
	// write renders entry i and its descendants, returning the next index.
	write = func(i int) int {
		e := toc[i]
		id := htmlEscape(e.anchor)
		fmt.Fprintf(&b, `<li id="toc-%s" class="citizen-toc-list-item citizen-toc-level-%d"><a class="citizen-toc-link" href="#%s"><div class="citizen-toc-indicator"></div>`+
			`<div class="citizen-toc-content"><div class="citizen-toc-text"><span class="citizen-toc-numb">%s</span><span class="citizen-toc-heading">%s</span></div></div></a>`,
			id, e.level, id, e.number, e.line)
		j := i + 1
		hasChildren := j < len(toc) && toc[j].level > e.level
		if hasChildren && e.level == 1 {
			fmt.Fprintf(&b, `<button aria-controls="toc-%s-sublist" class="citizen-toc-toggle" aria-expanded="false"><span class="citizen-ui-icon mw-ui-icon-wikimedia-collapse"></span><span></span></button>`, id)
		}
		fmt.Fprintf(&b, `<ul id="toc-%s-sublist" class="citizen-toc-list">`, id)
		for j < len(toc) && toc[j].level > e.level {
			j = write(j)
		}
		b.WriteString(`</ul></li>`)
		return j
	}
	for i := 0; i < len(toc); {
		i = write(i)
	}
	b.WriteString(`</ul></nav></div>`)
	return b.String()
}

// wrapSections reproduces Citizen's collapsible sections: every top-level
// h2 becomes a section heading followed by a section holding everything up
// to the next one, and the lead goes in section 0.
func wrapSections(root *html.Node) {
	var children []*html.Node
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		root.RemoveChild(c)
	}
	newSection := func(n int) *html.Node {
		sec := &html.Node{Type: html.ElementNode, Data: "section", DataAtom: atom.Section,
			Attr: []html.Attribute{{Key: "class", Val: "citizen-section"}, {Key: "id", Val: fmt.Sprintf("citizen-section-%d", n)}}}
		root.AppendChild(sec)
		return sec
	}
	current := newSection(0)
	count := 0
	for _, c := range children {
		if c.Type == html.ElementNode && c.DataAtom == atom.H2 {
			count++
			addClass(c, "citizen-section-heading")
			c.InsertBefore(&html.Node{Type: html.ElementNode, Data: "span", DataAtom: atom.Span,
				Attr: []html.Attribute{{Key: "class", Val: "citizen-section-indicator citizen-ui-icon mw-ui-icon-wikimedia-collapse"}}}, c.FirstChild)
			root.AppendChild(c)
			current = newSection(count)
			continue
		}
		current.AppendChild(c)
	}
}

// rewriteHref points a link at the mirrored page when there is one, and
// at the live wiki for everything the mirror does not carry. It also
// returns the title of the wiki page linked to, if any.
func (s *site) rewriteHref(href, from string) (string, string) {
	switch {
	case href == "" || strings.HasPrefix(href, "#"):
		return href, ""
	case strings.HasPrefix(href, "//"):
		href = "https:" + href
	case isCorpusLink(href):
		name, fragment, _ := strings.Cut(href, "#")
		rel, err := url.PathUnescape(name)
		if err == nil {
			if p := s.byRel[path.Join(path.Dir(from), rel)]; p != nil {
				return s.titleURL(p.Meta.Title, fragment), p.Meta.Title
			}
		}
		return href, ""
	}
	for _, prefix := range []string{wikiOrigin, "http://tolkiengateway.net", "https://www.tolkiengateway.net"} {
		if rest, ok := strings.CutPrefix(href, prefix); ok && strings.HasPrefix(rest, "/") {
			href = rest
			break
		}
	}
	if rest, ok := strings.CutPrefix(href, "/wiki/"); ok {
		return s.wikiHref(rest)
	}
	if strings.HasPrefix(href, "/") {
		return wikiOrigin + href, ""
	}
	return href, ""
}

// wikiHref resolves the part of a /wiki/ URL after the prefix, and gives
// the title it names.
func (s *site) wikiHref(rest string) (string, string) {
	name, fragment, _ := strings.Cut(rest, "#")
	if strings.Contains(name, "?") {
		return wikiOrigin + "/wiki/" + rest, ""
	}
	title, err := url.PathUnescape(name)
	if err != nil {
		return wikiOrigin + "/wiki/" + rest, ""
	}
	title = normalizeTitle(title)
	if title == "Special:Random" {
		return s.base + "/wiki/Special:Random.html", title
	}
	ns, _ := splitTitle(title)
	if _, ok := namespaceDirs[ns]; !ok {
		return wikiOrigin + "/wiki/" + rest, title
	}
	return s.titleURL(title, fragment), title
}

// normalizeTitle applies MediaWiki's title rules the links rely on:
// underscores are spaces and the first letter of the name is upper case.
func normalizeTitle(title string) string {
	title = strings.TrimSpace(strings.ReplaceAll(title, "_", " "))
	ns, name := splitTitle(title)
	name = strings.TrimSpace(name)
	if r, size := utf8.DecodeRuneInString(name); r != utf8.RuneError {
		name = string(unicode.ToUpper(r)) + name[size:]
	}
	if ns == 0 {
		return name
	}
	return namespaceNames[ns] + ":" + name
}

// imageURL maps a wiki image URL to the bundled copy, or to the wiki itself
// when the copy is not bundled.
func (s *site) imageURL(src string) string {
	local, remote, ok := s.imageTarget(src)
	if !ok {
		return remote
	}
	return local
}

// imageTarget returns the bundled URL of an image, if bundled, and the
// remote URL it falls back to.
func (s *site) imageTarget(src string) (local, remote string, ok bool) {
	rest := src
	for _, prefix := range []string{wikiOrigin, "//tolkiengateway.net", "http://tolkiengateway.net"} {
		if r, found := strings.CutPrefix(rest, prefix); found {
			rest = r
			break
		}
	}
	if !strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "//") {
		return "", src, false
	}
	remote = wikiOrigin + rest
	if !strings.HasPrefix(rest, "/w/images/") {
		return "", remote, false
	}
	rel, valid := imagePath("https://x" + rest)
	if !valid || !s.images.has(rel) {
		return "", remote, false
	}
	return s.assets + "/images/" + wikiEscape(rel), remote, true
}

// rewriteSrcset keeps the higher-density candidates that are bundled. When
// the plain src is bundled but a candidate is not, the candidate is dropped
// rather than hotlinked, so a sharp screen shows the local thumbnail.
func (s *site) rewriteSrcset(img *html.Node, srcset string) string {
	_, _, srcLocal := s.imageTarget(attr(img, "src"))
	var out []string
	for _, candidate := range strings.Split(srcset, ",") {
		fields := strings.Fields(candidate)
		if len(fields) == 0 {
			continue
		}
		local, remote, ok := s.imageTarget(fields[0])
		switch {
		case ok:
			fields[0] = local
		case srcLocal:
			continue
		default:
			fields[0] = remote
		}
		out = append(out, strings.Join(fields, " "))
	}
	return strings.Join(out, ", ")
}

func renderChildren(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&b, c)
	}
	return b.String()
}

func removeAttr(n *html.Node, key string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}

func addClass(n *html.Node, class string) {
	for i, a := range n.Attr {
		if a.Key == "class" {
			n.Attr[i].Val = strings.TrimSpace(a.Val + " " + class)
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: class})
}

func htmlEscape(s string) string {
	return html.EscapeString(s)
}
