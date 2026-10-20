package main

import (
	"context"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestPublishBuildsArticleCategoryAndRedirect(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, "content", "en", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main/B/Bilbo_Baggins.md", "---\ntitle: \"Bilbo Baggins\"\nnamespace: 0\nrevision_id: 7\ntimestamp: \"2026-08-01T20:11:25Z\"\n"+
		"short_description: \"Finder of the One Ring\"\ncategories:\n  - \"Hobbits\"\nlicense: CC BY-SA 4.0\n---\n\n"+
		"## Life {#Life}\n\nBilbo of [the Shire](/wiki/The_Shire) and [Frodo](/wiki/Frodo).\n")
	write("main/B/Bilbo.md", "---\ntitle: \"Bilbo\"\nnamespace: 0\nredirect: true\nredirect_target: \"Bilbo Baggins\"\nlicense: CC BY-SA 4.0\n---\n")
	write("main/T/The_Shire.md", "---\ntitle: \"The Shire\"\nnamespace: 0\nlicense: CC BY-SA 4.0\n---\n\nA region.\n")
	out := filepath.Join(dir, "site")
	if err := runPublish([]string{"-corpus", dir, "-out", out}); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	page := read("wiki/Bilbo_Baggins.html")
	for _, want := range []string{
		`<span class="mw-headline" id="Life">Life</span>`,
		`href="/tolkiengateway/wiki/The_Shire.html"`,
		`href="https://tolkiengateway.net/wiki/Frodo"`,
		`<div id="siteSub">Finder of the One Ring</div>`,
		`page-Bilbo_Baggins`,
		`href="/tolkiengateway/wiki/Category:Hobbits.html" title="Category:Hobbits">Hobbits</a>`,
		`This page was last edited on 1 August 2026, at 20:11.`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("article missing %q:\n%s", want, page)
		}
	}
	if category := read("wiki/Category:Hobbits.html"); !strings.Contains(category, "This category contains only the following page.") {
		t.Fatalf("generated category missing its member:\n%s", category)
	}
	if redirect := read("wiki/Bilbo.html"); !strings.Contains(redirect, "/tolkiengateway/wiki/Bilbo_Baggins.html") {
		t.Fatalf("redirect does not point at its target:\n%s", redirect)
	}
	if index := read("search.json"); !strings.Contains(index, `["Bilbo","wiki/Bilbo_Baggins.html","Finder of the One Ring","Bilbo Baggins"]`) {
		t.Fatalf("search index missing the redirect:\n%s", index)
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

func TestYAMLQuoteEscapesControlCharacters(t *testing.T) {
	title := "\u0095The One Ring, \"Isildur's Bane\"\\"
	var got struct{ Title string }
	if err := yaml.Unmarshal([]byte("title: "+yamlQuote(title)+"\n"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != title {
		t.Fatalf("round trip = %q, want %q", got.Title, title)
	}
}
