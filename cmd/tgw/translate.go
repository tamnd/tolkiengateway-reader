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
	"runtime"
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
	if err := enqueueTranslations(q, *root, *sourceLang, *targetLang, template.SHA); err != nil {
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

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := translateWorker(ctx, q, pool, log, instruction, *targetLang); err != nil {
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

func enqueueTranslations(q *queue.Queue, root, sourceLang, targetLang, promptSHA string) error {
	base := filepath.Join(root, "content", sourceLang)
	return filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
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

func translateWorker(ctx context.Context, q *queue.Queue, pool *route.Pool, log *ledger.Log, instruction, targetLang string) error {
	for {
		job, err := q.Lease("translate", runtime.GOOS, "", 20*time.Minute)
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
		r, client, release, err := pool.Pick(ctx)
		if err != nil {
			_ = q.Release(job, err.Error())
			return err
		}
		started := time.Now()
		response, callErr := client.Complete(ctx, llm.Request{Model: r.Wire(), Instructions: instruction, Input: string(input)})
		response.Text = strings.TrimSpace(response.Text) + "\n"
		response.Route = r.Name
		response.Elapsed = time.Since(started)
		release()
		_ = log.Record("translate", job.Target, r.Name, job.Attempts, response, callErr)
		if callErr != nil {
			pool.Fail(r.Name, callErr)
			if _, err := q.Fail(job, callErr.Error()); err != nil {
				return err
			}
			continue
		}
		pool.Succeed(r.Name)
		if err := validateTranslation(string(input), response.Text, targetLang); err != nil {
			if _, finishErr := q.Fail(job, err.Error()); finishErr != nil {
				return finishErr
			}
			continue
		}
		if err := atomicWrite(job.Meta["output"], []byte(response.Text)); err != nil {
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
	if targetLang == "vi" && strings.Contains(strings.ToLower(text), "translate the markdown article") {
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
