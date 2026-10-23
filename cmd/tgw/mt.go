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
	"strings"
	"time"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	g := &googleMT{sl: *sourceLang, tl: *targetLang, interval: *interval, client: &http.Client{
		Timeout: 2 * time.Minute,
		// Google answers a client it has had enough of with a redirect to a
		// captcha page, which must count as a refusal, not be followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	ctx := context.Background()
	if *evalIn != "" {
		return mtEval(ctx, g, *evalIn, *evalOut, *batch)
	}

	targets, err := translateTargets(*dirs)
	if err != nil {
		return err
	}
	pages, err := mtPages(*root, *sourceLang, *targetLang, targets, *overwrite)
	if err != nil {
		return err
	}
	if *limit > 0 && len(pages) > *limit {
		pages = pages[:*limit]
	}
	fmt.Printf("mt: %d pages to translate\n", len(pages))
	logFile, err := os.OpenFile(filepath.Join(*root, ".tgw", "mt.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
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
			if !*overwrite {
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
			line, _ := json.Marshal(map[string]any{"page": d.page, "engine": "google", "paragraphs": len(d.units), "english": failed, "at": time.Now().UTC().Format(time.RFC3339)})
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
		d, err := newMTDoc(p.page, p.source, p.output)
		if err != nil {
			return err
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
}

// mtDoc is a page cut into the pieces that are copied and the paragraphs
// that are translated.
type mtDoc struct {
	page, output, source string
	pieces               []string  // verbatim text, or "" where a unit goes
	slots                []*mtUnit // per piece, nil for verbatim
	units                []*mtUnit
}

func newMTDoc(page, source, output string) (*mtDoc, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	d := &mtDoc{page: page, output: output, source: strings.ReplaceAll(string(data), "\r\n", "\n")}
	head := frontMatter(d.source)
	d.add(head, nil)
	for _, chunk := range splitTranslation(d.source[len(head):]) {
		if chunk.verbatim {
			d.add(chunk.text, nil)
			continue
		}
		for _, block := range splitBlocks(chunk.text) {
			body := strings.TrimRight(block, " \t\n")
			if !hasLetters(body) {
				d.add(block, nil)
				continue
			}
			u := &mtUnit{source: body, m: maskMarkdown(body)}
			d.units = append(d.units, u)
			d.add("", u)
			d.add(block[len(body):], nil)
		}
	}
	return d, nil
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
			b.WriteString(u.source)
		default:
			b.WriteString(u.text)
		}
	}
	return b.String(), failed
}

func hasLetters(s string) bool {
	text := maskMarkdown(s).text
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
	text = matchSpacing(u.source, text)
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
// paragraph per q, and checks every answer.
func (g *googleMT) translateUnits(ctx context.Context, units []*mtUnit, batch int) error {
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
func mtEval(ctx context.Context, g *googleMT, in, out string, batch int) error {
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
		d, err := newMTDoc(r.ID, tmp, "")
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
