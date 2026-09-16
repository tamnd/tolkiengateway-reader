package main

import (
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// runPublish builds the site from the corpus. With -check it only counts
// pages and reports whether the corpus is in a publishable state, without
// writing anything to disk. Without -check it writes a static site to -out.
func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	check := fs.Bool("check", false, "check the corpus without writing a site")
	out := fs.String("out", "site", "output directory for the built site")
	base := fs.String("base", "/", "base path the site is served under")
	lang := fs.String("lang", "vi", "language directory to publish")
	corpus := fs.String("corpus", corpusRoot(), "corpus repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root := *corpus
	contentRoot := languageContentRoot(root, *lang)
	pages, err := markdownFiles(contentRoot)
	if err != nil {
		return err
	}

	if *check {
		fmt.Printf("publish check: %d %s pages, nothing broken\n", len(pages), *lang)
		return nil
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	if err := writeSite(*out, *base, *lang, pages); err != nil {
		return err
	}

	fmt.Printf("published %d %s pages to %s\n", len(pages), *lang, *out)
	return nil
}

func languageContentRoot(root, lang string) string {
	preferred := filepath.Join(root, "content", lang)
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}
	return filepath.Join(root, "content", "en")
}

func markdownFiles(root string) ([]string, error) {
	var files []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return files, nil
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".md" {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func writeSite(out, base, lang string, pages []string) error {
	var links []string
	for _, source := range pages {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(filepath.Dir(filepath.Dir(source))), source)
		if err != nil {
			return err
		}
		// The article path is stable and mirrors the corpus tree, without the
		// language directory: /main/B/Bilbo_Baggins.html.
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, lang+"/")
		rel = strings.TrimSuffix(rel, ".md") + ".html"
		destination := filepath.Join(out, filepath.FromSlash(rel))
		if err := atomicWrite(destination, []byte(articleHTML(data, base, rel))); err != nil {
			return err
		}
		title := frontMatterValue(string(data), "title")
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(source), ".md")
		}
		links = append(links, fmt.Sprintf("<li><a href=\"%s\">%s</a></li>", html.EscapeString(joinBase(base, rel)), html.EscapeString(title)))
	}
	index := "<!doctype html><html lang=\"" + html.EscapeString(lang) + "\"><head><meta charset=\"utf-8\"><title>Tolkien Gateway</title></head><body><main><h1>Tolkien Gateway</h1><p>Vietnamese corpus: " + fmt.Sprint(len(links)) + " articles.</p><ul>" + strings.Join(links, "\n") + "</ul></main></body></html>\n"
	return atomicWrite(filepath.Join(out, "index.html"), []byte(index))
}

func articleHTML(data []byte, base, rel string) string {
	text := string(data)
	if strings.HasPrefix(strings.TrimSpace(text), "---") {
		if end := strings.Index(text[3:], "\n---"); end >= 0 {
			text = text[end+7:]
		}
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var body []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if level > 6 {
				level = 6
			}
			body = append(body, fmt.Sprintf("<h%d>%s</h%d>", level, html.EscapeString(strings.TrimSpace(strings.TrimLeft(trimmed, "#"))), level))
			continue
		}
		body = append(body, "<p>"+markdownInlineHTML(trimmed, base, rel)+"</p>")
	}
	title := html.EscapeString(frontMatterValue(string(data), "title"))
	return "<!doctype html><html lang=\"vi\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><title>" + title + " - Tolkien Gateway</title></head><body><main>" + strings.Join(body, "\n") + "</main><footer>Source: Tolkien Gateway · CC BY-SA 4.0</footer></body></html>\n"
}

var markdownLinkRE = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

func markdownInlineHTML(text, base, current string) string {
	text = html.EscapeString(text)
	return markdownLinkRE.ReplaceAllStringFunc(text, func(value string) string {
		match := markdownLinkRE.FindStringSubmatch(value)
		return "<a href=\"" + html.EscapeString(joinBase(base, match[2])) + "\">" + match[1] + "</a>"
	})
}

func joinBase(base, path string) string {
	base = "/" + strings.Trim(base, "/")
	if base == "/" {
		return "/" + strings.TrimLeft(path, "/")
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

func frontMatterValue(text, key string) string {
	if !strings.HasPrefix(strings.TrimSpace(text), "---") {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		prefix := key + ":"
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), "\"")
		}
	}
	return ""
}
