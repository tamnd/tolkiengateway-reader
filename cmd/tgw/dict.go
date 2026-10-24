package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/tamnd/llm"
	"github.com/tamnd/llm/ledger"
	"github.com/tamnd/llm/route"
)

// The dictionary settles, once for the whole wiki, how each recurring term
// is said in Vietnamese. The glossary learns only from link texts; most
// mentions of Steward, Ranger, Ringwraith or King of Gondor are not links,
// and each engine and each page would otherwise pick its own words for
// them. A term made only of names (Gondor, Bag End's Bag aside) is kept as
// it is without asking; the model decides the rest, seeing one sentence the
// term is used in. The answers go to .tgw/dict.tsv, which every glossary
// load reads over what the corpus settled on, so Google's masking, the
// ChatGPT translations and the review all use the same words.

// dictNotTerm marks a candidate the model said is no term.
const dictNotTerm = "-"

// loadDict reads the dictionary: English, Vietnamese and a note,
// tab-separated. A missing file is an empty dictionary.
func loadDict(path string) (map[string]string, error) {
	d := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 2 && f[0] != "" && f[1] != "" {
			d[f[0]] = f[1]
		}
	}
	return d, nil
}

// dictTerm is a run of capitalised words, joined by the small words names
// use: King of Gondor, Battle of the Pelennor Fields, Bag End.
var dictTerm = regexp.MustCompile(`\p{Lu}[\p{L}\p{M}'’-]*(?:(?: of the| of| the| and| in| on| upon| de)? \p{Lu}[\p{L}\p{M}'’-]*)*`)

type dictCandidate struct {
	term    string
	uses    int
	pages   int
	example string
}

// collectDict counts the capitalised terms the English prose uses inside a
// sentence, where a capital means a name or a title, not a sentence start.
func collectDict(base string) (map[string]*dictCandidate, map[string]bool, error) {
	found := map[string]*dictCandidate{}
	lower, upper := map[string]int{}, map[string]int{}
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		seen := map[string]bool{}
		for _, chunk := range splitTranslation(text[len(frontMatter(text)):]) {
			if chunk.verbatim {
				continue
			}
			prose := proseText(chunk.text)
			for _, w := range htmlWord.FindAllString(prose, -1) {
				if r := []rune(w); unicode.IsLower(r[0]) {
					lower[w]++
				} else {
					upper[strings.ToLower(w)]++
				}
			}
			for _, sentence := range splitSentences(prose) {
				for _, loc := range dictTerm.FindAllStringIndex(sentence, -1) {
					if !midSentence(sentence, loc[0]) {
						continue
					}
					t := strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(sentence[loc[0]:loc[1]], "'s"), "’s"), "'’-")
					if len(t) < 3 || len(t) > 60 || initial.MatchString(t) {
						continue
					}
					c := found[t]
					if c == nil {
						c = &dictCandidate{term: t}
						found[t] = c
					}
					c.uses++
					if !seen[t] {
						seen[t] = true
						c.pages++
					}
					if n := len(sentence); n >= 40 && n <= 220 && (c.example == "" || n < len(c.example)) {
						c.example = sentence
					}
				}
			}
		}
		return nil
	})
	// A word is English when the corpus writes it in lower case a good part
	// of the time; Sauron in a file name now and then is still a name.
	english := map[string]bool{}
	for w, n := range lower {
		if n >= 5 && n*20 >= upper[w] {
			english[w] = true
		}
	}
	return found, english, err
}

var (
	proseDrop     = regexp.MustCompile("<[^<>\n]*>|`[^`\n]*`|\\{#[^}\n]*\\}|\\[\\^[^\\]]*\\]|https?://\\S+")
	sentenceSplit = regexp.MustCompile(`[.!?;:]\s+|\n+`)
)

// proseText is what a reader sees of Markdown prose: link texts without
// their targets, no tags, no emphasis.
func proseText(md string) string {
	md = maskLink.ReplaceAllString(md, "$1")
	md = proseDrop.ReplaceAllString(md, " ")
	md = strings.NewReplacer("**", "", "*", "", "_", " ", "#", " ", "|", " ").Replace(md)
	return md
}

// splitSentences splits prose at the ends of sentences and clauses, not at
// initials: J. R. R. Tolkien, Donald A. Wollheim.
func splitSentences(s string) []string {
	var out []string
	join := false
	for _, loc := range splitAt(s) {
		p := strings.Join(strings.Fields(s[loc[0]:loc[1]]), " ")
		if join && len(out) > 0 {
			out[len(out)-1] += ". " + p
		} else if p != "" {
			out = append(out, p)
		}
		join = initial.MatchString(p) && loc[1] < len(s) && s[loc[1]] == '.'
	}
	return out
}

var initial = regexp.MustCompile(`(?:^|[^\p{L}])\p{Lu}$`)

// splitAt gives the pieces of s between sentence breaks.
func splitAt(s string) [][2]int {
	var out [][2]int
	last := 0
	for _, loc := range sentenceSplit.FindAllStringIndex(s, -1) {
		out = append(out, [2]int{last, loc[0]})
		last = loc[1]
	}
	return append(out, [2]int{last, len(s)})
}

// midSentence reports whether the word at i follows a word written in
// lower case, or a comma, so its capital is its own.
func midSentence(s string, i int) bool {
	before := strings.TrimRight(s[:i], " ")
	if len(before) == i || before == "" {
		return false
	}
	r := []rune(before)
	last := r[len(r)-1]
	return unicode.IsLower(last) || last == ','
}

// onlyNames reports whether no word of the term is an English word, so it
// stays as it is.
func onlyNames(term string) bool {
	return namesOnly(term, func(w string) bool { return englishWords[w] })
}

// namesOnly is onlyNames with the test of what is English given.
func namesOnly(term string, english func(string) bool) bool {
	for _, w := range htmlWord.FindAllString(term, -1) {
		lw := strings.ToLower(strings.Trim(w, "'’-"))
		if htmlStop[lw] || english(lw) {
			return false
		}
		for _, part := range strings.Split(lw, "-") {
			if english(part) {
				return false
			}
		}
	}
	return true
}

// runDict collects the corpus's recurring terms and has the model settle
// the Vietnamese for the ones that hold English words.
func runDict(args []string) error {
	fs := flag.NewFlagSet("dict", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	sourceLang := fs.String("source-lang", "en", "source language directory")
	minUses := fs.Int("min", 8, "take terms used at least this often")
	minPages := fs.Int("pages", 3, "on at least this many pages")
	list := fs.Bool("list", false, "only list the terms the model would be asked about")
	routeFile := fs.String("routes", "", "llm route registry")
	batch := fs.Int("batch", 60, "terms per request")
	audit := fs.Int("audit", 0, "list the terms and pages, this many of each, whose Vietnamese misses the dictionary's")
	targetLang := fs.String("target-lang", "vi", "target language directory")
	titles := fs.Bool("titles", false, "take out the terms the English mostly uses inside titles of works, which stay in English")
	if err := fs.Parse(args); err != nil {
		return err
	}
	out := filepath.Join(*root, ".tgw", "dict.tsv")
	done, err := loadDict(out)
	if err != nil {
		return err
	}
	if *audit > 0 {
		return dictAudit(*root, *sourceLang, *targetLang, done, *audit)
	}
	if *titles {
		return dictTitles(*root, *sourceLang, out, done)
	}
	gloss, err := loadGlossary(filepath.Join(*root, ".tgw", "glossary.tsv"))
	if err != nil {
		return err
	}
	phrases, err := loadPhrases(filepath.Join(*root, ".tgw", "phrases.tsv"))
	if err != nil {
		return err
	}
	found, english, err := collectDict(filepath.Join(*root, "content", *sourceLang))
	if err != nil {
		return err
	}
	var names, ask []*dictCandidate
	for t, c := range found {
		if c.uses < *minUses || c.pages < *minPages || done[t] != "" || phrases[t] != "" {
			continue
		}
		if _, ok := gloss.terms[t]; ok {
			continue
		}
		if namesOnly(t, func(w string) bool { return english[w] }) {
			names = append(names, c)
		} else {
			ask = append(ask, c)
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i].term < names[j].term })
	sort.Slice(ask, func(i, j int) bool { return ask[i].uses > ask[j].uses })
	fmt.Printf("dict: %d terms known, %d new names kept as they are, %d to ask about\n", len(done), len(names), len(ask))
	if *list {
		for _, c := range ask {
			fmt.Printf("%d\t%d\t%s\t%s\n", c.uses, c.pages, c.term, c.example)
		}
		return nil
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, c := range names {
		fmt.Fprintf(w, "%s\t%s\tname\n", c.term, c.term)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(ask) == 0 {
		return nil
	}
	llm.Configure(llm.Config{App: "tolkiengateway"})
	registry, _, err := route.LoadOrDefault(*routeFile)
	if err != nil {
		return err
	}
	pool := route.NewJobPool(registry, "translate")
	if pool.Empty() {
		return fmt.Errorf("no enabled route accepts translate jobs")
	}
	log, err := ledger.Open(filepath.Join(*root, ".tgw", "ledger.jsonl"))
	if err != nil {
		return err
	}
	defer log.Close()
	ctx := context.Background()
	for i := 0; i < len(ask); i += *batch {
		part := ask[i:min(i+*batch, len(ask))]
		type item struct {
			Term    string `json:"term"`
			Example string `json:"example"`
		}
		items := make([]item, len(part))
		terms := make([]string, len(part))
		for k, c := range part {
			items[k] = item{c.term, c.example}
			terms[k] = c.term
		}
		input, _ := json.Marshal(items)
		instruction := fmt.Sprintf(dictPrompt, styleGuide, glossaryFor(gloss, terms...))
		answer, err := askPhrases(ctx, pool, log, instruction, string(input))
		if err != nil {
			// The next run asks again.
			fmt.Printf("dict: %d/%d skipped: %v\n", min(i+*batch, len(ask)), len(ask), err)
			continue
		}
		n := 0
		for _, c := range part {
			vi, ok := answer[c.term]
			if !ok {
				continue
			}
			vi = strings.TrimSpace(vi)
			note := "model"
			switch {
			case vi == "":
				vi, note = dictNotTerm, "not a term"
			case strings.ContainsAny(vi, "\t\n") || checkNumbers(c.term, vi) != nil:
				continue
			default:
				vi = applyRules(vi)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", c.term, vi, note)
			n++
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("dict: %d/%d, %d kept\n", min(i+*batch, len(ask)), len(ask), n)
	}
	return nil
}

const dictPrompt = `You are building the English-Vietnamese dictionary of Tolkien Gateway, an encyclopedia of J.R.R. Tolkien's works, so that every page says each term the same way. Each input item is a term and one sentence it is used in.

%s
For each term give the one Vietnamese form every page will use:
- names of people, places, peoples, languages and things that are names stay as they are (Bag End, Took, Rivendell, Westron, Sting);
- titles, ranks, epithets, kinds of beings, events and descriptive names are translated the way a careful Vietnamese Tolkien encyclopedia would (Steward of Gondor = Quốc quản Gondor, King of Gondor = Vua Gondor, Ringwraiths = Nhẫn Ma, War of the Ring = Cuộc chiến Nhẫn), keeping the names inside them;
- titles of books, chapters, poems, films, games, songs and articles stay in English;
- a person's name that holds an English word (Tom Bombadil, Treebeard aside) stays as written unless Tolkien's Vietnamese readers know it translated.
Give "" for an item that is not a term at all: an ordinary phrase, a fragment, a common word that only happens to be capitalised.

Input is a JSON array of {"term", "example"}. Return only a JSON object mapping each term to its Vietnamese, with no code fences.

Glossary already settled:
%s`

// dictTerms is the glossary of the terms the dictionary translates.
func dictTerms(dict map[string]string) *glossary {
	terms := map[string]string{}
	for en, vi := range dict {
		if vi != en && vi != dictNotTerm {
			terms[en] = vi
		}
	}
	return newGlossary(terms)
}

// dictMisses lists the dictionary terms the English uses whose Vietnamese
// the translation lacks.
func dictMisses(dict *glossary, en, vi string) []string {
	var miss []string
	low := strings.ToLower(vi)
	seen := map[string]bool{}
	dict.replace(proseText(en), func(term, want string) string {
		if !seen[term] && !strings.Contains(low, strings.ToLower(want)) {
			miss = append(miss, term)
		}
		seen[term] = true
		return term
	})
	return miss
}

// titleText is text in italics or quotes, where the wiki puts titles of
// works and chapters.
var titleText = regexp.MustCompile(`\*[^*\n]+\*|"[^"\n]{1,100}"|“[^”\n]{1,100}”`)

// titleCase reports whether a quoted text reads as a title: a few words,
// each capitalised but the small ones.
func titleCase(q string) bool {
	words := htmlWord.FindAllString(q, -1)
	if len(words) == 0 || len(words) > 10 {
		return false
	}
	for _, w := range words {
		if r := []rune(w); unicode.IsLower(r[0]) && !htmlStop[w] {
			return false
		}
	}
	return true
}

// midThe matches a The that opens a title inside a sentence: "in The Two
// Towers", "[The Tale of Tinúviel](...)".
var midThe = regexp.MustCompile(`(?:\p{Ll}[,:]? |[\[*"“(])The $`)

// dictTitles takes out of the dictionary the terms the English uses mostly
// inside titles of works, The Return of the King or Realms in Exile, where
// they stay in English. Titles are in italics or quotes, or follow a The
// inside a sentence; a The at a sentence start says nothing, and the Great
// River is still Đại Hà.
func dictTitles(root, sourceLang, out string, dict map[string]string) error {
	g := dictTerms(dict)
	uses, inTitle := map[string]int{}, map[string]int{}
	err := filepath.WalkDir(filepath.Join(root, "content", sourceLang), func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// What a reader sees, italics kept: no tags, no link targets.
		text := strings.NewReplacer("<i>", "*", "</i>", "*").Replace(string(data))
		text = proseDrop.ReplaceAllString(maskLink.ReplaceAllString(text, "$1"), " ")
		var spans [][]int
		for _, sp := range titleText.FindAllStringIndex(text, -1) {
			if text[sp[0]] == '*' || titleCase(text[sp[0]:sp[1]]) {
				spans = append(spans, sp)
			}
		}
		g.each(text, func(term string, at int) {
			uses[term]++
			title := midThe.MatchString(text[max(0, at-12):at])
			for _, sp := range spans {
				if sp[0] <= at && at < sp[1] {
					title = true
					break
				}
			}
			if title {
				inTitle[term]++
			}
		})
		return nil
	})
	if err != nil {
		return err
	}
	var drop []string
	for term, n := range uses {
		// Half its uses in titles make it a title's words.
		if inTitle[term]*2 >= n {
			drop = append(drop, term)
		}
	}
	sort.Slice(drop, func(i, j int) bool { return uses[drop[i]] > uses[drop[j]] })
	f, err := os.OpenFile(out, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, term := range drop {
		fmt.Printf("%6d %6d  %s = %s\n", uses[term], inTitle[term], term, dict[term])
		fmt.Fprintf(w, "%s\t%s\twork title, stays in English\n", term, dictNotTerm)
	}
	fmt.Printf("dict: %d terms are words of titles, taken out\n", len(drop))
	return w.Flush()
}

// dictAudit lists the pages whose Vietnamese lacks a dictionary term's
// Vietnamese where the English uses the term, most misses first.
func dictAudit(root, sourceLang, targetLang string, dict map[string]string, top int) error {
	g := dictTerms(dict)
	type miss struct {
		page string
		n    int
		eg   []string
	}
	var misses []miss
	counts := map[string]int{}
	base := filepath.Join(root, "content", targetLang)
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		vi, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		en, err := os.ReadFile(filepath.Join(root, "content", sourceLang, rel))
		if err != nil {
			return nil
		}
		m := miss{page: filepath.ToSlash(rel)}
		for _, term := range dictMisses(g, string(en), string(vi)) {
			m.n++
			counts[term]++
			if len(m.eg) < 3 {
				m.eg = append(m.eg, term)
			}
		}
		if m.n > 0 {
			misses = append(misses, m)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(misses, func(i, j int) bool { return misses[i].n > misses[j].n })
	fmt.Printf("dict: %d pages miss a dictionary term\n", len(misses))
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	for _, k := range keys[:min(top, len(keys))] {
		fmt.Printf("%8d  %s = %s\n", counts[k], k, g.terms[k])
	}
	for _, m := range misses[:min(top, len(misses))] {
		fmt.Printf("%4d  %s  %s\n", m.n, m.page, strings.Join(m.eg, ", "))
	}
	return nil
}
