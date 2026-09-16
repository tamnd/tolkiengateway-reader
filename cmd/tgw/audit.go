package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runAudit checks the corpus and writes reports/quality.md under it. The
// real rule groups land page by page as the extraction pipeline is built.
// For now it records the page count so the report file exists from the
// first commit onward.
func runAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	lang := fs.String("lang", "vi", "language directory to audit")
	corpus := fs.String("corpus", corpusRoot(), "corpus repository")
	if err := fs.Parse(args); err != nil {
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
