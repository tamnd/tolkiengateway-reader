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

func TestWikitextToMarkdown(t *testing.T) {
	got := wikitextToMarkdown(`<!-- hidden -->
== Life ==
Bilbo '''Baggins''' visited [[The Shire|the Shire]].
[[Category:Hobbits]]
<ref>Source note</ref>`)
	want := "## Life\n\nBilbo **Baggins** visited [the Shire](/wiki/The_Shire).\n\n> Reference: Source note\n"
	if got != want {
		t.Fatalf("unexpected markdown:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteSiteBuildsArticleAndIndex(t *testing.T) {
	dir := t.TempDir()
	content := filepath.Join(dir, "content", "vi", "main", "B")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	article := "---\ntitle: \"Bilbo Baggins\"\nlicense: CC BY-SA 4.0\n---\n\n## Cuộc đời\n\nBilbo [Shire](/wiki/The_Shire).\n"
	path := filepath.Join(content, "Bilbo_Baggins.md")
	if err := os.WriteFile(path, []byte(article), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := markdownFiles(filepath.Join(dir, "content", "vi"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "site")
	if err := writeSite(out, "/tolkiengateway", "vi", pages); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(out, "main", "B", "Bilbo_Baggins.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "<h2>Cuộc đời</h2>") || !strings.Contains(string(page), "Shire") {
		t.Fatalf("article was not rendered: %s", page)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "/tolkiengateway/main/B/Bilbo_Baggins.html") {
		t.Fatalf("index link missing: %s", index)
	}
}

func TestRenderArticleCarriesCategoriesAndInfobox(t *testing.T) {
	text := renderArticle(rawPage{
		Title: "Bilbo Baggins", PageID: 1, Namespace: 0, Revision: 7,
		SourceURL: "https://tolkiengateway.net/wiki/Bilbo_Baggins",
		Wikitext:  "{{Infobox character\n| name = Bilbo Baggins\n| born = Third Age\n}}\n== Life ==\nText.\n[[Category:Hobbits]]",
	})
	for _, want := range []string{
		`categories:`, `- "Hobbits"`, `infobox:`, `name: "Bilbo Baggins"`, `revision_id: 7`, `## Life`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered article missing %q:\n%s", want, text)
		}
	}
}

func TestRenderArticleCarriesRedirectMetadata(t *testing.T) {
	text := renderArticle(rawPage{Title: "Bilbo", PageID: 2, Revision: 3, Wikitext: "#REDIRECT [[Bilbo Baggins]]"})
	for _, want := range []string{`redirect: true`, `redirect_target: "Bilbo Baggins"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("redirect metadata missing %q:\n%s", want, text)
		}
	}
}

func TestWikitextToMarkdownConvertsMainTemplate(t *testing.T) {
	got := wikitextToMarkdown("{{Main|Hobbits}}")
	want := "See also: [Hobbits](/wiki/Hobbits).\n"
	if got != want {
		t.Fatalf("unexpected main template conversion: %q", got)
	}
}

/*
func TestTranslateQueuesAndWritesVietnameseArticle(t *testing.T) {
	translated := "---\ntitle: \"Bilbo Baggins\"\nlicense: CC BY-SA 4.0\n---\n\n## Cuộc đời\n\nBilbo là một Hobbit.\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"id": "test", "model": "test-model", "choices": []any{map[string]any{"delta": map[string]string{"content": translated}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n", chunk)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	content := filepath.Join(dir, "content", "en", "main", "B")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "Bilbo_Baggins.md"), []byte("---\ntitle: \"Bilbo Baggins\"\nlicense: CC BY-SA 4.0\n---\n\n## Life\n\nBilbo is a Hobbit.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	routeFile := filepath.Join(dir, "routes.json")
	routes := map[string]any{"routes": []any{map[string]any{"name": "test", "kind": "gateway", "base_url": server.URL, "model": "test-model", "rank": 1, "concurrency": 1, "jobs": []string{"translate"}}}}
	routeData, _ := json.Marshal(routes)
	if err := os.WriteFile(routeFile, routeData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runTranslate([]string{"--corpus", dir, "--routes", routeFile, "--no-tunnels"}); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(filepath.Join(dir, "content", "vi", "main", "B", "Bilbo_Baggins.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != translated {
		t.Fatalf("unexpected translation:\n%s", output)
	}
}
*/

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
