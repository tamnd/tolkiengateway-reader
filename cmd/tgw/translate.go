package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/llm"
	"github.com/tamnd/llm/fleet"
	"github.com/tamnd/llm/ledger"
	"github.com/tamnd/llm/prompt"
	"github.com/tamnd/llm/queue"
	"github.com/tamnd/llm/route"
)

//go:embed prompts/translate.md
var promptFiles embed.FS

func runTranslate(args []string) error {
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	sourceLang := fs.String("source-lang", "en", "source language directory")
	targetLang := fs.String("target-lang", "vi", "target language directory")
	glossary := fs.String("glossary", "Tolkien names and terms; prefer established Vietnamese Tolkien usage", "translation glossary")
	routeFile := fs.String("routes", "", "llm route registry; defaults to Tolkien Gateway config")
	workers := fs.Int("workers", 0, "parallel translation workers; defaults to fleet capacity")
	noTunnels := fs.Bool("no-tunnels", false, "do not start SSH tunnels; use already-running local endpoints")
	dirs := fs.String("dirs", "main", "comma-separated namespace directories under content/<source-lang> to translate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sourceLang) == "" || strings.TrimSpace(*targetLang) == "" || *sourceLang == *targetLang {
		return fmt.Errorf("source and target languages must be non-empty and different")
	}

	llm.Configure(llm.Config{App: "tolkiengateway"})
	registry, routePath, err := route.LoadOrDefault(*routeFile)
	if err != nil {
		return err
	}
	if len(registry.Routes) == 0 {
		return fmt.Errorf("no translation routes configured; add server1/2/3 routes to %s", routePath)
	}
	p := prompt.New(promptFiles)
	template, err := p.Get("translate")
	if err != nil {
		return err
	}
	instruction, err := template.Render(map[string]string{"LANGUAGE": "Vietnamese", "GLOSSARY": *glossary})
	if err != nil {
		return err
	}

	queueRoot := filepath.Join(*root, ".tgw", "queue")
	q, err := queue.Open(queueRoot, "translate")
	if err != nil {
		return err
	}
	if err := enqueueTranslations(q, *root, *sourceLang, *targetLang, template.SHA, strings.Split(*dirs, ",")); err != nil {
		return err
	}
	if *workers <= 0 {
		*workers = route.NewJobPool(registry, "translate").Lanes()
	}
	if *workers < 1 {
		*workers = 1
	}
	pool := route.NewJobPool(registry, "translate")
	if pool.Empty() {
		return fmt.Errorf("no enabled route accepts translate jobs")
	}
	ctx := context.Background()
	var supervisor *fleet.Supervisor
	var tunnels []fleet.Tunnel
	if !*noTunnels {
		supervisor, tunnels, err = startTranslationTunnels(ctx, registry)
		if err != nil {
			return err
		}
		if supervisor != nil {
			defer supervisor.Down(tunnels)
		}
	}
	log, err := ledger.Open(filepath.Join(*root, ".tgw", "ledger.jsonl"))
	if err != nil {
		return err
	}
	defer log.Close()

	cache := filepath.Join(*root, ".tgw", "cache", "translate")

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := translateWorker(ctx, q, pool, log, cache, instruction, *targetLang); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	stats, err := q.Stats("translate")
	if err != nil {
		return err
	}
	fmt.Printf("translation queue: %d done, %d pending, %d dead\n", stats.Counts[queue.Done], stats.Counts[queue.Pending], stats.Counts[queue.Dead])
	return nil
}

func startTranslationTunnels(ctx context.Context, registry route.Registry) (*fleet.Supervisor, []fleet.Tunnel, error) {
	links := fleet.Links(registry)
	if len(links) == 0 {
		return nil, nil, nil
	}
	byName := make(map[string]route.Route, len(registry.Routes))
	for _, value := range registry.Routes {
		byName[value.Name] = value
	}
	filtered := links[:0]
	for _, link := range links {
		value := byName[link.Route]
		if value.Kind != route.KindPool || !value.Does("translate") {
			continue
		}
		link.Check = healthCheck(strings.TrimRight(value.BaseURL, "/") + "/health")
		filtered = append(filtered, link)
	}
	links = filtered
	if len(links) == 0 {
		return nil, nil, nil
	}
	supervisor := &fleet.Supervisor{Logf: func(format string, args ...any) { fmt.Printf("tunnel: "+format+"\n", args...) }}
	tunnels, err := supervisor.Up(ctx, links)
	if err != nil {
		return nil, nil, fmt.Errorf("start translation tunnels: %w", err)
	}
	return supervisor, tunnels, nil
}

func healthCheck(endpoint string) func(context.Context) error {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("health returned HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

// enqueueTranslations queues every page in the chosen namespace
// directories. Redirects are skipped: the site draws them from the source
// page's front matter, so they have no text to translate.
func enqueueTranslations(q *queue.Queue, root, sourceLang, targetLang, promptSHA string, dirs []string) error {
	base := filepath.Join(root, "content", sourceLang)
	for _, dir := range dirs {
		if err := enqueueDir(q, root, base, filepath.Join(base, strings.TrimSpace(dir)), targetLang, promptSHA); err != nil {
			return err
		}
	}
	return nil
}

func enqueueDir(q *queue.Queue, root, base, dir, targetLang, promptSHA string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(frontMatter(string(data)), "\nredirect: true\n") {
			return nil
		}
		sum := sha256.Sum256(data)
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		target := filepath.ToSlash(rel)
		job := queue.New("translate", target, hex.EncodeToString(sum[:]), promptSHA)
		job.Meta = map[string]string{
			"source": path,
			"output": filepath.Join(root, "content", targetLang, rel),
		}
		_, err = q.Add(job)
		return err
	})
}

func translateWorker(ctx context.Context, q *queue.Queue, pool *route.Pool, log *ledger.Log, cache, instruction, targetLang string) error {
	for {
		job, err := q.Lease("translate", runtime.GOOS, "", time.Hour)
		if errors.Is(err, queue.ErrEmpty) {
			return nil
		}
		if err != nil {
			return err
		}
		input, err := os.ReadFile(job.Meta["source"])
		if err != nil {
			_, finishErr := q.Finish(job, false, err.Error())
			if finishErr != nil {
				return finishErr
			}
			continue
		}
		output, err := translateDocument(ctx, pool, log, cache, job, instruction, string(input))
		if errors.Is(err, errNoRoute) {
			_ = q.Release(job, err.Error())
			return err
		}
		if err == nil {
			err = validateTranslation(string(input), output, targetLang)
		}
		if err != nil {
			if _, finishErr := q.Fail(job, err.Error()); finishErr != nil {
				return finishErr
			}
			continue
		}
		if err := atomicWrite(job.Meta["output"], []byte(output)); err != nil {
			if _, finishErr := q.Finish(job, false, err.Error()); finishErr != nil {
				return finishErr
			}
			continue
		}
		if _, err := q.Finish(job, true, ""); err != nil {
			return err
		}
	}
}

func validateTranslation(source, text, targetLang string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("translation output is empty")
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "---") {
		return fmt.Errorf("translation output has no YAML front matter")
	}
	if sourceFrontMatter, outputFrontMatter := frontMatter(source), frontMatter(text); sourceFrontMatter != outputFrontMatter {
		return fmt.Errorf("translation output changed YAML front matter")
	}
	if targetLang == "vi" && strings.Contains(strings.ToLower(text), "translate the markdown fragment") {
		return fmt.Errorf("translation output contains prompt text")
	}
	return nil
}

func frontMatter(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	if end := strings.Index(text[4:], "\n---\n"); end >= 0 {
		return text[:end+9]
	}
	return ""
}

// pickRoute waits out a cold spell instead of giving up: a single account
// parked for a while should pause an unattended run, not end it.
func pickRoute(ctx context.Context, pool *route.Pool) (route.Route, llm.Completer, func(), error) {
	for {
		r, client, release, err := pool.Pick(ctx)
		if err == nil {
			return r, client, release, nil
		}
		at, _ := pool.EarliestReset()
		wait := time.Until(at)
		if at.IsZero() || wait > 3*time.Hour {
			return r, client, release, err
		}
		fmt.Fprintf(os.Stderr, "translate: %v; waiting\n", err)
		select {
		case <-ctx.Done():
			return r, client, release, ctx.Err()
		case <-time.After(max(wait, time.Second)):
		}
	}
}

// errNoRoute means no translation route can take work right now.
var errNoRoute = errors.New("no translation route available")

// maxChunk caps the prose sent in one request. Whole articles run to
// hundreds of kilobytes, too long for one reliable answer.
const maxChunk = 6 << 10

// translationChunk is one piece of a page body. Verbatim chunks, raw HTML
// such as infoboxes and style blocks, are copied unchanged.
type translationChunk struct {
	text     string
	verbatim bool
}

// translateDocument translates a page body chunk by chunk and puts it back
// behind the unchanged front matter.
func translateDocument(ctx context.Context, pool *route.Pool, log *ledger.Log, cache string, job queue.Job, instruction, document string) (string, error) {
	document = strings.ReplaceAll(document, "\r\n", "\n")
	head := frontMatter(document)
	var out strings.Builder
	out.WriteString(head)
	for i, chunk := range splitTranslation(document[len(head):]) {
		if chunk.verbatim || strings.TrimSpace(chunk.text) == "" {
			out.WriteString(chunk.text)
			continue
		}
		// A page fails as a whole, so its finished chunks are kept on disk
		// and the retry only asks again for the ones that did not pass.
		sum := sha256.Sum256([]byte(instruction + "\x00" + chunk.text))
		cached := filepath.Join(cache, hex.EncodeToString(sum[:])+".md")
		if data, err := os.ReadFile(cached); err == nil {
			out.Write(data)
			continue
		}
		text, err := translateChunk(ctx, pool, log, job, i, instruction, chunk.text)
		if err != nil {
			return "", err
		}
		if err := atomicWrite(cached, []byte(text)); err != nil {
			return "", err
		}
		out.WriteString(text)
	}
	return out.String(), nil
}

func translateChunk(ctx context.Context, pool *route.Pool, log *ledger.Log, job queue.Job, index int, instruction, source string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		r, client, release, err := pickRoute(ctx, pool)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errNoRoute, err)
		}
		started := time.Now()
		response, callErr := client.Complete(ctx, llm.Request{Model: r.Wire(), Instructions: instruction, Input: source})
		response.Route = r.Name
		response.Elapsed = time.Since(started)
		release()
		_ = log.Record("translate", fmt.Sprintf("%s#%d", job.Target, index), r.Name, attempt, response, callErr)
		if callErr != nil {
			pool.Fail(r.Name, callErr)
			lastErr = callErr
			continue
		}
		pool.Succeed(r.Name)
		text := matchSpacing(source, unescapeMarks(source, unlinkURLs(source, unfence(response.Text))))
		if err := validateChunk(source, text); err != nil {
			lastErr = err
			continue
		}
		return text, nil
	}
	return "", fmt.Errorf("chunk %d: %w", index, lastErr)
}

// splitTranslation cuts a page body into chunks. Blocks of raw HTML, which
// start with a tag and run to a blank line, are kept verbatim. Prose is
// grouped by section and capped at maxChunk.
func splitTranslation(body string) []translationChunk {
	var chunks []translationChunk
	var prose strings.Builder
	flush := func() {
		if prose.Len() > 0 {
			chunks = append(chunks, translationChunk{text: prose.String()})
			prose.Reset()
		}
	}
	for _, block := range splitBlocks(body) {
		if isRawHTML(block) {
			flush()
			if n := len(chunks); n > 0 && chunks[n-1].verbatim {
				chunks[n-1].text += block
			} else {
				chunks = append(chunks, translationChunk{text: block, verbatim: true})
			}
			continue
		}
		if prose.Len() > 0 && (strings.HasPrefix(block, "## ") || prose.Len()+len(block) > maxChunk) {
			flush()
		}
		prose.WriteString(block)
	}
	flush()
	return chunks
}

// splitBlocks splits text after each run of blank lines, so every block
// keeps its trailing blank lines and joining the blocks gives back the text.
func splitBlocks(text string) []string {
	var blocks []string
	lines := strings.SplitAfter(text, "\n")
	start, blank := 0, false
	pos := 0
	for _, line := range lines {
		isBlank := strings.TrimSpace(line) == ""
		if blank && !isBlank {
			blocks = append(blocks, text[start:pos])
			start = pos
		}
		blank = isBlank
		pos += len(line)
	}
	if start < len(text) {
		blocks = append(blocks, text[start:])
	}
	return blocks
}

var leadingTag = regexp.MustCompile(`^<(/?[a-zA-Z][a-zA-Z0-9]*)`)

// isRawHTML reports whether a block is HTML the site renders as is. Inline
// tags that open a paragraph, like <sup> or <span>, do not count.
func isRawHTML(block string) bool {
	m := leadingTag.FindStringSubmatch(strings.TrimSpace(block))
	if m == nil {
		return false
	}
	switch strings.ToLower(strings.TrimPrefix(m[1], "/")) {
	case "sup", "sub", "span", "a", "b", "i", "em", "strong", "small", "abbr", "br", "code", "s", "u":
		return false
	}
	return true
}

var (
	chunkHeading = regexp.MustCompile(`(?m)^(#{1,6}) .*?(\{#[^}]*\})?\s*$`)
	chunkLink    = regexp.MustCompile(`\]\(([^)\s]+)`)
	chunkTag     = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)`)
)

// validateChunk checks that a translation kept the fragment's structure:
// heading levels and anchors, link targets, and inline tags.
func validateChunk(source, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("translation is empty")
	}
	if a, b := headingShape(source), headingShape(text); a != b {
		return fmt.Errorf("headings changed: %q became %q", a, b)
	}
	if a, b := matchList(chunkLink, source), matchList(chunkLink, text); a != b {
		return fmt.Errorf("link targets changed")
	}
	if a, b := matchList(chunkTag, source), matchList(chunkTag, text); a != b {
		return fmt.Errorf("inline HTML changed")
	}
	if n, m := len(source), len([]rune(text)); m < n/4 {
		return fmt.Errorf("translation is too short: %d characters for %d", m, n)
	}
	return nil
}

func headingShape(text string) string {
	var parts []string
	for _, m := range chunkHeading.FindAllStringSubmatch(text, -1) {
		parts = append(parts, m[1]+m[2])
	}
	return strings.Join(parts, " ")
}

// matchList returns the first group of every match, sorted, so a
// translation may reorder links within a sentence.
func matchList(re *regexp.Regexp, text string) string {
	var parts []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		parts = append(parts, m[1])
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// unfence drops a code fence some models wrap around their answer.
func unfence(text string) string {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") && strings.HasSuffix(t, "```") {
		if i := strings.Index(t, "\n"); i >= 0 {
			t = strings.TrimSpace(strings.TrimSuffix(t[i+1:], "```"))
		}
	}
	return t
}

// unlinkURLs undoes the links ChatGPT makes of bare URLs. Its server
// rewrites every URL in an answer as [url](url), Markdown-escaped, even
// inside an HTML attribute, which turns href="http://…" into
// href="[http://…](http://…)". A link whose text and target are the same
// URL, and which the source did not have, goes back to the plain URL.
func unlinkURLs(source, text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], "[http") {
			if url, n := urlLink(text[i:]); n > 0 && !strings.Contains(source, text[i:i+n]) {
				b.WriteString(url)
				i += n
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// urlLink reads a [url](url) link at the start of s and returns the URL
// and the link's length, or 0 when s does not start with one.
func urlLink(s string) (string, int) {
	end := strings.Index(s, "](")
	if end < 0 || strings.ContainsAny(s[:end], " \t\n") {
		return "", 0
	}
	label := s[1:end]
	depth := 0
	for i := end + 2; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case ' ', '\n':
			return "", 0
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			url := markdownUnescape(label)
			if url != markdownUnescape(s[end+2:i]) {
				return "", 0
			}
			return url, i + 1
		}
	}
	return "", 0
}

// markdownUnescape drops the backslashes Markdown puts before punctuation.
func markdownUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!<>|~", s[i+1]) >= 0 {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeMarks drops the backslashes ChatGPT puts before emphasis and
// link brackets, which turn *[olvar](…)* into literal asterisks around a
// link. An escape the source itself uses is kept.
func unescapeMarks(source, text string) string {
	for _, c := range "*_[]" {
		esc := `\` + string(c)
		if !strings.Contains(source, esc) {
			text = strings.ReplaceAll(text, esc, string(c))
		}
	}
	return text
}

// matchSpacing gives the translation the source's leading and trailing
// whitespace, so chunks join back with the same blank lines.
func matchSpacing(source, text string) string {
	lead := source[:len(source)-len(strings.TrimLeft(source, " \t\n"))]
	trail := source[len(strings.TrimRight(source, " \t\n")):]
	return lead + strings.TrimSpace(text) + trail
}
