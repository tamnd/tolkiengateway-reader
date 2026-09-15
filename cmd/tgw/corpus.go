package main

import (
	"io/fs"
	"os"
	"path/filepath"
)

// corpusRoot returns the checked out tolkiengateway repo, read from
// TOLKIENGATEWAY_CORPUS, falling back to the current directory.
func corpusRoot() string {
	if v := os.Getenv("TOLKIENGATEWAY_CORPUS"); v != "" {
		return v
	}
	return "."
}

// countPages walks content/ under root and counts the Markdown files in it.
// An empty or missing content directory counts as zero pages rather than
// an error, since a fresh corpus has neither yet.
func countPages(root string) (int, error) {
	contentDir := filepath.Join(root, "content")
	if _, err := os.Stat(contentDir); os.IsNotExist(err) {
		return 0, nil
	}

	count := 0
	err := filepath.WalkDir(contentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".md" {
			count++
		}
		return nil
	})
	return count, err
}
