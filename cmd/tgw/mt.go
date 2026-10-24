package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// runMT translates pages with Google Translate. It is far faster than the
// ChatGPT route, a whole namespace in an hour, and less careful: it renders
// "fanonically" as "fanatically". So it fills the pages ChatGPT has not
// reached yet, never overwrites one ChatGPT wrote, and checks every
// paragraph the way ChatGPT's answers are checked. A paragraph that fails
// stays in English. ChatGPT, working down the same priority list, replaces
// these pages with its own as it goes.
func runMT(args []string) error {
	fs := flag.NewFlagSet("mt", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	sourceLang := fs.String("source-lang", "en", "source language directory")
	targetLang := fs.String("target-lang", "vi", "target language directory")
	dirs := fs.String("dirs", "main", "comma-separated namespace directories or pages under content/<source-lang>, or @file for one per line")
	overwrite := fs.Bool("overwrite", false, "translate pages that already have a translation")
	batch := fs.Int("batch", 60000, "characters of text per request")
	interval := fs.Duration("interval", 3*time.Second, "pause between requests")
	limit := fs.Int("limit", 0, "stop after this many pages; 0 for all")
	evalIn := fs.String("eval", "", "translate the chunks of an mteval sample.jsonl instead of pages")
	evalOut := fs.String("eval-out", "", "where -eval writes its results")
	useGlossary := fs.Bool("glossary", true, "give Google the terms ChatGPT's translations settled on")
	glossaryFile := fs.String("glossary-file", "", "read the glossary from this file instead of mining the corpus")
	fill := fs.Bool("fill", false, "in translated pages, translate only the paragraphs and HTML text still in English")
	all := fs.Bool("all", false, "with -fill, fill Google's pages too, not only ChatGPT's")
	phrasesFile := fs.String("phrases", "", "table of short HTML phrases; defaults to .tgw/phrases.tsv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g := &googleMT{sl: *sourceLang, tl: *targetLang, interval: *interval, client: &http.Client{
		Timeout: 2 * time.Minute,
		// Google answers a client it has had enough of with a redirect to a
		// captcha page, which must count as a refusal, not be followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if *phrasesFile == "" {
		*phrasesFile = filepath.Join(*root, ".tgw", "phrases.tsv")
	}
	var err error
	if htmlPhrases, err = loadPhrases(*phrasesFile); err != nil {
		return err
	}
	ctx := context.Background()
	logPath := filepath.Join(*root, ".tgw", "mt.jsonl")
	var gloss *glossary
	if *glossaryFile != "" {
		var err error
		if gloss, err = loadGlossary(*glossaryFile); err != nil {
			return err
		}
	} else if *useGlossary {
		skip, err := mtLogged(logPath)
		if err != nil {
			return err
		}
		if gloss, err = buildGlossary(*root, *sourceLang, *targetLang, skip); err != nil {
			return err
		}
		if err := gloss.save(filepath.Join(*root, ".tgw", "glossary.tsv")); err != nil {
			return err
		}
	}
	if gloss != nil {
		// A corpus without ChatGPT's pages mines nothing, and Google left
		// alone writes Yêu tinh for Elves.
		if len(gloss.terms) < 100 {
			return fmt.Errorf("glossary of only %d terms; pass -glossary-file or -glossary=false", len(gloss.terms))
		}
		fmt.Printf("mt: glossary of %d terms\n", len(gloss.terms))
	}
	if *evalIn != "" {
		return mtEval(ctx, g, gloss, *evalIn, *evalOut, *batch)
	}

	targets, err := translateTargets(*dirs)
	if err != nil {
		return err
	}
	pages, err := mtPages(*root, *sourceLang, *targetLang, targets, *overwrite || *fill)
	if err != nil {
		return err
	}
	if *fill {
		google, err := mtLogged(logPath)
		if err != nil {
			return err
		}
		kept := pages[:0]
		for _, p := range pages {
			if _, err := os.Stat(p.output); err == nil && (*all || !google[p.page]) {
				kept = append(kept, p)
			}
		}
		pages = kept
	}
	if *limit > 0 && len(pages) > *limit {
		pages = pages[:*limit]
	}
	fmt.Printf("mt: %d pages to translate\n", len(pages))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	var group []*mtDoc
	chars, done, kept, total := 0, 0, 0, 0
	flush := func() error {
		if len(group) == 0 {
			return nil
		}
		var units []*mtUnit
		for _, d := range group {
			units = append(units, d.units...)
		}
		if err := g.translateUnits(ctx, units, *batch); err != nil {
			return err
		}
		for _, d := range group {
			if !*overwrite && !*fill {
				// ChatGPT may have finished the page while this batch ran.
				if _, err := os.Stat(d.output); err == nil {
					continue
				}
			}
			text, failed := d.assemble()
			if err := validateTranslation(d.source, text, *targetLang); err != nil {
				fmt.Fprintf(os.Stderr, "mt: %s: %v\n", d.page, err)
				continue
			}
			if err := atomicWrite(d.output, []byte(text)); err != nil {
				return err
			}
			engine := "google"
			if *fill {
				engine = "google-fill"
			}
			line, _ := json.Marshal(map[string]any{"page": d.page, "engine": engine, "paragraphs": len(d.units), "english": failed, "at": time.Now().UTC().Format(time.RFC3339)})
			fmt.Fprintln(logFile, string(line))
			done++
			kept += failed
			total += len(d.units)
		}
		fmt.Printf("mt: %d/%d pages, %d of %d paragraphs left in English\n", done, len(pages), kept, total)
		group, chars = group[:0], 0
		return nil
	}
	for _, p := range pages {
		var d *mtDoc
		if *fill {
			d, err = fillDoc(p, gloss)
		} else {
			d, err = newMTDoc(p.page, p.source, p.output, gloss, nil)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "mt: skipped", err)
			continue
		}
		if *fill && len(d.units) == 0 && d.phrases == 0 {
			continue
		}
		group = append(group, d)
		chars += d.chars()
		if chars >= *batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

type mtPage struct{ page, source, output string }

// mtPages lists the pages to translate in the order given, skipping
// redirects and, unless overwrite is set, pages already translated.
func mtPages(root, sourceLang, targetLang string, targets []string, overwrite bool) ([]mtPage, error) {
	base := filepath.Join(root, "content", sourceLang)
	seen := map[string]bool{}
	var pages []mtPage
	for _, t := range targets {
		err := filepath.WalkDir(filepath.Join(base, strings.TrimSpace(t)), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			rel, _ := filepath.Rel(base, path)
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			output := filepath.Join(root, "content", targetLang, rel)
			if !overwrite {
				if _, err := os.Stat(output); err == nil {
					return nil
				}
			}
			pages = append(pages, mtPage{rel, path, output})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return pages, nil
}

// mtUnit is one paragraph sent to the engine.
type mtUnit struct {
	source string // the Markdown paragraph
	m      masked
	answer string // Google's text for m.text
	text   string // the Markdown translation, once it passed every check
	quote  bool   // whether the paragraph is a YAML string in front matter
}

func (u *mtUnit) out(text string) string {
	if u.quote {
		return strconv.Quote(text)
	}
	return text
}

// mtDoc is a page cut into the pieces that are copied and the paragraphs
// that are translated.
type mtDoc struct {
	page, output, source string
	pieces               []string  // verbatim text, or "" where a unit goes
	slots                []*mtUnit // per piece, nil for verbatim
	units                []*mtUnit
	phrases              int // HTML phrases put in from the table
}

// htmlPhrases is the table of short HTML phrases.
var htmlPhrases phraseTable

// fillDoc reads a page ChatGPT translated and makes units of only the
// paragraphs still identical to the English source.
func fillDoc(p mtPage, gloss *glossary) (*mtDoc, error) {
	en, err := os.ReadFile(p.source)
	if err != nil {
		return nil, err
	}
	if vi, err := os.ReadFile(p.output); err == nil {
		if fixed, n := repairSpans(string(en), string(vi), gloss); n > 0 {
			if err := atomicWrite(p.output, []byte(fixed)); err != nil {
				return nil, err
			}
			fmt.Printf("mt: %s: put back %d runs that are not English\n", p.page, n)
		}
	}
	english := map[string]bool{}
	for _, para := range pageParagraphs(string(en)) {
		english[para] = true
	}
	for _, span := range pageSpans(string(en)) {
		english[span] = true
	}
	if v, ok := shortDescription(frontMatter(string(en))); ok {
		english[shortKey+v] = true
	}
	return newMTDoc(p.page, p.output, p.output, gloss, english)
}

// shortKey marks the short description in the set of English paragraphs.
const shortKey = "short_description\x00"

// newMTDoc cuts a page into paragraphs to translate: its prose, the runs of
// text in its blocks of HTML, and the short description shown under its
// title. With only set, just the paragraphs in it are, and the rest is kept
// as it is.
func newMTDoc(page, source, output string, gloss *glossary, only map[string]bool) (d *mtDoc, err error) {
	// A page the masker trips on is skipped, not the whole run.
	defer func() {
		if r := recover(); r != nil {
			d, err = nil, fmt.Errorf("%s: %v", page, r)
		}
	}()
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	d = &mtDoc{page: page, output: output, source: strings.ReplaceAll(string(data), "\r\n", "\n")}
	head := frontMatter(d.source)
	d.addHead(head, gloss, only)
	for _, chunk := range splitTranslation(d.source[len(head):]) {
		if chunk.verbatim {
			d.addHTML(chunk.text, gloss, only)
			continue
		}
		for _, block := range splitBlocks(chunk.text) {
			body := strings.TrimRight(block, " \t\n")
			if !hasLetters(body) || only != nil && !only[strings.TrimSpace(body)] {
				d.add(block, nil)
				continue
			}
			u := &mtUnit{source: body, m: maskMarkdown(body, gloss)}
			d.units = append(d.units, u)
			d.add("", u)
			d.add(block[len(body):], nil)
		}
	}
	return d, nil
}

// addHTML adds a block of raw HTML, with a unit for each run of text in it
// worth translating.
func (d *mtDoc) addHTML(block string, gloss *glossary, only map[string]bool) {
	last := 0
	for _, s := range htmlSpans(block) {
		span := block[s.start:s.end]
		if only != nil && !only[span] {
			continue
		}
		vi, ok := htmlPhrases.phraseFor(span)
		if !ok && gloss != nil {
			vi, ok = phraseTable(gloss.terms).phraseFor(span)
		}
		if ok {
			d.add(block[last:s.start]+vi, nil)
			d.phrases++
			last = s.end
			continue
		}
		if !htmlTranslatable(span) {
			continue
		}
		m, err := maskHTML(span, gloss)
		if err != nil {
			continue
		}
		d.add(block[last:s.start], nil)
		u := &mtUnit{source: span, m: m}
		d.units = append(d.units, u)
		d.add("", u)
		last = s.end
	}
	d.add(block[last:], nil)
}

var shortLine = regexp.MustCompile(`(?m)^short_description: (.+)$`)

// shortDescription reads the short description from front matter.
func shortDescription(head string) (string, bool) {
	m := shortLine.FindStringSubmatch(head)
	if m == nil {
		return "", false
	}
	var v string
	if err := yaml.Unmarshal([]byte(m[1]), &v); err != nil || v == "" {
		return "", false
	}
	return v, true
}

// addHead adds the front matter, with a unit for the short description.
func (d *mtDoc) addHead(head string, gloss *glossary, only map[string]bool) {
	loc := shortLine.FindStringSubmatchIndex(head)
	v, ok := shortDescription(head)
	if !ok || !htmlTranslatable(html.EscapeString(v)) || only != nil && !only[shortKey+v] {
		d.add(head, nil)
		return
	}
	d.add(head[:loc[2]], nil)
	u := &mtUnit{source: v, m: maskMarkdown(v, gloss), quote: true}
	d.units = append(d.units, u)
	d.add("", u)
	d.add(head[loc[3]:], nil)
}

func (d *mtDoc) add(text string, u *mtUnit) {
	d.pieces = append(d.pieces, text)
	d.slots = append(d.slots, u)
}

func (d *mtDoc) chars() int {
	n := 0
	for _, u := range d.units {
		n += len(u.m.text)
	}
	return n
}

// assemble puts the page back together, with each paragraph that failed its
// checks in English, and says how many did.
func (d *mtDoc) assemble() (string, int) {
	var b strings.Builder
	failed := 0
	for i, piece := range d.pieces {
		u := d.slots[i]
		switch {
		case u == nil:
			b.WriteString(piece)
		case u.text == "":
			failed++
			b.WriteString(u.out(u.source))
		default:
			b.WriteString(u.out(u.text))
		}
	}
	return b.String(), failed
}

func hasLetters(s string) bool {
	text := maskMarkdown(s, nil).text
	text = maskTag.ReplaceAllString(text, "")
	return strings.IndexFunc(text, func(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }) >= 0
}

// check turns the engine's answer into Markdown and holds it to what a
// ChatGPT answer must keep: tags, headings, link targets and inline HTML,
// and every year and count.
func (u *mtUnit) check() error {
	text, err := u.m.unmask(u.answer)
	if err != nil {
		return err
	}
	if err := checkNumbers(u.m.text, u.answer); err != nil {
		return err
	}
	text = matchSpacing(u.source, applyRules(politePronouns(text)))
	if err := validateChunk(u.source, text); err != nil {
		return err
	}
	u.text = text
	return nil
}

type googleMT struct {
	sl, tl   string
	interval time.Duration
	client   *http.Client
	last     time.Time
}

// errRefused means Google turned the client away for now.
var errRefused = errors.New("google refused the request")

// translateUnits sends the units in requests of about batch characters, one
// paragraph per q, and checks every answer. The more a paragraph is masked
// the likelier Google drops a tag, so a paragraph that fails is sent once
// more with only its markup masked, the glossary terms left in English.
func (g *googleMT) translateUnits(ctx context.Context, units []*mtUnit, batch int) error {
	if err := g.sendUnits(ctx, units, batch); err != nil {
		return err
	}
	var again []*mtUnit
	for _, u := range units {
		if u.text == "" {
			if m := maskMarkdown(u.source, nil); m.text != u.m.text {
				u.m = m
				again = append(again, u)
			}
		}
	}
	if len(again) == 0 {
		return nil
	}
	return g.sendUnits(ctx, again, batch)
}

func (g *googleMT) sendUnits(ctx context.Context, units []*mtUnit, batch int) error {
	for start := 0; start < len(units); {
		end, size := start, 0
		for end < len(units) && (end == start || size+len(units[end].m.text) <= batch) {
			size += len(units[end].m.text)
			end++
		}
		texts := make([]string, 0, end-start)
		for _, u := range units[start:end] {
			texts = append(texts, u.m.text)
		}
		answers, err := g.translate(ctx, texts)
		if err != nil {
			return err
		}
		for i, u := range units[start:end] {
			u.answer = answers[i]
			_ = u.check()
		}
		start = end
	}
	return nil
}

// translate sends texts in one request, waiting out refusals: the endpoint
// is free and unofficial, and Google rations it per address.
func (g *googleMT) translate(ctx context.Context, texts []string) ([]string, error) {
	backoff := 5 * time.Minute
	for attempt := 1; ; attempt++ {
		if wait := g.interval - time.Since(g.last); wait > 0 {
			time.Sleep(wait)
		}
		g.last = time.Now()
		answers, err := g.request(ctx, texts)
		if err == nil {
			return answers, nil
		}
		if errors.Is(err, errRefused) {
			fmt.Fprintf(os.Stderr, "mt: %v; waiting %s\n", err, backoff)
			time.Sleep(backoff)
			backoff = min(2*backoff, time.Hour)
			continue
		}
		if attempt >= 5 {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "mt: %v; retrying\n", err)
		time.Sleep(time.Duration(attempt) * 10 * time.Second)
	}
}

func (g *googleMT) request(ctx context.Context, texts []string) ([]string, error) {
	form := url.Values{"q": texts}
	endpoint := "https://translate.googleapis.com/translate_a/t?client=gtx&sl=" + url.QueryEscape(g.sl) + "&tl=" + url.QueryEscape(g.tl)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 3:
		return nil, fmt.Errorf("%w: HTTP %d", errRefused, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("google: HTTP %d: %.200s", resp.StatusCode, body)
	}
	return parseGoogle(body, len(texts))
}

// parseGoogle reads the answer, an array with one entry per q: a string, or
// [text, detected language] when the source language is auto.
func parseGoogle(body []byte, want int) ([]string, error) {
	if want == 1 {
		// One q gets the bare answer, not an array of one.
		var single any
		if err := json.Unmarshal(body, &single); err != nil {
			return nil, err
		}
		if _, ok := single.([]any); !ok {
			body = append(append([]byte("["), body...), ']')
		}
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	if len(raw) != want {
		return nil, fmt.Errorf("google: %d answers for %d texts", len(raw), want)
	}
	out := make([]string, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &out[i]); err == nil {
			continue
		}
		var pair []string
		if err := json.Unmarshal(r, &pair); err != nil || len(pair) == 0 {
			return nil, fmt.Errorf("google: answer %d: %s", i, r)
		}
		out[i] = pair[0]
	}
	return out, nil
}

// mtEval runs the chunks of an mteval sample through the same pipeline as a
// page, for tools/mteval to score against the other engines.
func mtEval(ctx context.Context, g *googleMT, gloss *glossary, in, out string, batch int) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	type row struct {
		ID     string `json:"id"`
		Source string `json:"source"`
	}
	var rows []row
	var docs []*mtDoc
	var units []*mtUnit
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<24)
	for scanner.Scan() {
		var r row
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return err
		}
		tmp := filepath.Join(os.TempDir(), "tgw-mt-eval.md")
		if err := os.WriteFile(tmp, []byte(r.Source), 0o644); err != nil {
			return err
		}
		d, err := newMTDoc(r.ID, tmp, "", gloss, nil)
		if err != nil {
			return err
		}
		rows, docs, units = append(rows, r), append(docs, d), append(units, d.units...)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := g.translateUnits(ctx, units, batch); err != nil {
		return err
	}
	w, err := os.Create(out)
	if err != nil {
		return err
	}
	defer w.Close()
	for i, d := range docs {
		text, failed := d.assemble()
		var problems []string
		for _, u := range d.units {
			if err := u.check(); err != nil {
				problems = append(problems, err.Error())
			}
		}
		line, _ := json.Marshal(map[string]any{"id": rows[i].ID, "text": text, "english": failed, "paragraphs": len(d.units), "problems": problems})
		fmt.Fprintln(w, string(line))
	}
	return nil
}

// politePronoun swaps the pronouns Google uses for a friend, anh ấy and
// cô ấy, and the cold anh ta and cô ta, for those an encyclopedia uses of a
// grown man or woman.
var politePronoun = regexp.MustCompile(`(^|[^\p{L}])([aA]nh|[cC]ô) (ấy|ta)([^\p{L}]|$)`)

func politePronouns(s string) string {
	// Matches share the boundary between them, so run until none is left.
	for i := 0; i < 3; i++ {
		s = politePronoun.ReplaceAllStringFunc(s, func(m string) string {
			g := politePronoun.FindStringSubmatch(m)
			word := map[string]string{"anh": "ông", "Anh": "Ông", "cô": "bà", "Cô": "Bà"}[g[2]]
			return g[1] + word + g[4]
		})
	}
	return s
}
