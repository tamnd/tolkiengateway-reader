package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// glossary maps Tolkien terms to the Vietnamese the ChatGPT translations
// settled on, so Google's pages use the same words: Con Người for Men, not
// Đàn ông (males), and Tiên tộc for Elves, not Yêu tinh (goblins). Terms
// ChatGPT keeps in English, such as Valar and Shire, map to themselves,
// which keeps Google from translating them.
type glossary struct {
	terms map[string]string
	// byFirst lists the terms by their first word, longest first.
	byFirst map[string][]string
}

// buildGlossary reads every page ChatGPT translated and pairs the text of
// each link with the text of the translated link to the same page.
func buildGlossary(root, sourceLang, targetLang string, skip map[string]bool) (*glossary, error) {
	seen := map[string]map[string]int{} // English text -> translation -> uses
	base := filepath.Join(root, "content", targetLang)
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		if skip[filepath.ToSlash(rel)] {
			return nil
		}
		translated, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(root, "content", sourceLang, rel))
		if err != nil {
			return nil
		}
		en, vi := linkTexts(string(source)), linkTexts(string(translated))
		for target, texts := range en {
			if len(vi[target]) != len(texts) {
				continue
			}
			for i, text := range texts {
				if seen[text] == nil {
					seen[text] = map[string]int{}
				}
				seen[text][vi[target][i]]++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	terms := map[string]string{}
	for text, uses := range seen {
		if vi, ok := settled(text, uses); ok {
			terms[text] = vi
		}
	}
	return withFixes(newGlossary(terms)), nil
}

// linkTexts lists the texts of the links to each target, in order.
func linkTexts(md string) map[string][]string {
	out := map[string][]string{}
	for _, m := range maskLink.FindAllStringSubmatch(md, -1) {
		out[m[2]] = append(out[m[2]], m[1])
	}
	return out
}

// settled decides what a term is translated as, if the translations agree
// enough. Renderings differing only in case are one; the most common
// spelling wins. A rendering used for three in five uses is taken, English
// or not. Otherwise ChatGPT kept the term in English some of the time, and
// the most common Vietnamese is taken if half the other uses agree on it.
func settled(text string, uses map[string]int) (string, bool) {
	if len(text) < 3 || !startsUpper(text) || strings.ContainsAny(text, "[]*<>|\n") {
		return "", false
	}
	type variant struct {
		spelling string
		best     int
		count    int
	}
	merged := map[string]*variant{}
	total := 0
	for vi, n := range uses {
		key := strings.ToLower(vi)
		v := merged[key]
		if v == nil {
			v = &variant{}
			merged[key] = v
		}
		if n > v.best {
			v.spelling, v.best = vi, n
		}
		v.count += n
		total += n
	}
	var ranked []*variant
	for _, v := range merged {
		ranked = append(ranked, v)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].spelling < ranked[j].spelling
	})
	if total < 3 || strings.ContainsAny(ranked[0].spelling, "[]*<>|\n") {
		return "", false
	}
	if 5*ranked[0].count >= 3*total {
		return ranked[0].spelling, true
	}
	english := merged[strings.ToLower(text)]
	others := total
	if english != nil {
		others -= english.count
	}
	for _, v := range ranked {
		if v == english {
			continue
		}
		if v.count >= 3 && 2*v.count >= others {
			return v.spelling, true
		}
		break
	}
	return "", false
}

func startsUpper(s string) bool {
	for _, r := range s {
		return r >= 'A' && r <= 'Z' || r > 127 && strings.ToUpper(string(r)) == string(r) && strings.ToLower(string(r)) != string(r)
	}
	return false
}

// glossaryFixes win over what the corpus settled on: terms ChatGPT left
// half in English or rarely linked, which Google then renders its own way,
// Thời đại thứ hai for Second Age and Yêu tinh for elves.
var glossaryFixes = map[string]string{
	"Second Age":  "Kỷ Đệ Nhị",
	"Mount Doom":  "Núi Diệt Vong",
	"One Ring":    "Nhẫn Chúa",
	"Ruling Ring": "Nhẫn Chúa",
	"First Age":   "Kỷ Đệ Nhất",
	"Third Age":   "Kỷ Đệ Tam",
	"Fourth Age":  "Kỷ Đệ Tứ",
	"Dwarf":       "Người Lùn",
	"Dwarves":     "Người Lùn",
	"Ring-bearer": "Người Mang Nhẫn",
	"Dark Lord":   "Chúa tể Bóng tối",
	"elves":       "Tiên tộc",
	"elf":         "Tiên",
}

func withFixes(g *glossary) *glossary {
	terms := map[string]string{}
	for k, v := range g.terms {
		terms[k] = v
	}
	for k, v := range glossaryFixes {
		terms[k] = v
	}
	return newGlossary(terms)
}

func newGlossary(terms map[string]string) *glossary {
	g := &glossary{terms: terms, byFirst: map[string][]string{}}
	for k := range terms {
		if w := firstWord(k); w != "" {
			g.byFirst[w] = append(g.byFirst[w], k)
		}
	}
	// Longest first, so Mount Doom wins over Mount.
	for _, keys := range g.byFirst {
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) > len(keys[j])
			}
			return keys[i] < keys[j]
		})
	}
	return g
}

// firstWord is the run of letters and digits a term starts with.
func firstWord(s string) string {
	end := 0
	for end < len(s) && isWordByte(s, end) {
		_, n := utf8.DecodeRuneInString(s[end:])
		end += n
	}
	return s[:end]
}

// replace calls fn on every whole-word glossary term in s, the longest at
// each place.
func (g *glossary) replace(s string, fn func(term, vi string) string) string {
	var b strings.Builder
	last := 0
	g.each(s, func(term string, at int) {
		b.WriteString(s[last:at])
		b.WriteString(fn(term, g.terms[term]))
		last = at + len(term)
	})
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// each calls fn with every whole-word glossary term in s and where it
// starts, the longest at each place.
func (g *glossary) each(s string, fn func(term string, at int)) {
	if g == nil || len(g.byFirst) == 0 {
		return
	}
	for i := 0; i < len(s); {
		if !isWordByte(s, i) || i > 0 && isWordByte(s, i-1) {
			_, n := utf8.DecodeRuneInString(s[i:])
			i += n
			continue
		}
		word := firstWord(s[i:])
		match := ""
		for _, k := range g.byFirst[word] {
			if strings.HasPrefix(s[i:], k) && (i+len(k) == len(s) || !isWordByte(s, i+len(k))) {
				match = k
				break
			}
		}
		if match == "" {
			i += len(word)
			continue
		}
		fn(match, i)
		i += len(match)
	}
}

func (g *glossary) save(path string) error {
	keys := make([]string, 0, len(g.terms))
	for k := range g.terms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%s\n", k, g.terms[k])
	}
	return atomicWrite(path, []byte(b.String()))
}

// mtLogged lists the pages tgw mt translated whole, which must not teach the glossary.
func mtLogged(path string) (map[string]bool, error) {
	pages := map[string]bool{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return pages, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		var r struct{ Page, Engine string }
		// Pages Google only filled in are still ChatGPT's.
		if json.Unmarshal(s.Bytes(), &r) == nil && r.Page != "" && r.Engine != "google-fill" {
			pages[r.Page] = true
		}
	}
	return pages, s.Err()
}
