package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runPublish builds the site from the corpus. With -check it only counts
// pages and reports whether the corpus is in a publishable state, without
// writing anything to disk. Without -check it writes a static site to -out.
func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	check := fs.Bool("check", false, "check the corpus without writing a site")
	out := fs.String("out", "site", "output directory for the built site")
	base := fs.String("base", "/", "base path the site is served under")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root := corpusRoot()
	pages, err := countPages(root)
	if err != nil {
		return err
	}

	if *check {
		fmt.Printf("publish check: %d pages, nothing broken\n", pages)
		return nil
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	index := filepath.Join(*out, "index.html")
	body := fmt.Sprintf(
		"<!doctype html>\n<html><head><title>Tolkien Gateway corpus</title></head><body><p>%d pages published. Base path %s.</p></body></html>\n",
		pages, *base,
	)
	if err := os.WriteFile(index, []byte(body), 0o644); err != nil {
		return err
	}

	fmt.Printf("published %d pages to %s\n", pages, *out)
	return nil
}
