package main

import (
	"strings"
	"testing"
)

func TestConvertLinksPointAtCorpusFiles(t *testing.T) {
	links := &corpusLinks{
		files: map[string]string{
			"The Shire":        "main/T/The_Shire.md",
			"Gandalf":          "main/G/Gandalf.md",
			"Bilbo":            "main/B/Bilbo.md",
			"Category:Hobbits": "category/H/Hobbits.md",
			"AC/DC":            "main/A/AC%2FDC.md",
		},
		styles: map[string]string{},
	}
	source := `<div class="mw-parser-output"><p>See <a href="/wiki/The_Shire" title="The Shire">the Shire</a>, ` +
		`<a href="/wiki/gandalf#Early_life" title="Gandalf">Gandalf</a>, <a href="/wiki/Bilbo#Life">below</a>, ` +
		`<a href="/wiki/Category:Hobbits">hobbits</a>, <a href="/wiki/AC/DC">band</a> and <a href="/wiki/Nowhere">nowhere</a>.</p>` +
		`<style data-mw-deduplicate="TemplateStyles:r1">.mw-parser-output .x{color:red}</style>` +
		`<link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1"/>` +
		`<figure><a class="mw-file-description" href="/wiki/File:X.jpg"><img src="/w/images/thumb/a/ab/X.jpg/300px-X.jpg" srcset="/w/images/thumb/a/ab/X.jpg/600px-X.jpg 2x"/></a></figure></div>`
	got, _ := htmlToMarkdown(source, links, "main/B/Bilbo.md")
	for _, want := range []string{
		"[the Shire](../T/The_Shire.md)",
		"[Gandalf](../G/Gandalf.md#Early_life)",
		"[below](#Life)",
		"[hobbits](../../category/H/Hobbits.md)",
		"[band](../A/AC%252FDC.md)",
		"[nowhere](https://tolkiengateway.net/wiki/Nowhere)",
		`href="https://tolkiengateway.net/wiki/File:X.jpg"`,
		`src="https://tolkiengateway.net/w/images/thumb/a/ab/X.jpg/300px-X.jpg"`,
		`srcset="https://tolkiengateway.net/w/images/thumb/a/ab/X.jpg/600px-X.jpg 2x"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<style") || strings.Contains(got, "mw-data:") || strings.Contains(got, `"The Shire"`) {
		t.Errorf("styles or link titles left in:\n%s", got)
	}
	if links.styles["TemplateStyles:r1"] != ".mw-parser-output .x{color:red}" {
		t.Errorf("styles not collected: %v", links.styles)
	}
}
