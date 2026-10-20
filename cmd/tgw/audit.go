package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// runAudit checks the corpus and writes reports/quality.md under it. The
// real rule groups land page by page as the extraction pipeline is built.
// For now it records the page count so the report file exists from the
// first commit onward.
func runAudit(args []string) error {
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	lang := flags.String("lang", "en", "language directory to audit")
	corpus := flags.String("corpus", corpusRoot(), "corpus repository")
	if err := flags.Parse(args); err != nil {
		return err
	}

	root := *corpus
	files, err := markdownFiles(languageContentRoot(root, *lang))
	if err != nil {
		return err
	}
	var failures []string
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(string(data)), "---") {
			failures = append(failures, path+": missing YAML front matter")
		}
		if !strings.Contains(string(data), "license: CC BY-SA 4.0") {
			failures = append(failures, path+": missing CC BY-SA 4.0 license metadata")
		}
	}

	reportDir := filepath.Join(root, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return err
	}

	report := fmt.Sprintf("# Quality report\n\nLanguage: %s\nPages checked: %d\nHard rule failures: %d\n", *lang, len(files), len(failures))
	if len(failures) > 0 {
		report += "\n## Failures\n\n- " + strings.Join(failures, "\n- ") + "\n"
	}
	if err := atomicWrite(filepath.Join(reportDir, "quality.md"), []byte(report)); err != nil {
		return err
	}

	if len(failures) > 0 {
		return fmt.Errorf("audit: %d hard failures", len(failures))
	}
	fmt.Printf("audit: %d %s pages, no hard failures\n", len(files), *lang)
	return nil
}

// markdownFiles lists the corpus pages below root, sorted.
func markdownFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
