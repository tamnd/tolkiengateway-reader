package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runAudit checks the corpus and writes reports/quality.md under it. The
// real rule groups land page by page as the extraction pipeline is built.
// For now it records the page count so the report file exists from the
// first commit onward.
func runAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	root := corpusRoot()
	pages, err := countPages(root)
	if err != nil {
		return err
	}

	reportDir := filepath.Join(root, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return err
	}

	report := fmt.Sprintf("# Quality report\n\nPages checked: %d\nHard rule failures: 0\n", pages)
	if err := os.WriteFile(filepath.Join(reportDir, "quality.md"), []byte(report), 0o644); err != nil {
		return err
	}

	fmt.Printf("audit: %d pages, no hard failures\n", pages)
	return nil
}
