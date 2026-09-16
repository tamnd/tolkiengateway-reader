package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	sectionRE = regexp.MustCompile(`^(={2,6})\s*(.*?)\s*$`)
	refRE     = regexp.MustCompile(`(?is)<ref(?:\s[^>]*)?>(.*?)</ref\s*>|<ref\s*/>`)
	commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// runExtract turns one acquired revision into a Markdown article. The raw
// revision remains the source of truth; extraction can therefore be rerun
// after improving a conversion rule without fetching the page again.
func runExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	lang := fs.String("lang", "en", "output language directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: tgw extract [flags] <raw-page.json>")
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("read raw page: %w", err)
	}
	var source rawPage
	if err := json.Unmarshal(raw, &source); err != nil {
		return fmt.Errorf("decode raw page: %w", err)
	}
	if source.Title == "" || source.PageID == 0 {
		return fmt.Errorf("raw page is missing title or page id")
	}
	contentHash := sha256.Sum256([]byte(source.Wikitext))
	markdown := renderArticle(source)
	letter := strings.ToUpper(string([]rune(source.Title)[0]))
	if letter < "A" || letter > "Z" {
		letter = "_"
	}
	path := filepath.Join(*root, "content", *lang, "main", letter, slugTitle(source.Title)+".md")
	if err := atomicWrite(path, []byte(markdown)); err != nil {
		return err
	}
	fmt.Printf("extracted %s revision %d (%s)\n", source.Title, source.Revision, hex.EncodeToString(contentHash[:]))
	return nil
}

func renderArticle(source rawPage) string {
	hash := sha256.Sum256([]byte(source.Wikitext))
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlQuote(source.Title))
	fmt.Fprintf(&b, "page_id: %d\nnamespace: %d\nrevision_id: %d\n", source.PageID, source.Namespace, source.Revision)
	sourceURL := source.SourceURL
	if sourceURL == "" {
		sourceURL = "https://tolkiengateway.net/wiki/" + strings.ReplaceAll(source.Title, " ", "_")
	}
	fmt.Fprintf(&b, "source_url: %s\ncontent_sha256: %s\nlicense: CC BY-SA 4.0\n", yamlQuote(sourceURL), hex.EncodeToString(hash[:]))
	b.WriteString("source: Tolkien Gateway\n---\n\n")
	categories := extractCategories(source.Wikitext)
	if len(categories) == 0 {
		b.WriteString("categories: []\n")
	} else {
		b.WriteString("categories:\n")
	}
	for _, category := range categories {
		fmt.Fprintf(&b, "  - %s\n", yamlQuote(category))
	}
	if infobox := extractInfobox(source.Wikitext); len(infobox) > 0 {
		b.WriteString("infobox:\n")
		keys := make([]string, 0, len(infobox))
		for key := range infobox {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", yamlKey(key), yamlQuote(infobox[key]))
		}
	}
	b.WriteString("\n")
	b.WriteString(wikitextToMarkdown(source.Wikitext))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

var categoryRE = regexp.MustCompile(`(?i)\[\[Category:([^\]|]+)(?:\|[^\]]*)?\]\]`)

func extractCategories(text string) []string {
	matches := categoryRE.FindAllStringSubmatch(text, -1)
	values := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		value := strings.TrimSpace(match[1])
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func extractInfobox(text string) map[string]string {
	start := strings.Index(strings.ToLower(text), "{{infobox")
	if start < 0 {
		return nil
	}
	end := strings.Index(text[start:], "\n}}")
	if end < 0 {
		end = strings.Index(text[start:], "}}")
	}
	if end < 0 {
		return nil
	}
	block := text[start : start+end]
	fields := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "|"))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			fields[key] = value
		}
	}
	return fields
}

func yamlKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "field"
	}
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func wikitextToMarkdown(text string) string {
	text = commentRE.ReplaceAllString(text, "")
	text = refRE.ReplaceAllStringFunc(text, func(value string) string {
		if strings.HasSuffix(strings.TrimSpace(value), "/> ") || strings.HasSuffix(strings.TrimSpace(value), "/>") {
			return ""
		}
		match := refRE.FindStringSubmatch(value)
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			return "\n> Reference: " + strings.TrimSpace(match[1]) + "\n"
		}
		return ""
	})
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "[[Category:") || strings.HasPrefix(trimmed, "{{DEFAULTSORT:") {
			continue
		}
		if strings.HasPrefix(trimmed, "{{Infobox") {
			continue
		}
		if strings.HasPrefix(trimmed, "|") || trimmed == "}}" {
			continue
		}
		if match := sectionRE.FindStringSubmatch(trimmed); len(match) > 0 &&
			strings.HasSuffix(trimmed, match[1]) {
			level := len(match[1])
			heading := strings.TrimSpace(strings.TrimSuffix(match[2], match[1]))
			out = append(out, strings.Repeat("#", level)+" "+heading)
			continue
		}
		line = strings.ReplaceAll(line, "'''", "**")
		line = strings.ReplaceAll(line, "''", "*")
		line = convertLinks(line)
		line = regexp.MustCompile(`(?i)\[\[File:[^\]]+\]\]`).ReplaceAllStringFunc(line, func(string) string { return "[Image omitted; see original page]" })
		out = append(out, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(out, "\n\n")) + "\n"
}

func convertLinks(line string) string {
	linkRE := regexp.MustCompile(`\[\[([^\]|#]+)(?:#([^\]|]+))?(?:\|([^\]]+))?\]\]`)
	return linkRE.ReplaceAllStringFunc(line, func(value string) string {
		match := linkRE.FindStringSubmatch(value)
		title, section, label := match[1], match[2], match[3]
		if label == "" {
			label = title
		}
		target := "/wiki/" + strings.ReplaceAll(strings.TrimSpace(title), " ", "_")
		if section != "" {
			target += "#" + strings.ReplaceAll(strings.TrimSpace(section), " ", "_")
		}
		return "[" + strings.TrimSpace(label) + "](" + target + ")"
	})
}
