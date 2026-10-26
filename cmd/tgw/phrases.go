package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/tamnd/llm"
	"github.com/tamnd/llm/ledger"
	"github.com/tamnd/llm/route"
)

// The wiki's templates repeat a few thousand short phrases on every page:
// infobox labels and values (Other names, Gender, Male, Never married),
// section heads of the infobox, navbox titles. Such a phrase is translated
// once, by the model, and then put in everywhere the same way. Google never
// sees them, and they never differ from one page to the next.

// maxPhraseWords is the longest phrase the table holds.
const maxPhraseWords = 6

type phraseTable map[string]string

// loadPhrases reads a table of English and Vietnamese, tab-separated. A
// missing file is an empty table.
func loadPhrases(path string) (phraseTable, error) {
	t := phraseTable{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if en, vi, ok := strings.Cut(line, "\t"); ok && en != "" && vi != "" {
			t[en] = vi
		}
	}
	return t, nil
}

// plainSpan matches a run that is one piece of text, with only the tags
// that wrap it whole.
var plainSpan = regexp.MustCompile(`^((?:<[a-zA-Z][^<>]*>)*)([^<>]+)((?:</[a-zA-Z]+>)*)$`)

// phraseFor gives a run's translation from the table, its wrapping tags
// kept.
func (t phraseTable) phraseFor(span string) (string, bool) {
	if len(t) == 0 {
		return "", false
	}
	m := plainSpan.FindStringSubmatch(span)
	if m == nil {
		return "", false
	}
	vi, ok := t[htmlVisible(m[2])]
	if !ok {
		return "", false
	}
	return m[1] + html.EscapeString(vi) + m[3], true
}

// runPhrases collects the short phrases of the corpus's HTML, or with
// -translate has the model translate a collected list.
func runPhrases(args []string) error {
	fs := flag.NewFlagSet("phrases", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	sourceLang := fs.String("source-lang", "en", "source language directory")
	minUses := fs.Int("min", 5, "collect phrases used at least this often")
	translate := fs.String("translate", "", "translate the phrases in this file, one per line or count<TAB>phrase")
	out := fs.String("out", "", "where to write; defaults to stdout when collecting and .tgw/phrases.tsv when translating")
	routeFile := fs.String("routes", "", "llm route registry")
	batch := fs.Int("batch", 80, "phrases per request")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *translate != "" {
		if *out == "" {
			*out = filepath.Join(*root, ".tgw", "phrases.tsv")
		}
		return translatePhrases(*root, *translate, *out, *routeFile, *batch)
	}
	counts, err := collectPhrases(filepath.Join(*root, "content", *sourceLang))
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(counts))
	for k, n := range counts {
		if n >= *minUses {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	for _, k := range keys {
		fmt.Fprintf(w, "%d\t%s\n", counts[k], k)
	}
	return nil
}

// collectPhrases counts the short plain runs in the HTML of every page that
// hold a word written in lower case somewhere in the corpus, which names do
// not.
func collectPhrases(base string) (map[string]int, error) {
	counts := map[string]int{}
	lower := map[string]int{}
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, w := range htmlWord.FindAllString(text, -1) {
			if r := []rune(w); unicode.IsLower(r[0]) {
				lower[w]++
			}
		}
		head := frontMatter(text)
		for _, chunk := range splitTranslation(text[len(head):]) {
			if !chunk.verbatim {
				continue
			}
			for _, s := range htmlSpans(chunk.text) {
				m := plainSpan.FindStringSubmatch(chunk.text[s.start:s.end])
				if m == nil {
					continue
				}
				v := htmlVisible(m[2])
				if n := len(strings.Fields(v)); n > 0 && n <= maxPhraseWords && hasLetters(v) && !isCitation(v) {
					counts[v]++
				}
			}
		}
		return nil
	})
	for k := range counts {
		english := false
		for _, w := range htmlWord.FindAllString(k, -1) {
			if lower[strings.ToLower(w)] >= 20 && !htmlStop[strings.ToLower(w)] {
				english = true
				break
			}
		}
		if !english {
			delete(counts, k)
		}
	}
	return counts, err
}

const phrasePrompt = `Translate these short phrases from Tolkien Gateway, an encyclopedia of J.R.R. Tolkien's works, into Vietnamese. They are infobox labels and values, table headers and navigation titles, so translate them as such: short, the way a careful Vietnamese encyclopedia labels things ("Other names" = "Tên khác", "Never married" = "Chưa từng kết hôn").

%s
Keep names of people and places, invented words and titles of works as they are; translate the words around them. A phrase that is only a name stays the same.

Input is a JSON array of strings. Return only a JSON object mapping each input string to its translation, with no code fences.

Glossary:
%s`

// translatePhrases asks the model for the phrases not yet in the table,
// batch by batch, appending each answer, so a stopped run picks up where it
// left off.
func translatePhrases(root, in, out, routeFile string, batch int) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	done, err := loadPhrases(out)
	if err != nil {
		return err
	}
	var todo []string
	for _, line := range strings.Split(string(data), "\n") {
		if _, p, ok := strings.Cut(line, "\t"); ok {
			line = p
		}
		if line = strings.TrimSpace(line); line != "" && done[line] == "" {
			todo = append(todo, line)
		}
	}
	fmt.Printf("phrases: %d to translate\n", len(todo))
	llm.Configure(llm.Config{App: "tolkiengateway"})
	registry, _, err := route.LoadOrDefault(routeFile)
	if err != nil {
		return err
	}
	pool := route.NewJobPool(registry, "translate")
	if pool.Empty() {
		return fmt.Errorf("no enabled route accepts translate jobs")
	}
	gloss, err := loadGlossary(filepath.Join(root, ".tgw", "glossary.tsv"))
	if err != nil {
		return err
	}
	log, err := ledger.Open(filepath.Join(root, ".tgw", "ledger.jsonl"))
	if err != nil {
		return err
	}
	defer log.Close()
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx := context.Background()
	for i := 0; i < len(todo); i += batch {
		part := todo[i:min(i+batch, len(todo))]
		input, _ := json.Marshal(part)
		instruction := fmt.Sprintf(phrasePrompt, styleGuide, glossaryFor(gloss, part...))
		answer, err := askPhrases(ctx, pool, log, instruction, string(input))
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		n := 0
		for _, en := range part {
			vi := strings.TrimSpace(answer[en])
			if vi == "" || strings.ContainsAny(vi, "\t\n") || checkNumbers(en, vi) != nil {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\n", en, applyRules(vi))
			n++
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("phrases: %d/%d, %d kept\n", min(i+batch, len(todo)), len(todo), n)
	}
	return nil
}

func askPhrases(ctx context.Context, pool *route.Pool, log *ledger.Log, instruction, input string) (map[string]string, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		r, client, release, err := pickRoute(ctx, pool)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		response, callErr := client.Complete(ctx, llm.Request{Model: r.Wire(), Instructions: instruction, Input: input})
		response.Route = r.Name
		response.Elapsed = time.Since(started)
		release()
		_ = log.Record("phrases", "phrases", r.Name, attempt, response, callErr)
		if callErr != nil {
			pool.Fail(r.Name, callErr)
			lastErr = callErr
			continue
		}
		pool.Succeed(r.Name)
		text := response.Text
		start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
		if start < 0 || end < start {
			lastErr = fmt.Errorf("no JSON object in answer")
			continue
		}
		answer := map[string]string{}
		if err := json.Unmarshal([]byte(text[start:end+1]), &answer); err != nil {
			lastErr = err
			continue
		}
		return answer, nil
	}
	return nil, lastErr
}
