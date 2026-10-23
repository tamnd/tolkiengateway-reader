package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Machine translation engines translate text, not Markdown. Handed a page as
// is, they translate link targets, drop anchors and respace emphasis. So a
// paragraph is rewritten as text with small numbered tags, which Google
// carries through:
//
//	[Anduin](../A/Anduin.md)        ->  <a3>Anduin</a3>
//	**Ethir Anduin**                ->  <b4>Ethir Anduin</b4>
//	<sup id="cite_ref-1">…</sup>    ->  <x5/>
//	*The Hobbit*                    ->  <x6/>
//
// Only the visible text inside a pair is translated. Italic spans that are
// short or are one link, which on the wiki are titles of works and words in
// Tolkien's languages, are hidden whole, so they stay as written.

var (
	maskSup    = regexp.MustCompile(`(?s)<sup\b[^>]*>.*?</sup>`)
	maskOpaque = regexp.MustCompile("<[A-Za-z/!][^<>\\n]*>" +
		`|\{#[^}\n]*\}` +
		`|!\[[^\]\n]*\]\([^)\n]*\)` +
		"|`[^`\\n]+`" +
		`|https?://[^\s<>]+`)
	maskLink = regexp.MustCompile(`\[((?:[^\[\]\n]|\[[^\]\n]*\])+)\]\((<[^>\n]*>|[^()\s]*(?:\([^()\s]*\)[^()\s]*)*)(\s+"[^"\n]*")?\)`)
	// Initials and dotted abbreviations: J.R.R. Tolkien, C. S. Lewis, T.A.
	// Google reads their dots as sentence ends and splits them, writing
	// "J.R.R. Bản thân Tolkien" for "J.R.R. Tolkien himself".
	maskInitials = regexp.MustCompile(`\b(?:[A-Z]\.\s?){1,3}[A-Z]\p{Ll}+|\b(?:[A-Z]\.){2,}`)
	maskStrong   = regexp.MustCompile(`\*\*(\S|\S[^\n]*?\S)\*\*`)
	maskPrefix   = regexp.MustCompile(`^(\s*(?:#{1,6}\s+|[-*+]\s+|\d+\.\s+|>\s*)*)`)
	maskOwnTag   = regexp.MustCompile(`^</?[xabi]\d+/?>$`)
	maskTag      = regexp.MustCompile(`<\s*(/?)\s*([xabi]\d+)\s*(/?)\s*>`)
	maskTarget   = regexp.MustCompile(`\]\([^)]*\)`)
)

// maxTitleWords is the longest italic span kept as written. Longer ones are
// quotations or emphasis, and are translated.
const maxTitleWords = 6

type maskPart struct {
	payload string // opaque text, or a link's target
	extra   string // a link's title
	// Whether the source had no space right before or after an opaque tag,
	// as with a citation glued to the word it follows.
	gluedBefore, gluedAfter bool
	// Whether the source had a space before an opaque tag or an opening one.
	// Engines drop it between two tags, joining [T.A.](…) [3018](…).
	spaceBefore bool
	// Whether the source had no word right after an opaque tag or a closing
	// one, or right before an opening one. Vietnamese moves words around
	// the tags, and engines leave them glued: "Tolkien có" came back
	// "Tolkiencó", "các [Dân tộc Tự do]() nào" came back "Tự do]()nào".
	spaceAfter, apartBefore bool
}

type masked struct {
	text  string
	parts map[string]*maskPart
}

// maskMarkdown masks a paragraph. Glossary terms are hidden with their
// translation, so the engine sees them already settled.
func maskMarkdown(md string, gloss *glossary) masked {
	m := masked{parts: map[string]*maskPart{}}
	n := 0
	id := func(kind string, p *maskPart) string {
		n++
		tag := kind + strconv.Itoa(n)
		m.parts[tag] = p
		return tag
	}
	opaque := func(s string) string {
		return "<" + id("x", &maskPart{payload: s}) + "/>"
	}
	link := func(s string) string {
		g := maskLink.FindStringSubmatch(s)
		if !hasLower(g[1]) {
			// Nothing to translate in [3018](…) or [T.A.](…).
			return opaque(s)
		}
		if gloss != nil {
			if vi, ok := gloss.terms[g[1]]; ok {
				return opaque("[" + vi + "](" + g[2] + g[3] + ")")
			}
		}
		tag := id("a", &maskPart{payload: g[2], extra: g[3]})
		return "<" + tag + ">" + g[1] + "</" + tag + ">"
	}
	pair := func(kind string) func(string) string {
		return func(s string) string {
			n := 2
			if kind == "i" {
				n = 1
			}
			inner := s[n : len(s)-n]
			tag := id(kind, &maskPart{})
			return "<" + tag + ">" + inner + "</" + tag + ">"
		}
	}

	lines := strings.Split(md, "\n")
	for i, line := range lines {
		prefix := maskPrefix.FindString(line)
		body := line[len(prefix):]
		if prefix != "" {
			prefix = opaque(prefix)
		}
		body = maskSup.ReplaceAllStringFunc(body, opaque)
		body = replaceItalics(body, func(span string) string {
			if italicWords(span) <= maxTitleWords || isWholeLink(span[1:len(span)-1]) {
				return opaque(span)
			}
			return span
		})
		body = maskLink.ReplaceAllStringFunc(body, link)
		body = maskOpaque.ReplaceAllStringFunc(body, func(s string) string {
			if maskOwnTag.MatchString(s) {
				return s
			}
			return opaque(s)
		})
		body = maskInitials.ReplaceAllStringFunc(body, opaque)
		body = gloss.replace(body, func(_, vi string) string { return opaque(vi) })
		if strings.HasPrefix(strings.TrimSpace(body), "|") {
			body = maskPipes(body, opaque)
		}
		body = maskStrong.ReplaceAllStringFunc(body, pair("b"))
		body = replaceItalics(body, pair("i"))
		lines[i] = prefix + body
	}
	m.text = strings.Join(lines, "\n")
	m.markGlue()
	return m
}

// maskPipes hides the cell borders of a Markdown table row.
func maskPipes(row string, opaque func(string) string) string {
	var b strings.Builder
	for _, cell := range strings.SplitAfter(row, "|") {
		if strings.HasSuffix(cell, "|") {
			b.WriteString(cell[:len(cell)-1])
			b.WriteString(opaque("|"))
			continue
		}
		b.WriteString(cell)
	}
	return b.String()
}

// replaceItalics calls fn on every *span* that Markdown reads as emphasis:
// single stars, not part of ** or of a word, hugging non-space text.
func replaceItalics(s string, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '*' || !italicOpens(s, i) {
			continue
		}
		end := strings.IndexAny(s[i+1:], "*\n")
		if end < 0 {
			break
		}
		j := i + 1 + end
		if s[j] != '*' || !italicCloses(s, j) || j == i+1 {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(fn(s[i : j+1]))
		last = j + 1
		i = j
	}
	b.WriteString(s[last:])
	return b.String()
}

func italicOpens(s string, i int) bool {
	if i > 0 && (s[i-1] == '*' || isWordByte(s, i-1)) {
		return false
	}
	return i+1 < len(s) && s[i+1] != '*' && s[i+1] != ' ' && s[i+1] != '\n'
}

func italicCloses(s string, j int) bool {
	if s[j-1] == ' ' || s[j-1] == '*' {
		return false
	}
	return j+1 >= len(s) || (s[j+1] != '*' && !isWordByte(s, j+1))
}

// isWordByte reports whether the rune at or ending at byte i is a letter or
// digit.
func isWordByte(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	if r == utf8.RuneError {
		r, _ = utf8.DecodeLastRuneInString(s[:i+1])
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isWholeLink reports whether s is one link and nothing else, as an italic
// title linking to its page is, however long.
func isWholeLink(s string) bool {
	loc := maskLink.FindStringIndex(s)
	return loc != nil && loc[0] == 0 && loc[1] == len(s)
}

func italicWords(span string) int {
	return len(strings.Fields(maskTarget.ReplaceAllString(span, "]")))
}

func hasLower(s string) bool { return strings.IndexFunc(s, unicode.IsLower) >= 0 }

// markGlue records which tags sit right against the text around them.
func (m masked) markGlue() {
	for _, loc := range maskTag.FindAllStringSubmatchIndex(m.text, -1) {
		tag := m.text[loc[4]:loc[5]]
		p := m.parts[tag]
		if p == nil {
			continue
		}
		if loc[3] > loc[2] {
			p.spaceAfter = !startsWord(m.text[loc[1]:])
			continue
		}
		p.apartBefore = !endsWord(m.text[:loc[0]])
		if tag[0] == 'x' {
			// Tolkien's comes back as "của Tolkien có", the 's gone.
			p.spaceAfter = !startsWord(m.text[loc[1]:]) && endsWord(visible(p.payload))
		}
		p.spaceBefore = loc[0] > 0 && m.text[loc[0]-1] == ' '
		if tag[0] != 'x' {
			continue
		}
		p.gluedBefore = loc[0] > 0 && !isSpace(m.text[loc[0]-1])
		p.gluedAfter = loc[1] < len(m.text) && !isSpace(m.text[loc[1]])
	}
}

// startsWord reports whether s begins with a letter or digit.
func startsWord(s string) bool {
	for _, r := range s {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	return false
}

func endsWord(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

var (
	trailingTarget = regexp.MustCompile(`\]\([^)]*\)$|</[a-z]+>$|[*_]$`)
)

// visible strips the Markdown a span ends with, so a hidden link such as
// [Thranduil](../T/Thranduil.md) ends with what a reader sees: Thranduil.
func visible(s string) string {
	for {
		loc := trailingTarget.FindStringIndex(s)
		if loc == nil {
			return s
		}
		s = s[:loc[0]]
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

// unmask rebuilds the Markdown from an engine's answer, and reports the tags
// it lost, repeated or made up.
func (m masked) unmask(text string) (string, error) {
	// Engines put spaces just inside a pair that the source did not have,
	// and in Markdown "** text **" is no longer emphasis.
	text = regexp.MustCompile(`(<[abi]\d+>)[ \t]+`).ReplaceAllString(text, "$1")
	text = regexp.MustCompile(`[ \t]+(</[abi]\d+>)`).ReplaceAllString(text, "$1")

	var out []byte
	var problems []string
	seen := map[string]bool{}
	last := 0
	skipSpace, needSpace := false, false
	// Only the engine's own spaces are trimmed, never the end of what the
	// tags put back, such as the space in a list marker.
	restored := 0
	for _, loc := range maskTag.FindAllStringSubmatchIndex(text, -1) {
		between := text[last:loc[0]]
		if skipSpace {
			between = strings.TrimLeft(between, " \t")
		}
		if needSpace && startsWord(between) {
			out = append(out, ' ')
		}
		out = append(out, between...)
		last, skipSpace, needSpace = loc[1], false, false
		closing, tag := text[loc[2]:loc[3]] == "/", text[loc[4]:loc[5]]
		p := m.parts[tag]
		if p == nil {
			problems = append(problems, "invented "+tag)
			continue
		}
		needSpace = p.spaceAfter && (tag[0] == 'x' || closing && endsWord(string(out)))
		if !closing {
			if seen[tag] {
				problems = append(problems, "repeated "+tag)
			}
			seen[tag] = true
			if p.spaceBefore && len(out) > 0 && !isSpace(out[len(out)-1]) ||
				p.apartBefore && !p.gluedBefore && endsWord(string(out)) {
				out = append(out, ' ')
			}
		}
		switch tag[0] {
		case 'x':
			if p.gluedBefore {
				out = append(out[:restored], strings.TrimRight(string(out[restored:]), " \t")...)
			}
			out = append(out, p.payload...)
			skipSpace = p.gluedAfter
		case 'a':
			if closing {
				out = append(out, "]("+p.payload+p.extra+")"...)
			} else {
				out = append(out, '[')
			}
		case 'b':
			out = append(out, "**"...)
		case 'i':
			out = append(out, '*')
		}
		restored = len(out)
	}
	tail := text[last:]
	if skipSpace {
		tail = strings.TrimLeft(tail, " \t")
	}
	if needSpace && startsWord(tail) {
		out = append(out, ' ')
	}
	out = append(out, tail...)
	for tag := range m.parts {
		if !seen[tag] {
			problems = append(problems, "lost "+tag)
		}
	}
	if len(problems) > 0 {
		return string(out), fmt.Errorf("tags changed: %s", strings.Join(problems, ", "))
	}
	return string(out), nil
}

var plainNumber = regexp.MustCompile(`\d+(?:[,.]\d{3})*`)

// numbers lists the years and counts in the visible text of a masked
// paragraph, with thousands separators dropped, since Vietnamese groups
// them with dots and English with commas.
func numbers(maskedText string) map[string]int {
	text := maskTag.ReplaceAllString(maskedText, " ")
	found := map[string]int{}
	for _, loc := range plainNumber.FindAllStringIndex(text, -1) {
		if loc[0] > 0 && (isWordByte(text, loc[0]-1) || strings.ContainsRune(".:&", rune(text[loc[0]-1]))) {
			continue
		}
		if loc[1] < len(text) && (isWordByte(text, loc[1]) || strings.ContainsRune(":;", rune(text[loc[1]]))) {
			continue
		}
		n := strings.NewReplacer(",", "", ".", "").Replace(text[loc[0]:loc[1]])
		found[n]++
	}
	return found
}

// checkNumbers catches an engine that changed a year or a count, as Google
// does when it reads 1630 as the time 16:30.
func checkNumbers(source, text string) error {
	have := numbers(text)
	for n, want := range numbers(source) {
		if have[n] < want {
			return fmt.Errorf("number %s changed", n)
		}
	}
	return nil
}
