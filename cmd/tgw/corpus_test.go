package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllPagesFollowsContinuationAndSorts(t *testing.T) {
	responses := []string{
		`{"query":{"namespaces":{"0":{"id":0}}}}`,
		`{"query":{"allpages":[{"pageid":2,"ns":0,"title":"Zed"},{"pageid":1,"ns":0,"title":"A \"page\"","redirect":""}]},"continue":{"apcontinue":"Zed"}}`,
		`{"query":{"allpages":[{"pageid":3,"ns":0,"title":"Bilbo"}]}}`,
	}
	index := 0
	client := pageFetcherFunc(func(context.Context, string) (*http.Response, error) {
		body := responses[index]
		index++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	pages, err := allPages(context.Background(), client, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 || pages[0].Title != `A "page"` || pages[1].Title != "Bilbo" || pages[2].Title != "Zed" {
		t.Fatalf("unexpected pages: %+v", pages)
	}
	if !pages[0].Redirect || pages[1].Redirect {
		t.Fatalf("unexpected redirect flags: %+v", pages)
	}
	if index != 3 {
		t.Fatalf("want namespace plus two API calls, got %d", index)
	}
}

type pageFetcherFunc func(context.Context, string) (*http.Response, error)

func (f pageFetcherFunc) Get(ctx context.Context, path string) (*http.Response, error) {
	return f(ctx, path)
}

func TestWritePageManifestAndReport(t *testing.T) {
	dir := t.TempDir()
	pages := []page{{Title: `A "page"`, PageID: 1, Namespace: 0, Redirect: true}}
	if err := writePageManifest(dir, pages); err != nil {
		t.Fatal(err)
	}
	if err := writePageReport(dir, 1); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "manifests", "pages.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "title: \"A \\\"page\\\"\""
	if !strings.Contains(string(manifest), want) || !strings.Contains(string(manifest), "redirect: true") {
		t.Fatalf("manifest missing fields:\n%s", manifest)
	}
	report, err := os.ReadFile(filepath.Join(dir, "reports", "pages.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "Pages in the inventory: 1") {
		t.Fatalf("report missing count: %s", report)
	}
}

func TestCountPagesEmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	n, err := countPages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 pages, got %d", n)
	}
}

func TestCountPagesMissingContentDir(t *testing.T) {
	dir := t.TempDir()
	n, err := countPages(filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 pages, got %d", n)
	}
}

func TestCountPagesFindsMarkdown(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "content", "en", "main", "B")
	if err := os.MkdirAll(page, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(page, "Bilbo_Baggins.md"), []byte("---\ntitle: Bilbo Baggins\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(page, "notes.txt"), []byte("not a page"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := countPages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 page, got %d", n)
	}
}
