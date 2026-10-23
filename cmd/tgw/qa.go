package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// runQA checks every translated page against its source, paragraph by
// paragraph, for what a machine can tell without reading: markup that came
// apart, words glued together, text left in English, numbers that changed,
// and renderings known to be wrong. It is the cheap first layer of the
// quality harness; tgw review sends what it flags to a model to judge.
func runQA(args []string) error {
	fs := flag.NewFlagSet("qa", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	sourceLang := fs.String("source-lang", "en", "source language directory")
	targetLang := fs.String("target-lang", "vi", "target language directory")
	out := fs.String("out", "", "where to write one JSON line per problem; defaults to .tgw/qa.jsonl")
	examples := fs.Int("examples", 3, "examples to print per check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(*root, ".tgw", "qa.jsonl")
	}
	google, err := mtLogged(filepath.Join(*root, ".tgw", "mt.jsonl"))
	if err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	type tally struct{ pages, paragraphs int }
	totals := map[string]*tally{}
	flagged := map[string]map[string]*tally{} // engine -> check -> counts
	shown := map[string]int{}
	base := filepath.Join(*root, "content", *targetLang)
	err = filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
		vi, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		en, err := os.ReadFile(filepath.Join(*root, "content", *sourceLang, rel))
		if err != nil {
			return nil
		}
		engine := "gpt"
		if google[rel] {
			engine = "google"
		}
		if totals[engine] == nil {
			totals[engine] = &tally{}
			flagged[engine] = map[string]*tally{}
		}
		totals[engine].pages++
		problems := qaPage(string(en), string(vi))
		totals[engine].paragraphs += problems.paragraphs
		pageChecks := map[string]bool{}
		for _, p := range problems.found {
			p.Page, p.Engine = rel, engine
			if err := enc.Encode(p); err != nil {
				return err
			}
			t := flagged[engine][p.Check]
			if t == nil {
				t = &tally{}
				flagged[engine][p.Check] = t
			}
			t.paragraphs++
			if !pageChecks[p.Check] {
				pageChecks[p.Check] = true
				t.pages++
			}
			if shown[p.Check] < *examples {
				shown[p.Check]++
				fmt.Printf("%s %s #%d: %s\n", p.Check, rel, p.Para, p.Detail)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	engines := make([]string, 0, len(totals))
	for e := range totals {
		engines = append(engines, e)
	}
	sort.Strings(engines)
	for _, e := range engines {
		t := totals[e]
		fmt.Printf("\n%s: %d pages, %d paragraphs\n", e, t.pages, t.paragraphs)
		checks := make([]string, 0, len(flagged[e]))
		for c := range flagged[e] {
			checks = append(checks, c)
		}
		sort.Strings(checks)
		for _, c := range checks {
			x := flagged[e][c]
			fmt.Printf("  %-12s %6d paragraphs (%.2f%%) on %5d pages\n", c, x.paragraphs, 100*float64(x.paragraphs)/float64(max(t.paragraphs, 1)), x.pages)
		}
	}
	return nil
}

type qaProblem struct {
	Page   string `json:"page"`
	Engine string `json:"engine"`
	Para   int    `json:"para"`
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

type qaResult struct {
	paragraphs int
	found      []qaProblem
}

// pageParagraphs lists the prose paragraphs of a page, in order, the way
// tgw translate and tgw mt cut it: raw HTML blocks are left out.
func pageParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	head := frontMatter(text)
	var out []string
	for _, chunk := range splitTranslation(text[len(head):]) {
		if chunk.verbatim {
			continue
		}
		for _, block := range splitBlocks(chunk.text) {
			if body := strings.TrimSpace(block); body != "" {
				out = append(out, body)
			}
		}
	}
	return out
}

var (
	qaLeftTag = regexp.MustCompile(`<\s*/?\s*[xabi]\d+\s*/?\s*>`)
	// A link glued to the word after it, or to the word before it.
	qaLinkThenWord = regexp.MustCompile(`\]\([^)\s]*\)\p{L}`)
	qaWordThenLink = regexp.MustCompile(`\p{L}\[[^\]]+\]\(`)
	// An English name run into a Vietnamese word: Tolkiencó, Mordorvà.
	// Vietnamese syllables have at most three letters before their vowel
	// mark, so five plain letters then a mark is a join. Only marks Elvish
	// names never carry count, or Lothlórien would be one.
	qaGluedName  = regexp.MustCompile(`\b\p{Lu}[a-z]{4,}[a-zđ]*[àạảãầấậẩẫăằắặẳẵèẹẻẽềếệểễìịỉĩòọỏõồốộổỗơờớợởỡùụủũưừứựửữỳýỵỷỹđ]\p{L}*`)
	qaSpacePunct = regexp.MustCompile(`\p{L} [,.;:!?](\s|$)`)
	qaDoubleSp   = regexp.MustCompile(`\S  +\S`)
	// Renderings the review found wrong for Tolkien terms.
	qaBanned = []string{"Yêu tinh", "yêu tinh", "Đàn ông", "Người đàn ông", "Núi Doom", "Cổng đen", "Thời đại thứ", "Kỷ nguyên thứ", "Tuổi thứ"}
)

func qaPage(en, vi string) qaResult {
	var r qaResult
	ep, vp := pageParagraphs(en), pageParagraphs(vi)
	r.paragraphs = len(vp)
	if len(ep) != len(vp) {
		r.found = append(r.found, qaProblem{Para: -1, Check: "blocks", Detail: fmt.Sprintf("%d paragraphs for %d", len(vp), len(ep))})
		return r
	}
	add := func(i int, check, detail string) {
		r.found = append(r.found, qaProblem{Para: i, Check: check, Detail: detail})
	}
	for i := range vp {
		s, t := ep[i], vp[i]
		if m := qaLeftTag.FindString(t); m != "" {
			add(i, "tag", m)
		}
		if s == t {
			if words := len(strings.Fields(maskLink.ReplaceAllString(s, " "))); words >= 6 && hasLetters(s) {
				add(i, "english", clip(s))
			}
			continue
		}
		if err := validateChunk(s, t); err != nil {
			add(i, "markup", err.Error())
		}
		if err := checkNumbers(s, t); err != nil {
			add(i, "numbers", err.Error())
		}
		for _, c := range []struct {
			name string
			re   *regexp.Regexp
		}{{"glue", qaLinkThenWord}, {"glue", qaWordThenLink}, {"punct", qaSpacePunct}, {"spaces", qaDoubleSp}} {
			if got := c.re.FindAllString(t, -1); len(got) > len(c.re.FindAllString(s, -1)) {
				add(i, c.name, strings.Join(got, " | "))
			}
		}
		if got := qaGluedName.FindAllString(t, -1); len(got) > 0 {
			add(i, "glue", strings.Join(got, " | "))
		}
		if strings.Count(t, "**")%2 != strings.Count(s, "**")%2 {
			add(i, "emphasis", clip(t))
		}
		for _, bad := range qaBanned {
			if strings.Contains(t, bad) {
				add(i, "term", bad)
			}
		}
		if englishShare(t) > 0.5 && len(strings.Fields(t)) >= 8 {
			add(i, "english", clip(t))
		}
	}
	return r
}

// englishShare is the share of a paragraph's plain words, outside links and
// markup, that are common English words.
func englishShare(t string) float64 {
	t = chunkLink.ReplaceAllString(t, " ")
	words, english := 0, 0
	for _, w := range strings.FieldsFunc(t, func(r rune) bool { return !unicode.IsLetter(r) }) {
		words++
		if commonEnglish[strings.ToLower(w)] {
			english++
		}
	}
	if words == 0 {
		return 0
	}
	return float64(english) / float64(words)
}

var commonEnglish = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("the of and to in was is that he it his for as with by on at from which were had be this but not they are or an their have him who her she been when after its into all also there one would so these than then them what other more could only some about") {
		m[w] = true
	}
	return m
}()

func clip(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
