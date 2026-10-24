package main

import (
	"strings"
	"testing"
)

func TestRulesApply(t *testing.T) {
	cases := map[string]string{
		"Vào Kỷ nguyên thứ Ba, Chiếc Nhẫn Duy Nhất ở the Shire.": "Vào Kỷ Đệ Tam, Nhẫn Chúa ở Shire.",
		"Bộ phim (phim 1978) hay .":                               "Bộ phim (phim năm 1978) hay.",
		"[Núi Doom](../M/Mount_Doom.md)":                          "[Núi Diệt Vong](../M/Mount_Doom.md)",
		"Kỷ Đệ Tam":                                               "Kỷ Đệ Tam",
	}
	for in, want := range cases {
		if got := applyRules(in); got != want {
			t.Errorf("applyRules(%q) = %q, want %q", in, got, want)
		}
	}
	// Link targets and tags are left alone.
	in := `<a href="../N/Núi Doom.md" title="Người lùn">x</a>`
	if got := applyRules(in); got != in {
		t.Errorf("rules rewrote a tag: %q", got)
	}
}

func TestMaskHTMLRoundTrip(t *testing.T) {
	span := `He lived at <a href="../B/Bag_End.md" title="Bag End">his old home</a> in <i>The Hobbit</i> &amp; more<sup>[1]</sup>`
	m, err := maskHTML(span, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.text, "href") || strings.Contains(m.text, "The Hobbit") {
		t.Fatalf("not masked: %q", m.text)
	}
	if got, err := m.unmask(m.text); err != nil || got != span {
		t.Errorf("round trip:\n got %q\nwant %q", got, span)
	}
}

func TestHTMLSpans(t *testing.T) {
	block := `<table><tr><th>Other names</th><td><a href="../F/Frodo.md">Frodo</a></td></tr>` +
		`<tr><td>He was born in the <a href="../S/Shire.md">Shire</a>.</td></tr></table>`
	var got []string
	for _, s := range htmlSpans(block) {
		if span := block[s.start:s.end]; htmlTranslatable(span) {
			got = append(got, span)
		}
	}
	want := []string{"Other names", `He was born in the <a href="../S/Shire.md">Shire</a>.`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("spans = %q, want %q", got, want)
	}
}

func TestPhraseFor(t *testing.T) {
	p := phraseTable{"Other names": "Tên khác"}
	if got, ok := p.phraseFor("<b>Other names</b>"); !ok || got != "<b>Tên khác</b>" {
		t.Errorf("phraseFor = %q, %v", got, ok)
	}
	if _, ok := p.phraseFor("Gender"); ok {
		t.Error("phraseFor matched a phrase not in the table")
	}
}
