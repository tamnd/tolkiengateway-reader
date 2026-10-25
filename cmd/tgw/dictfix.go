package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tamnd/llm"
	"github.com/tamnd/llm/ledger"
	"github.com/tamnd/llm/route"
)

// The dictionary says how each term is said; Google, translating a page
// before the dictionary existed, said it its own way, and says it the same
// way every time: Kiểm lâm for Rangers, Người quản lý Gondor for Steward of
// Gondor. Those renderings are found once, from the paragraphs that miss a
// term, confirmed by the model, and then put right everywhere without it.

// maxVariantWords is the longest rendering looked for, in syllables.
const maxVariantWords = 6

var viWord = regexp.MustCompile(`[\p{L}\p{M}\p{N}'’-]+`)

// viGrams lists the distinct runs of up to maxVariantWords words of a
// paragraph's visible text, lowercase.
func viGrams(md string) map[string]bool {
	words := viWord.FindAllString(strings.ToLower(proseText(md)), -1)
	out := map[string]bool{}
	for i := range words {
		for n := 1; n <= maxVariantWords && i+n <= len(words); n++ {
			out[strings.Join(words[i:i+n], " ")] = true
		}
	}
	return out
}

type dictSample struct{ en, vi string }

// variantScan is what the corpus says about the terms the translations
// miss: for each, the runs of words its missing paragraphs share and a few
// of those paragraphs, and how common each run is anywhere.
type variantScan struct {
	Missed   map[string]int            `json:"missed"`
	Samples  map[string]int            `json:"samples"`
	Grams    map[string]map[string]int `json:"grams"`
	Examples map[string][]string       `json:"examples"`
	DF       map[string]int            `json:"df"`
	Total    int                       `json:"total"`
}

// scanVariants reads the corpus for dictVariants; it takes a while, so the
// result is kept in cache.
func scanVariants(root, sourceLang, targetLang string, g *glossary, cache string) (*variantScan, error) {
	if data, err := os.ReadFile(cache); err == nil {
		sc := &variantScan{}
		if json.Unmarshal(data, sc) == nil {
			return sc, nil
		}
	}
	const perTerm = 80
	samples := map[string][]dictSample{}
	sc := &variantScan{Missed: map[string]int{}, Samples: map[string]int{}, Grams: map[string]map[string]int{}, Examples: map[string][]string{}, DF: map[string]int{}}
	base := filepath.Join(root, "content", targetLang)
	var pages []string
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && filepath.Ext(path) == ".md" {
			rel, _ := filepath.Rel(base, path)
			pages = append(pages, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, page := range pages {
		ep, vp, ok := readAligned(root, sourceLang, targetLang, page)
		if !ok {
			continue
		}
		perPage := map[string]int{}
		for i := range ep {
			if ep[i] == vp[i] || len(ep[i]) > 2000 {
				continue
			}
			for _, term := range dictMisses(g, ep[i], vp[i]) {
				sc.Missed[term]++
				// A few from each page, so one page's wording is not
				// taken for everyone's.
				if len(samples[term]) < perTerm && perPage[term] < 2 {
					samples[term] = append(samples[term], dictSample{ep[i], vp[i]})
					perPage[term]++
				}
			}
		}
	}
	want := map[string]bool{}
	for term, ss := range samples {
		if len(ss) < 3 {
			continue
		}
		counts := map[string]int{}
		for _, s := range ss {
			for gram := range viGrams(s.vi) {
				counts[gram]++
			}
		}
		keep := map[string]int{}
		for gram, n := range counts {
			if n >= 3 && 5*n >= len(ss) {
				keep[gram] = n
				want[gram] = true
			}
		}
		sc.Grams[term] = keep
		sc.Samples[term] = len(ss)
		sort.Slice(ss, func(i, j int) bool { return len(ss[i].vi) < len(ss[j].vi) })
		for _, s := range ss[:min(3, len(ss))] {
			sc.Examples[term] = append(sc.Examples[term], "EN: "+clipAround(proseText(s.en), term, 300)+"\nVI: "+clipVI(proseText(s.vi), 400))
		}
	}
	// How common each run is anywhere, from every fourth page.
	for k, page := range pages {
		if k%4 != 0 {
			continue
		}
		_, vp, ok := readAligned(root, sourceLang, targetLang, page)
		if !ok {
			continue
		}
		for _, p := range vp {
			sc.Total++
			for gram := range viGrams(p) {
				if want[gram] {
					sc.DF[gram]++
				}
			}
		}
	}
	if data, err := json.Marshal(sc); err == nil {
		_ = os.MkdirAll(filepath.Dir(cache), 0o755)
		_ = atomicWrite(cache, data)
	}
	return sc, nil
}

// dictVariants finds, for each dictionary term the translations miss, the
// renderings it was given instead, and has the model keep the true ones.
func dictVariants(root, sourceLang, targetLang, routeFile string, dict map[string]string, out string, batch int) error {
	g := dictTerms(dict)
	sc, err := scanVariants(root, sourceLang, targetLang, g, filepath.Join(root, ".tgw", "cache", "dict-variants.json"))
	if err != nil {
		return err
	}
	type cand struct {
		text  string
		share float64
		score float64
	}
	type item struct {
		Term       string   `json:"term"`
		Vietnamese string   `json:"vietnamese"`
		Candidates []string `json:"candidates"`
		Examples   []string `json:"examples"`
	}
	var items []item
	for term, keep := range sc.Grams {
		if _, ok := g.terms[term]; !ok {
			continue
		}
		m := float64(sc.Samples[term])
		var cs []cand
		for gram, n := range keep {
			share := float64(n) / m
			lift := share / (float64(sc.DF[gram]+1) / float64(sc.Total+1))
			// One syllable is a piece of a word, not a rendering of a
			// term; and a run common anywhere is not the term's.
			if !strings.Contains(gram, " ") || share < 0.25 || lift < 30 || strings.Contains(strings.ToLower(term), gram) {
				continue
			}
			cs = append(cs, cand{gram, share, share * math.Log(lift)})
		}
		// A run inside a longer one used half as often is part of it.
		var kept []cand
		for _, a := range cs {
			covered := false
			for _, b := range cs {
				if len(b.text) > len(a.text) && strings.Contains(" "+b.text+" ", " "+a.text+" ") && 2*b.share >= a.share {
					covered = true
					break
				}
			}
			if !covered {
				kept = append(kept, a)
			}
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].score > kept[j].score })
		if len(kept) == 0 {
			continue
		}
		it := item{Term: term, Vietnamese: g.terms[term], Examples: sc.Examples[term]}
		for _, c := range kept[:min(6, len(kept))] {
			it.Candidates = append(it.Candidates, c.text)
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return sc.Missed[items[i].Term] > sc.Missed[items[j].Term] })
	fmt.Printf("dict: %d terms missed, %d with renderings to confirm\n", len(sc.Missed), len(items))
	if batch <= 0 {
		for _, it := range items {
			fmt.Printf("%d\t%s\t%s\t%s\n", sc.Missed[it.Term], it.Term, it.Vietnamese, strings.Join(it.Candidates, " | "))
		}
		return nil
	}

	llm.Configure(llm.Config{App: "tolkiengateway"})
	registry, _, err := route.LoadOrDefault(routeFile)
	if err != nil {
		return err
	}
	pool := route.NewJobPool(registry, "translate")
	if pool.Empty() {
		return fmt.Errorf("no enabled route accepts translate jobs")
	}
	log, err := ledger.Open(filepath.Join(root, ".tgw", "ledger.jsonl"))
	if err != nil {
		return err
	}
	defer log.Close()
	done, err := loadVariants(out)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	ctx := context.Background()
	var todo []item
	for _, it := range items {
		if _, ok := done[it.Term]; !ok {
			todo = append(todo, it)
		}
	}
	for i := 0; i < len(todo); i += batch {
		part := todo[i:min(i+batch, len(todo))]
		input, _ := json.Marshal(part)
		answer, err := askPhrases(ctx, pool, log, variantPrompt, string(input))
		if err != nil {
			fmt.Printf("dict: %d/%d skipped: %v\n", min(i+batch, len(todo)), len(todo), err)
			continue
		}
		n := 0
		for _, it := range part {
			got, ok := answer[it.Term]
			if !ok {
				continue
			}
			valid := map[string]bool{}
			for _, c := range it.Candidates {
				valid[c] = true
			}
			var vs []string
			for _, v := range strings.Split(got, "|") {
				if v = strings.ToLower(strings.TrimSpace(v)); valid[v] {
					vs = append(vs, v)
				}
			}
			// The term is written down even with no rendering, so it is
			// not asked about again.
			fmt.Fprintf(w, "%s\t%s\t%s\n", it.Term, strings.Join(vs, "|"), it.Vietnamese)
			n += len(vs)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("dict: %d/%d, %d renderings kept\n", min(i+batch, len(todo)), len(todo), n)
	}
	return nil
}

const variantPrompt = `You check a Vietnamese translation of Tolkien Gateway, an encyclopedia of J.R.R. Tolkien's works, against its dictionary. Each input item is an English term, the Vietnamese the dictionary gives it, a few candidate phrases found in Vietnamese paragraphs that translate the term some other way, and example paragraphs.

For each term pick the candidates that are the paragraphs' own rendering of the whole term, so that putting the dictionary's Vietnamese in their place is right wherever the English says the term. A candidate is kept only if it translates every content word of the term and nothing else: try the swap in the examples, and keep it only if every sentence still reads right. Leave out a candidate that:
- is only part of the term's rendering (nước mắt for Battle of Unnumbered Tears, phương bắc for Rangers of the North), or holds words around it (ở ..., của ...);
- is a common word or phrase that also means other things (trung thành, thông thái, quản lý, trận chiến);
- renders a different word of the sentence.
Several candidates may be right when the translation varies; when in doubt leave a candidate out, and give none if none is right.

Input is a JSON array of {"term", "vietnamese", "candidates", "examples"}. Return only a JSON object mapping each term to the candidates kept, exactly as given, joined by " | ", or "" for none. No code fences.`

// loadVariants reads the confirmed renderings: term, renderings joined by
// "|", and the dictionary's Vietnamese.
func loadVariants(path string) (map[string][]string, error) {
	out := map[string][]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		var vs []string
		for _, v := range strings.Split(f[1], "|") {
			if v = strings.TrimSpace(v); v != "" {
				vs = append(vs, v)
			}
		}
		out[f[0]] = vs
	}
	return out, nil
}

// clipAround cuts text to about n bytes around the first use of term.
func clipAround(s, term string, n int) string {
	if len(s) <= n {
		return s
	}
	at := max(0, strings.Index(s, term)-n/2)
	end := min(len(s), at+n)
	for at > 0 && !utf8.RuneStart(s[at]) {
		at--
	}
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	return "…" + strings.TrimSpace(s[at:end]) + "…"
}

func clipVI(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// variantFixer puts the dictionary's Vietnamese in place of a confirmed
// rendering, in a paragraph whose English uses the term.
type variantFixer struct {
	dict *glossary
	res  map[string][]*regexp.Regexp // term -> renderings, longest first
}

func newVariantFixer(dict map[string]string, variants map[string][]string) *variantFixer {
	f := &variantFixer{dict: dictTerms(dict), res: map[string][]*regexp.Regexp{}}
	for term, vs := range variants {
		if _, ok := f.dict.terms[term]; !ok {
			continue
		}
		sort.Slice(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
		for _, v := range vs {
			words := strings.Fields(v)
			for i := range words {
				words[i] = regexp.QuoteMeta(words[i])
			}
			f.res[term] = append(f.res[term], regexp.MustCompile(`(?i)(^|[^\p{L}\p{M}\p{N}])(`+strings.Join(words, `\s+`)+`)($|[^\p{L}\p{M}\p{N}])`))
		}
	}
	return f
}

// fix rewrites a paragraph, and says which terms it put right. A rendering
// is replaced only if it is used no more often than the English uses the
// term, so the same words meaning something else elsewhere are left.
func (f *variantFixer) fix(en, vi string, hits map[string]int) string {
	uses := map[string]int{}
	f.dict.each(proseText(en), func(term string, at int) { uses[term]++ })
	for _, term := range dictMisses(f.dict, en, vi) {
		want := f.dict.terms[term]
		for _, re := range f.res[term] {
			n := 0
			for _, s := range freeText(vi) {
				n += len(re.FindAllStringIndex(s, -1))
			}
			if n == 0 || n > uses[term] {
				continue
			}
			vi = mapFree(vi, func(s string) string {
				return re.ReplaceAllStringFunc(s, func(m string) string {
					sub := re.FindStringSubmatch(m)
					to := want
					if startsUpper(sub[2]) && !startsUpper(to) && sentenceStart(s, m) {
						r, size := utf8.DecodeRuneInString(to)
						to = string(unicode.ToUpper(r)) + to[size:]
					}
					return sub[1] + to + sub[3]
				})
			})
			hits[term] += n
			break
		}
	}
	return vi
}

// sentenceStart is a rough check that m opens a sentence of s.
func sentenceStart(s, m string) bool {
	at := strings.Index(s, m)
	before := strings.TrimRight(s[:max(0, at)], " [*_\"“(")
	return before == "" || strings.ContainsAny(before[len(before)-1:], ".!?:\n#|")
}

// freeText lists the parts of a fragment rules may rewrite.
func freeText(text string) []string {
	var out []string
	mapFree(text, func(s string) string { out = append(out, s); return s })
	return out
}

// mapFree rewrites the parts of a fragment outside link targets, tags and
// code, as rules do.
func mapFree(text string, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range protectedText.FindAllStringIndex(text, -1) {
		b.WriteString(fn(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(fn(text[last:]))
	return b.String()
}

// dictFix puts the confirmed renderings right across the corpus, or with
// dry only shows what it would change.
func dictFix(root, sourceLang, targetLang string, dict map[string]string, variants map[string][]string, dry bool, show int) error {
	f := newVariantFixer(dict, variants)
	hits := map[string]int{}
	pages, shown := 0, 0
	base := filepath.Join(root, "content", targetLang)
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		ep, vp, ok := readAligned(root, sourceLang, targetLang, filepath.ToSlash(rel))
		if !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		page := string(data)
		changed := false
		for i := range ep {
			if ep[i] == vp[i] {
				continue
			}
			fixed := f.fix(ep[i], vp[i], hits)
			if fixed == vp[i] || strings.Count(page, vp[i]) != 1 {
				continue
			}
			if shown < show {
				fmt.Printf("--- %s\n- %s\n+ %s\n", rel, clipVI(vp[i], 300), clipVI(fixed, 300))
				shown++
			}
			page = strings.Replace(page, vp[i], fixed, 1)
			changed = true
		}
		if !changed {
			return nil
		}
		pages++
		if dry {
			return nil
		}
		return atomicWrite(path, []byte(page))
	})
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(hits))
	n := 0
	for k, v := range hits {
		keys = append(keys, k)
		n += v
	}
	sort.Slice(keys, func(i, j int) bool { return hits[keys[i]] > hits[keys[j]] })
	for _, k := range keys[:min(40, len(keys))] {
		fmt.Printf("%7d  %s → %s  (%s)\n", hits[k], k, f.dict.terms[k], strings.Join(variants[k], " | "))
	}
	verb := "changed"
	if dry {
		verb = "would change"
	}
	fmt.Printf("dict: %d renderings of %d terms put right; %s %d pages\n", n, len(keys), verb, pages)
	return nil
}
