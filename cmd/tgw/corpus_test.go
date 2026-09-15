package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
