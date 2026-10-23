package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/llm"
	"github.com/tamnd/llm/ledger"
	"github.com/tamnd/llm/prompt"
	"github.com/tamnd/llm/route"
)

//go:embed prompts/review.md
var reviewPrompts embed.FS

// runReview is the model layer of the quality harness. It sends paragraphs,
// a random sample of every engine plus the worst tgw qa flagged, to a model
// that lists their errors MQM style and gives a corrected paragraph. The
// errors score each engine; with -fix, corrections that keep the markup and
// numbers are written back to the pages.
func runReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	sourceLang := fs.String("source-lang", "en", "source language directory")
	targetLang := fs.String("target-lang", "vi", "target language directory")
	routeFile := fs.String("routes", "", "llm route registry; defaults to Tolkien Gateway config")
	sample := fs.Int("sample", 60, "random paragraphs to review per engine")
	worst := fs.Int("worst", 60, "paragraphs flagged by tgw qa to review")
	checks := fs.String("checks", "english,glue,term,spaces,punct", "tgw qa checks whose paragraphs count as worst")
	batch := fs.Int("batch", 12, "paragraphs per request")
	fix := fs.Bool("fix", false, "write corrections that keep the markup back to the pages")
	seed := fs.Int64("seed", 0, "sampling seed; 0 picks one from the clock")
	out := fs.String("out", "", "where to log each reviewed paragraph; defaults to .tgw/review.jsonl")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(*root, ".tgw", "review.jsonl")
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	llm.Configure(llm.Config{App: "tolkiengateway"})
	registry, _, err := route.LoadOrDefault(*routeFile)
	if err != nil {
		return err
	}
	pool := route.NewJobPool(registry, "translate")
	if pool.Empty() {
		return fmt.Errorf("no enabled route accepts translate jobs")
	}
	template, err := prompt.New(reviewPrompts).Get("review")
	if err != nil {
		return err
	}
	gloss, err := loadGlossary(filepath.Join(*root, ".tgw", "glossary.tsv"))
	if err != nil {
		return err
	}
	google, err := mtLogged(filepath.Join(*root, ".tgw", "mt.jsonl"))
	if err != nil {
		return err
	}

	items, err := reviewSample(*root, *sourceLang, *targetLang, google, rng, *sample)
	if err != nil {
		return err
	}
	flagged, err := reviewWorst(*root, *sourceLang, *targetLang, strings.Split(*checks, ","), rng, *worst)
	if err != nil {
		return err
	}
	items = append(items, flagged...)
	fmt.Printf("review: %d paragraphs (seed %d)\n", len(items), *seed)

	logFile, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log, err := ledger.Open(filepath.Join(*root, ".tgw", "ledger.jsonl"))
	if err != nil {
		return err
	}
	defer log.Close()

	ctx := context.Background()
	var batches [][]*reviewItem
	for i := 0; i < len(items); i += *batch {
		batches = append(batches, items[i:min(i+*batch, len(items))])
	}
	work := make(chan []*reviewItem)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < max(pool.Lanes(), 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range work {
				instruction, err := template.Render(map[string]string{"LANGUAGE": "Vietnamese", "GLOSSARY": reviewGlossary(gloss, b)})
				if err == nil {
					err = reviewBatch(ctx, pool, log, instruction, b)
				}
				mu.Lock()
				if err != nil {
					fmt.Fprintln(os.Stderr, "review:", err)
				}
				for _, it := range b {
					if it.Verdict == nil {
						continue
					}
					if *fix {
						it.applyFix(*root, *targetLang)
					}
					line, _ := json.Marshal(it)
					fmt.Fprintln(logFile, string(line))
				}
				mu.Unlock()
			}
		}()
	}
	for i, b := range batches {
		work <- b
		fmt.Printf("review: batch %d/%d\n", i+1, len(batches))
	}
	close(work)
	wg.Wait()
	reviewReport(items)
	return nil
}

type reviewItem struct {
	Page    string        `json:"page"`
	Para    int           `json:"para"`
	Engine  string        `json:"engine"`
	Why     string        `json:"why"` // sample, or the qa check that flagged it
	EN      string        `json:"en"`
	VI      string        `json:"vi"`
	Verdict *reviewAnswer `json:"verdict,omitempty"`
	Applied string        `json:"applied,omitempty"` // yes, or why the fix was not
}

type reviewAnswer struct {
	ID     int           `json:"id"`
	Errors []reviewError `json:"errors"`
	Fix    string        `json:"fix"`
}

type reviewError struct {
	Cat  string `json:"cat"`
	Sev  string `json:"sev"`
	EN   string `json:"en"`
	Bad  string `json:"bad"`
	Good string `json:"good"`
}

// reviewSample picks random paragraphs, n per engine, one per page, from
// paragraphs long enough to judge.
func reviewSample(root, sourceLang, targetLang string, google map[string]bool, rng *rand.Rand, n int) ([]*reviewItem, error) {
	var pages []string
	base := filepath.Join(root, "content", targetLang)
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && filepath.Ext(path) == ".md" {
			rel, _ := filepath.Rel(base, path)
			pages = append(pages, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	rng.Shuffle(len(pages), func(i, j int) { pages[i], pages[j] = pages[j], pages[i] })
	taken := map[string]int{}
	var out []*reviewItem
	for _, page := range pages {
		engine := "gpt"
		if google[page] {
			engine = "google"
		}
		if taken[engine] >= n {
			if taken["gpt"] >= n && taken["google"] >= n {
				break
			}
			continue
		}
		ep, vp, ok := readAligned(root, sourceLang, targetLang, page)
		if !ok {
			continue
		}
		var long []int
		for i := range vp {
			if ep[i] != vp[i] && len(ep[i]) >= 120 && !strings.HasPrefix(ep[i], "|") {
				long = append(long, i)
			}
		}
		if len(long) == 0 {
			continue
		}
		i := long[rng.Intn(len(long))]
		out = append(out, &reviewItem{Page: page, Para: i, Engine: engine, Why: "sample", EN: ep[i], VI: vp[i]})
		taken[engine]++
	}
	return out, nil
}

// reviewWorst picks paragraphs tgw qa flagged.
func reviewWorst(root, sourceLang, targetLang string, checks []string, rng *rand.Rand, n int) ([]*reviewItem, error) {
	f, err := os.Open(filepath.Join(root, ".tgw", "qa.jsonl"))
	if os.IsNotExist(err) || n <= 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	want := map[string]bool{}
	for _, c := range checks {
		want[strings.TrimSpace(c)] = true
	}
	var found []qaProblem
	seen := map[string]bool{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		var p qaProblem
		key := ""
		if json.Unmarshal(s.Bytes(), &p) == nil && want[p.Check] && p.Para >= 0 {
			key = fmt.Sprintf("%s#%d", p.Page, p.Para)
		}
		if key != "" && !seen[key] {
			seen[key] = true
			found = append(found, p)
		}
	}
	rng.Shuffle(len(found), func(i, j int) { found[i], found[j] = found[j], found[i] })
	var out []*reviewItem
	for _, p := range found {
		if len(out) >= n {
			break
		}
		ep, vp, ok := readAligned(root, sourceLang, targetLang, p.Page)
		if !ok || p.Para >= len(vp) || len(ep[p.Para]) > 3000 {
			continue
		}
		out = append(out, &reviewItem{Page: p.Page, Para: p.Para, Engine: p.Engine, Why: p.Check, EN: ep[p.Para], VI: vp[p.Para]})
	}
	return out, s.Err()
}

func readAligned(root, sourceLang, targetLang, page string) (en, vi []string, ok bool) {
	e, err := os.ReadFile(filepath.Join(root, "content", sourceLang, page))
	if err != nil {
		return nil, nil, false
	}
	v, err := os.ReadFile(filepath.Join(root, "content", targetLang, page))
	if err != nil {
		return nil, nil, false
	}
	en, vi = pageParagraphs(string(e)), pageParagraphs(string(v))
	return en, vi, len(en) == len(vi)
}

func loadGlossary(path string) (*glossary, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newGlossary(nil), nil
	}
	if err != nil {
		return nil, err
	}
	terms := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if en, vi, ok := strings.Cut(line, "\t"); ok {
			terms[en] = vi
		}
	}
	return newGlossary(terms), nil
}

// reviewGlossary lists the glossary terms that occur in a batch.
func reviewGlossary(g *glossary, items []*reviewItem) string {
	used := map[string]string{}
	for _, it := range items {
		g.replace(it.EN, func(term, vi string) string { used[term] = vi; return term })
		for text := range linkTexts(it.EN) {
			if vi, ok := g.terms[text]; ok {
				used[text] = vi
			}
		}
	}
	if len(used) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(used))
	for k := range used {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %s\n", k, used[k])
	}
	return b.String()
}

func reviewBatch(ctx context.Context, pool *route.Pool, log *ledger.Log, instruction string, items []*reviewItem) error {
	var in strings.Builder
	for i, it := range items {
		fmt.Fprintf(&in, "### Item %d\nEN:\n%s\n\nVI:\n%s\n\n", i+1, it.EN, it.VI)
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		r, client, release, err := pickRoute(ctx, pool)
		if err != nil {
			return err
		}
		started := time.Now()
		response, callErr := client.Complete(ctx, llm.Request{Model: r.Wire(), Instructions: instruction, Input: in.String()})
		response.Route = r.Name
		response.Elapsed = time.Since(started)
		release()
		_ = log.Record("review", items[0].Page, r.Name, attempt, response, callErr)
		if callErr != nil {
			pool.Fail(r.Name, callErr)
			lastErr = callErr
			continue
		}
		pool.Succeed(r.Name)
		answers, err := parseReview(response.Text)
		if err != nil {
			lastErr = err
			continue
		}
		for _, a := range answers {
			if a.ID >= 1 && a.ID <= len(items) {
				a := a
				items[a.ID-1].Verdict = &a
			}
		}
		return nil
	}
	return lastErr
}

// parseReview reads the JSON array out of an answer, past any code fence
// or words around it.
func parseReview(text string) ([]reviewAnswer, error) {
	start, end := strings.Index(text, "["), strings.LastIndex(text, "]")
	if start < 0 || end < start {
		return nil, fmt.Errorf("no JSON array in answer")
	}
	var answers []reviewAnswer
	if err := json.Unmarshal([]byte(text[start:end+1]), &answers); err != nil {
		return nil, fmt.Errorf("answer: %w", err)
	}
	return answers, nil
}

// applyFix writes the corrected paragraph into the page if it keeps the
// source's markup and numbers and the page still has the paragraph that
// was reviewed.
func (it *reviewItem) applyFix(root, targetLang string) {
	fix := strings.TrimSpace(it.Verdict.Fix)
	switch {
	case fix == "" || fix == it.VI:
		return
	case validateChunk(it.EN, fix) != nil:
		it.Applied = "markup: " + validateChunk(it.EN, fix).Error()
		return
	case checkNumbers(it.EN, fix) != nil:
		it.Applied = checkNumbers(it.EN, fix).Error()
		return
	}
	path := filepath.Join(root, "content", targetLang, it.Page)
	data, err := os.ReadFile(path)
	if err != nil || strings.Count(string(data), it.VI) != 1 {
		it.Applied = "paragraph changed or repeated"
		return
	}
	if err := atomicWrite(path, []byte(strings.Replace(string(data), it.VI, fix, 1))); err != nil {
		it.Applied = err.Error()
		return
	}
	it.Applied = "yes"
}

// mqmWeight scores an error by severity, as MQM does.
var mqmWeight = map[string]int{"minor": 1, "major": 5, "critical": 10}

func reviewReport(items []*reviewItem) {
	type score struct {
		items, words, penalty, clean int
		cats                         map[string]int
	}
	scores := map[string]*score{}
	terms := map[string]int{}
	for _, it := range items {
		if it.Verdict == nil {
			continue
		}
		key := it.Engine + " " + it.Why
		if it.Why != "sample" {
			key = it.Engine + " flagged"
		}
		s := scores[key]
		if s == nil {
			s = &score{cats: map[string]int{}}
			scores[key] = s
		}
		s.items++
		s.words += len(strings.Fields(it.EN))
		if len(it.Verdict.Errors) == 0 {
			s.clean++
		}
		for _, e := range it.Verdict.Errors {
			s.penalty += mqmWeight[e.Sev]
			s.cats[e.Cat+"/"+e.Sev]++
			if e.Cat == "terminology" && e.EN != "" && e.Good != "" {
				terms[e.EN+" → "+e.Good]++
			}
		}
	}
	keys := make([]string, 0, len(scores))
	for k := range scores {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := scores[k]
		fmt.Printf("\n%s: %d paragraphs, %d clean, MQM penalty %.1f per 100 words\n", k, s.items, s.clean, 100*float64(s.penalty)/float64(max(s.words, 1)))
		cats := make([]string, 0, len(s.cats))
		for c := range s.cats {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		for _, c := range cats {
			fmt.Printf("  %-24s %d\n", c, s.cats[c])
		}
	}
	type term struct {
		text string
		n    int
	}
	var ranked []term
	for t, n := range terms {
		ranked = append(ranked, term{t, n})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].n > ranked[j].n })
	if len(ranked) > 0 {
		fmt.Println("\nterminology fixes:")
	}
	for i, t := range ranked {
		if i == 40 {
			break
		}
		fmt.Printf("  %3d  %s\n", t.n, t.text)
	}
}
