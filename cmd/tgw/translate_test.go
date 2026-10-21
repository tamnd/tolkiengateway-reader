package main

import (
	"strings"
	"testing"
)

const sampleBody = `
<table class="infobox"><tr><td>Gondor</td></tr></table>
<style data-mw-deduplicate="x">.a{color:red}</style>

**Gondor** was a [realm](/wiki/Realm "Realm") of [Men](/wiki/Men "Men").<sup id="cite_ref-1">[1]</sup>

## History {#History}

It was founded by [Isildur](/wiki/Isildur "Isildur").

<center>A quote</center>

### Kings {#Kings}

The kings ruled.
`

func TestSplitTranslationRoundTrips(t *testing.T) {
	chunks := splitTranslation(sampleBody)
	var joined strings.Builder
	for _, c := range chunks {
		joined.WriteString(c.text)
	}
	if joined.String() != sampleBody {
		t.Fatalf("chunks do not join back:\n%q", joined.String())
	}
	var kinds []string
	for _, c := range chunks {
		kind := "prose"
		if c.verbatim {
			kind = "html"
		}
		kinds = append(kinds, kind)
	}
	if got, want := strings.Join(kinds, ","), "prose,html,prose,prose,html,prose"; got != want {
		t.Fatalf("chunk kinds = %s, want %s", got, want)
	}
	if !strings.HasPrefix(chunks[3].text, "## History") {
		t.Fatalf("section does not start a chunk: %q", chunks[3].text)
	}
}

func TestSplitTranslationCapsChunks(t *testing.T) {
	body := strings.Repeat(strings.Repeat("word ", 200)+"\n\n", 40)
	for _, c := range splitTranslation(body) {
		if len(c.text) > maxChunk+1100 {
			t.Fatalf("chunk of %d bytes exceeds the cap", len(c.text))
		}
	}
}

func TestIsRawHTML(t *testing.T) {
	for block, want := range map[string]bool{
		"<table><tr></tr></table>\n": true,
		"<div class=\"x\">y</div>\n": true,
		"<sup>[1]</sup> text\n":      false,
		"<span>x</span> y\n":         false,
		"plain text <b>x</b>\n":      false,
	} {
		if got := isRawHTML(block); got != want {
			t.Errorf("isRawHTML(%q) = %v, want %v", block, got, want)
		}
	}
}

func TestValidateChunk(t *testing.T) {
	source := "## History {#History}\n\nIt was founded by [Isildur](/wiki/Isildur \"Isildur\").<sup>[1]</sup>\n"
	good := "## Lịch sử {#History}\n\nNó được thành lập bởi [Isildur](/wiki/Isildur \"Isildur\").<sup>[1]</sup>\n"
	if err := validateChunk(source, good); err != nil {
		t.Fatalf("good translation rejected: %v", err)
	}
	for name, bad := range map[string]string{
		"anchor": "## Lịch sử {#Lich_su}\n\nNó được thành lập bởi [Isildur](/wiki/Isildur).<sup>[1]</sup>\n",
		"level":  "### Lịch sử {#History}\n\nNó được thành lập bởi [Isildur](/wiki/Isildur).<sup>[1]</sup>\n",
		"link":   "## Lịch sử {#History}\n\nNó được thành lập bởi [Isildur](/wiki/Isildur_I).<sup>[1]</sup>\n",
		"tag":    "## Lịch sử {#History}\n\nNó được thành lập bởi [Isildur](/wiki/Isildur). [1]\n",
		"empty":  " \n",
	} {
		if err := validateChunk(source, bad); err == nil {
			t.Errorf("%s change was accepted", name)
		}
	}
}

func TestMatchSpacingAndUnfence(t *testing.T) {
	got := matchSpacing("\nHello\n\n", unfence("```markdown\nXin chào\n```"))
	if got != "\nXin chào\n\n" {
		t.Fatalf("got %q", got)
	}
}

func TestUnlinkURLsUndoesChatGPTLinks(t *testing.T) {
	source := `<a href="http://en.wikipedia.org/wiki/Ealing">Ealing</a> <a href="http://en.wikipedia.org/wiki/Realism_(visual_arts)">Realistic</a> [http://a.example/x](http://a.example/x) [Men](../N/Men.md)`
	// As it came back from ChatGPT.
	text := `<a href="[http://en.wikipedia.org/wiki/Ealing](http://en.wikipedia.org/wiki/Ealing)">Ealing</a> <a href="[http://en.wikipedia.org/wiki/Realism\_(visual_arts)](http://en.wikipedia.org/wiki/Realism_\(visual_arts\))">Hiện thực</a> [http://a.example/x](http://a.example/x) [Men](../N/Men.md) [http://b.example](http://c.example)`
	want := `<a href="http://en.wikipedia.org/wiki/Ealing">Ealing</a> <a href="http://en.wikipedia.org/wiki/Realism_(visual_arts)">Hiện thực</a> [http://a.example/x](http://a.example/x) [Men](../N/Men.md) [http://b.example](http://c.example)`
	if got := unlinkURLs(source, text); got != want {
		t.Fatalf("unlinkURLs:\n got %s\nwant %s", got, want)
	}
}

func TestUnescapeMarksKeepsEmphasis(t *testing.T) {
	source := "The *[olvar](../O/Olvar.md)*"
	got := unescapeMarks(source, "Các \\*[olvar](../O/Olvar.md)\\*")
	if want := "Các *[olvar](../O/Olvar.md)*"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := unescapeMarks("a \\_b", "x \\_y \\*z"); got != "x \\_y *z" {
		t.Errorf("got %q", got)
	}
}

func TestUnescapeMarksKeepsLinksAndTags(t *testing.T) {
	source := `In [S.A.](../S/Second_Age.md)<sup id="x"><a href="#n">[2]</a></sup>`
	got := unescapeMarks(source, `Vào [N.A.]\(../S/Second_Age.md)\<sup id="x">\<a href="#n">[2]\</a>\</sup>`)
	if want := `Vào [N.A.](../S/Second_Age.md)<sup id="x"><a href="#n">[2]</a></sup>`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
