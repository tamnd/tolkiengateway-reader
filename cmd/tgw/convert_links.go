package main

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// templateStylesPath is where convert gathers the wiki's TemplateStyles,
// below the corpus root, and publish serves them from.
const templateStylesPath = "assets/templatestyles.css"

// corpusLinks rewrites the parser output's site-relative URLs so the
// Markdown reads correctly wherever it is viewed: a link to a mirrored page
// becomes a relative path to its corpus file, and everything else points at
// the live wiki. It also collects the TemplateStyles the pages carry inline,
// so they can be kept once in a shared stylesheet instead of in every page.
type corpusLinks struct {
	files  map[string]string // title → path below content/<lang>/
	styles map[string]string // data-mw-deduplicate key → stylesheet
}

func newCorpusLinks(items []inventoryItem, paths map[int]string) *corpusLinks {
	l := &corpusLinks{files: map[string]string{}, styles: map[string]string{}}
	for _, item := range items {
		if rel, ok := paths[item.PageID]; ok {
			l.files[normalizeTitle(item.Title)] = rel
		}
	}
	return l
}

// rewrite updates every URL below n for a page written at from, a path
// below content/<lang>/, and lifts the TemplateStyles out of the tree.
func (l *corpusLinks) rewrite(n *html.Node, from string) {
	for child := n.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == html.ElementNode {
			switch {
			case child.DataAtom == atom.Style && attr(child, "data-mw-deduplicate") != "":
				if child.FirstChild != nil && l.styles != nil {
					l.styles[attr(child, "data-mw-deduplicate")] = child.FirstChild.Data
				}
				n.RemoveChild(child)
				child = next
				continue
			case child.DataAtom == atom.Link && attr(child, "rel") == "mw-deduplicated-inline-style":
				n.RemoveChild(child)
				child = next
				continue
			}
			for i := range child.Attr {
				a := &child.Attr[i]
				switch a.Key {
				case "href":
					a.Val = l.href(from, a.Val)
				case "src", "poster", "data-src":
					a.Val = absoluteWikiURL(a.Val)
				case "srcset":
					candidates := strings.Split(a.Val, ",")
					for j, c := range candidates {
						fields := strings.Fields(c)
						if len(fields) > 0 {
							fields[0] = absoluteWikiURL(fields[0])
							candidates[j] = strings.Join(fields, " ")
						}
					}
					a.Val = strings.Join(candidates, ", ")
				}
			}
		}
		l.rewrite(child, from)
		child = next
	}
}

// href maps one link.
func (l *corpusLinks) href(from, href string) string {
	rest, ok := strings.CutPrefix(href, "/wiki/")
	if !ok {
		return absoluteWikiURL(href)
	}
	name, fragment, hasFragment := strings.Cut(rest, "#")
	if strings.Contains(name, "?") {
		return wikiOrigin + href
	}
	title, err := url.PathUnescape(name)
	if err != nil {
		return wikiOrigin + href
	}
	rel, ok := l.files[normalizeTitle(title)]
	if !ok {
		return wikiOrigin + href
	}
	target := relativePath(path.Dir(from), rel)
	if rel == from && hasFragment {
		target = ""
	}
	if hasFragment {
		target += "#" + fragment
	}
	return target
}

// absoluteWikiURL points a site-relative URL at the live wiki.
func absoluteWikiURL(u string) string {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return wikiOrigin + u
	}
	return u
}

// relativePath is the URL path from directory dir to file target, both
// below the same root, with the characters a link destination cannot carry
// escaped.
func relativePath(dir, target string) string {
	from := strings.Split(dir, "/")
	to := strings.Split(target, "/")
	common := 0
	for common < len(from) && common < len(to)-1 && from[common] == to[common] {
		common++
	}
	var parts []string
	for range from[common:] {
		parts = append(parts, "..")
	}
	for _, seg := range to[common:] {
		parts = append(parts, linkEscape(seg))
	}
	return strings.Join(parts, "/")
}

// linkEscape escapes a file name for a link: the percent signs the file
// names themselves carry and anything that would end or split the URL.
// Letters outside ASCII stay readable.
func linkEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c == 0x7f || strings.IndexByte(`%#?<>"\`+"`", c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isCorpusLink reports whether href is a relative link to a corpus file,
// as written by corpusLinks.
func isCorpusLink(href string) bool {
	name, _, _ := strings.Cut(href, "#")
	return strings.HasSuffix(name, ".md") && !strings.Contains(name, ":") && !strings.HasPrefix(name, "/")
}

// writeTemplateStyles saves the collected TemplateStyles below the corpus
// root, sorted by key so an unchanged wiki gives an unchanged file.
func (l *corpusLinks) writeTemplateStyles(root string) error {
	keys := make([]string, 0, len(l.styles))
	for k := range l.styles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("/* TemplateStyles from Tolkien Gateway (CC BY-SA 4.0), gathered by tgw convert. */\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "/* %s */\n%s\n", strings.ReplaceAll(k, "*/", "* /"), strings.TrimSpace(l.styles[k]))
	}
	target := filepath.Join(root, filepath.FromSlash(templateStylesPath))
	if old, err := os.ReadFile(target); err == nil && string(old) == b.String() {
		return nil
	}
	return atomicWrite(target, []byte(b.String()))
}
