package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tamnd/tolkiengateway-reader/internal/fetch"
)

type syncState struct {
	Since time.Time `json:"since"`
}

// runSync refreshes only pages named by MediaWiki recentchanges. It stores the
// high-water mark after the batch, so a failed run is replayed rather than
// silently advancing past pages that were not written.
func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	lang := fs.String("lang", "en", "source language directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	statePath := filepath.Join(*root, ".tgw", "sync.json")
	state := syncState{Since: time.Now().UTC().Add(-24 * time.Hour)}
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Since.IsZero() {
		state.Since = time.Now().UTC().Add(-24 * time.Hour)
	}
	ctx := context.Background()
	client, err := fetch.NewClient(ctx, strings.TrimRight(*base, "/"))
	if err != nil {
		return fmt.Errorf("warm a session: %w", err)
	}
	changes, newest, err := recentChanges(ctx, client, state.Since)
	if err != nil {
		return err
	}
	for _, title := range changes {
		page, err := fetchRawPage(ctx, client, title)
		if err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(*root, "raw", slugTitle(page.Title)+".json"), mustJSON(page)); err != nil {
			return err
		}
		path := filepath.Join(*root, "content", *lang, "main", firstLetter(page.Title), slugTitle(page.Title)+".md")
		if err := atomicWrite(path, []byte(renderArticle(page))); err != nil {
			return err
		}
	}
	if !newest.IsZero() {
		state.Since = newest
	}
	if err := atomicWrite(statePath, mustJSON(state)); err != nil {
		return err
	}
	fmt.Printf("synced %d changed pages through %s\n", len(changes), state.Since.Format(time.RFC3339))
	return nil
}

func recentChanges(ctx context.Context, client pageFetcher, since time.Time) ([]string, time.Time, error) {
	values := url.Values{}
	values.Set("action", "query")
	values.Set("list", "recentchanges")
	values.Set("rcnamespace", "0")
	values.Set("rctype", "edit|new")
	values.Set("rcprop", "title|timestamp")
	values.Set("rclimit", "500")
	values.Set("rcstart", time.Now().UTC().Format(time.RFC3339))
	values.Set("rcend", since.UTC().Format(time.RFC3339))
	values.Set("format", "json")
	resp, err := client.Get(ctx, apiPath+"?"+values.Encode())
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("fetch recent changes: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, time.Time{}, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if fetch.Challenged(resp) {
			return nil, time.Time{}, fmt.Errorf("recent changes is still behind the Cloudflare challenge")
		}
		return nil, time.Time{}, fmt.Errorf("recent changes returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Query struct {
			Changes []struct {
				Title     string    `json:"title"`
				Timestamp time.Time `json:"timestamp"`
			} `json:"recentchanges"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode recent changes: %w", err)
	}
	seen := map[string]bool{}
	var titles []string
	newest := since
	for _, change := range result.Query.Changes {
		if !seen[change.Title] {
			seen[change.Title] = true
			titles = append(titles, change.Title)
		}
		if change.Timestamp.After(newest) {
			newest = change.Timestamp
		}
	}
	return titles, newest, nil
}

func fetchRawPage(ctx context.Context, client pageFetcher, title string) (rawPage, error) {
	values := url.Values{}
	values.Set("action", "query")
	values.Set("prop", "revisions|info")
	values.Set("rvprop", "content|ids")
	values.Set("rvslots", "main")
	values.Set("inprop", "url")
	values.Set("titles", title)
	values.Set("format", "json")
	resp, err := client.Get(ctx, apiPath+"?"+values.Encode())
	if err != nil {
		return rawPage{}, fmt.Errorf("fetch page %q: %w", title, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return rawPage{}, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if fetch.Challenged(resp) {
			return rawPage{}, fmt.Errorf("page %q is still behind the Cloudflare challenge", title)
		}
		return rawPage{}, fmt.Errorf("page %q returned HTTP %d", title, resp.StatusCode)
	}
	var result struct {
		Query struct {
			Pages map[string]struct {
				Title     string `json:"title"`
				PageID    int    `json:"pageid"`
				Namespace int    `json:"ns"`
				FullURL   string `json:"fullurl"`
				Revisions []struct {
					Revision int `json:"revid"`
					Slots    struct {
						Main struct {
							Content string `json:"*"`
						} `json:"main"`
					} `json:"slots"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return rawPage{}, fmt.Errorf("decode page %q: %w", title, err)
	}
	for _, p := range result.Query.Pages {
		if p.PageID == 0 || len(p.Revisions) == 0 {
			return rawPage{}, fmt.Errorf("page %q was not found or has no revision", title)
		}
		page := rawPage{Title: p.Title, PageID: p.PageID, Namespace: p.Namespace, Revision: p.Revisions[0].Revision, Wikitext: p.Revisions[0].Slots.Main.Content, SourceURL: p.FullURL}
		page.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(page.Wikitext)))
		return page, nil
	}
	return rawPage{}, fmt.Errorf("page %q was not found", title)
}

func firstLetter(title string) string {
	runes := []rune(title)
	if len(runes) == 0 {
		return "_"
	}
	letter := strings.ToUpper(string(runes[0]))
	if letter < "A" || letter > "Z" {
		return "_"
	}
	return letter
}
