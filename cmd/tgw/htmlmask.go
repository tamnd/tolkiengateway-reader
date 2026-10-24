package main

import (
	_ "embed"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// Both translators keep blocks of raw HTML verbatim, and on the wiki these
// are much of what a reader sees: the infobox, the hatnote, the quote at the
// head of a page, the captions, the adaptations list, the navboxes. So
// within such a block every run of text a reader takes as one phrase, a
// table cell, a caption, a list item, is translated on its own, its inline
// tags masked as in a paragraph:
//
//	<a href="../B/Bag_End.md" title="Bag End">home</a>  ->  <h3>home</h3>
//	<i>The Hobbit</i>                                  ->  <x4/>
//
// A run with no lowercase word, a name or a list of names, is left alone,
// and so are citations.

// htmlInline are the tags that sit inside a run of text rather than end it.
var htmlInline = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "br": true, "cite": true, "code": true,
	"em": true, "i": true, "q": true, "s": true, "small": true, "span": true, "strong": true,
	"sub": true, "sup": true, "u": true, "wbr": true,
}

// htmlSkip are the elements whose text is never translated.
var htmlSkip = map[string]bool{"style": true, "script": true, "math": true, "code": true, "pre": true}

type htmlSpan struct{ start, end int }

// htmlSpans finds the runs of text in a block of raw HTML, each from its
// first word to its last, with the tags that wrap a whole run left out.
func htmlSpans(block string) []htmlSpan {
	z := xhtml.NewTokenizer(strings.NewReader(block))
	var spans []htmlSpan
	pos, start, end := 0, -1, -1
	skip := 0
	flush := func() {
		if start >= 0 && end > start {
			spans = append(spans, htmlSpan{start, end})
		}
		start, end = -1, -1
	}
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		raw := z.Raw()
		at := pos
		pos += len(raw)
		name, _ := z.TagName()
		tag := string(name)
		switch tt {
		case xhtml.TextToken:
			if skip > 0 {
				continue
			}
			text := string(raw)
			if strings.TrimSpace(text) == "" {
				continue
			}
			lead := len(text) - len(strings.TrimLeft(text, " \t\n"))
			if start < 0 {
				start = at + lead
			}
			end = at + len(strings.TrimRight(text, " \t\n"))
		case xhtml.StartTagToken, xhtml.EndTagToken, xhtml.SelfClosingTagToken:
			if htmlSkip[tag] {
				if tt == xhtml.StartTagToken {
					skip++
				} else if tt == xhtml.EndTagToken && skip > 0 {
					skip--
				}
				flush()
				continue
			}
			if htmlInline[tag] {
				if start >= 0 && tt != xhtml.StartTagToken {
					// A closing tag belongs to the run it closes; whether
					// it stays inside is settled by trimSpan.
					end = at + len(raw)
				}
				continue
			}
			flush()
		default:
			flush()
		}
	}
	flush()
	var out []htmlSpan
	for _, s := range spans {
		if s, ok := trimSpan(block, s); ok {
			out = append(out, s)
		}
	}
	return out
}

var (
	htmlOpenTag  = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9]*)\b[^>]*>`)
	htmlCloseTag = regexp.MustCompile(`</([a-zA-Z][a-zA-Z0-9]*)\s*>$`)
	htmlAnyTag   = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)\b[^>]*>`)
	htmlSupSpan  = regexp.MustCompile(`(?s)<sup\b[^>]*>.*?</sup>`)
)

// trimSpan makes a run's tags balance. A closing tag of an element the
// run did not open is dropped when it ends the run, as </td> would, and
// otherwise the run takes in the opening tag right before it, as with a
// link the run starts in.
func trimSpan(block string, s htmlSpan) (htmlSpan, bool) {
	for s.end > s.start {
		var stack []string
		bad := -1 // end of the first unmatched closing tag
		for _, loc := range htmlAnyTag.FindAllStringSubmatchIndex(block[s.start:s.end], -1) {
			t := block[s.start+loc[0] : s.start+loc[1]]
			name := strings.ToLower(block[s.start+loc[2] : s.start+loc[3]])
			if strings.HasPrefix(t, "</") {
				if len(stack) > 0 && stack[len(stack)-1] == name {
					stack = stack[:len(stack)-1]
					continue
				}
				bad = s.start + loc[1]
				break
			}
			if name != "br" && name != "wbr" && name != "img" && !strings.HasSuffix(t, "/>") {
				stack = append(stack, name)
			}
		}
		switch {
		case bad < 0 && len(stack) == 0:
			return s, true
		case bad < 0:
			return s, false
		case bad == s.end:
			m := htmlCloseTag.FindStringIndex(block[s.start:s.end])
			s.end = s.start + len(strings.TrimRight(block[s.start:s.start+m[0]], " \t\n"))
		default:
			i := strings.LastIndex(block[:s.start], "<")
			if i < 0 {
				return s, false
			}
			m := htmlOpenTag.FindStringSubmatch(block[i:s.start])
			if m == nil || len(m[0]) != s.start-i || !htmlInline[strings.ToLower(m[1])] {
				return s, false
			}
			s.start = i
		}
	}
	return s, false
}

// htmlVisible is the text a reader sees in a run, footnote marks dropped.
func htmlVisible(span string) string {
	span = htmlSupSpan.ReplaceAllString(span, "")
	span = htmlAnyTag.ReplaceAllString(span, " ")
	return strings.Join(strings.Fields(html.UnescapeString(span)), " ")
}

// htmlStop are English words a title or a list of names may have in lower
// case.
var htmlStop = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "by": true, "de": true, "for": true,
	"from": true, "in": true, "of": true, "on": true, "or": true, "the": true, "to": true, "with": true,
}

var htmlWord = regexp.MustCompile(`[\p{L}][\p{L}'’-]*`)

// englishWords are the lowercase words of the English prose, those used
// at least three times and mostly outside italics and quotes, where the
// wiki writes its Elvish: house, plural, married, but not mellon or zadan.
//
//go:embed rules/en-words.txt
var englishWordList string

var englishWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(englishWordList) {
		m[w] = true
	}
	return m
}()

// htmlTranslatable reports whether a run reads as a phrase to translate
// rather than names or a citation.
func htmlTranslatable(span string) bool {
	if strings.Contains(span, "mw-cite-backlink") || strings.Contains(span, "reference-text") {
		return false
	}
	text := htmlVisible(span)
	if isCitation(strings.TrimLeft(text, "↑^ 0123456789.")) {
		return false
	}
	// A word in English, not a name, nor Elvish, nor a table of sounds.
	for _, w := range htmlWord.FindAllString(text, -1) {
		if hasLower(w[:1]) && !htmlStop[w] && englishWords[strings.Trim(w, "'’-")] {
			return true
		}
	}
	return false
}

// repairSpans puts the English back in the runs of a page's HTML that an
// earlier fill translated though they were not English, those the table of
// phrases and the glossary do not give: a row of sounds, a word in Adûnaic.
// It returns the page and how many runs it put back.
func repairSpans(en, vi string, gloss *glossary) (string, int) {
	blocks := func(text string) (out []translationChunk) {
		return splitTranslation(text[len(frontMatter(text)):])
	}
	ec, vc := blocks(en), blocks(vi)
	var eb []string
	for _, c := range ec {
		if c.verbatim {
			eb = append(eb, c.text)
		}
	}
	nv := 0
	for _, c := range vc {
		if c.verbatim {
			nv++
		}
	}
	if len(eb) != nv {
		return vi, 0
	}
	var b strings.Builder
	b.WriteString(frontMatter(vi))
	n, k := 0, 0
	for _, c := range vc {
		if !c.verbatim {
			b.WriteString(c.text)
			continue
		}
		e, v := eb[k], c.text
		k++
		es, vs := htmlSpans(e), htmlSpans(v)
		if len(es) != len(vs) {
			b.WriteString(v)
			continue
		}
		last := 0
		for j := range es {
			es, vs := e[es[j].start:es[j].end], v[vs[j].start:vs[j].end]
			if es == vs || htmlTranslatable(es) {
				continue
			}
			if _, ok := htmlPhrases.phraseFor(es); ok {
				continue
			}
			if gloss != nil {
				if _, ok := phraseTable(gloss.terms).phraseFor(es); ok {
					continue
				}
			}
			b.WriteString(v[last:vsStart(v, vs, last)])
			b.WriteString(es)
			last = vsStart(v, vs, last) + len(vs)
			n++
		}
		b.WriteString(v[last:])
	}
	return b.String(), n
}

// vsStart finds a run in a block from where the last one ended.
func vsStart(block, span string, from int) int { return from + strings.Index(block[from:], span) }

// htmlPair are the tags whose text is translated with them kept around it.
var htmlPair = map[string]bool{"a": true, "b": true, "strong": true, "em": true, "span": true, "small": true, "abbr": true, "q": true, "cite": true, "u": true, "s": true, "bdi": true}

// maskHTML masks a run of HTML the way maskMarkdown masks a paragraph.
func maskHTML(span string, gloss *glossary) (m masked, err error) {
	m = masked{parts: map[string]*maskPart{}}
	n := 0
	id := func(kind string, p *maskPart) string {
		n++
		tag := kind + strconv.Itoa(n)
		m.parts[tag] = p
		return tag
	}
	opaque := func(s string) string { return "<" + id("x", &maskPart{payload: s}) + "/>" }
	type open struct {
		name, tag string
	}
	var stack []open
	var b strings.Builder
	rest := span
	for rest != "" {
		loc := htmlAnyTag.FindStringSubmatchIndex(rest)
		if loc == nil {
			b.WriteString(maskHTMLText(rest, opaque))
			break
		}
		b.WriteString(maskHTMLText(rest[:loc[0]], opaque))
		raw := rest[loc[0]:loc[1]]
		name := strings.ToLower(rest[loc[2]:loc[3]])
		rest = rest[loc[1]:]
		if strings.HasPrefix(raw, "</") {
			if len(stack) == 0 || stack[len(stack)-1].name != name {
				return m, fmt.Errorf("unbalanced </%s>", name)
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			m.parts[top.tag].extra = raw
			b.WriteString("</" + top.tag + ">")
			continue
		}
		if strings.HasSuffix(raw, "/>") || name == "br" || name == "wbr" || name == "img" {
			b.WriteString(opaque(raw))
			continue
		}
		// The element whole, to judge it as a unit.
		close := "</" + name
		depth, j := 1, 0
		for depth > 0 {
			l := htmlAnyTag.FindStringSubmatchIndex(rest[j:])
			if l == nil {
				return m, fmt.Errorf("unclosed <%s>", name)
			}
			t := rest[j+l[0] : j+l[1]]
			if strings.EqualFold(rest[j+l[2]:j+l[3]], name) {
				if strings.HasPrefix(t, close) {
					depth--
				} else if !strings.HasSuffix(t, "/>") {
					depth++
				}
			}
			j += l[1]
		}
		whole := raw + rest[:j]
		inner := rest[:j-len(htmlCloseTag.FindString(rest[:j]))]
		text := htmlVisible(inner)
		switch {
		case name == "sup" || name == "code":
			b.WriteString(opaque(whole))
			rest = rest[j:]
			continue
		case name == "i" && (len(strings.Fields(text)) <= maxTitleWords || isWorkTitle(text)):
			// A title or a word in Elvish.
			b.WriteString(opaque(whole))
			rest = rest[j:]
			continue
		case name == "a" && !strings.Contains(inner, "<"):
			if !hasLower(text) || isWorkTitle(text) {
				b.WriteString(opaque(whole))
				rest = rest[j:]
				continue
			}
			if gloss != nil {
				if vi, ok := gloss.terms[text]; ok {
					b.WriteString(opaque(raw + html.EscapeString(vi) + "</a>"))
					rest = rest[j:]
					continue
				}
			}
		}
		if !htmlPair[name] && name != "i" {
			return m, fmt.Errorf("tag <%s> inside a run", name)
		}
		tag := id("h", &maskPart{payload: raw})
		stack = append(stack, open{name, tag})
		b.WriteString("<" + tag + ">")
	}
	if len(stack) > 0 {
		return m, fmt.Errorf("unclosed <%s>", stack[len(stack)-1].name)
	}
	body := maskInitials.ReplaceAllStringFunc(b.String(), opaque)
	body = gloss.replace(body, func(_, vi string) string { return opaque(html.EscapeString(vi)) })
	m.text = body
	m.markGlue()
	return m, nil
}

// maskHTMLText turns text between tags into plain text for the engine,
// with what HTML must escape kept aside.
func maskHTMLText(s string, opaque func(string) string) string {
	s = html.UnescapeString(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString(opaque("&amp;"))
		case '<':
			b.WriteString(opaque("&lt;"))
		case '>':
			b.WriteString(opaque("&gt;"))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
