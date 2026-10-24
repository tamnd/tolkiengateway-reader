package main

import (
	"strings"
	"testing"
)

func TestRulesApply(t *testing.T) {
	cases := map[string]string{
		"Vào Kỷ nguyên thứ Ba, Chiếc Nhẫn Duy Nhất ở the Shire.": "Vào Kỷ Đệ Tam, Nhẫn Chúa ở Shire.",
		"Bộ phim (phim 1978) hay .":                              "Bộ phim (phim năm 1978) hay.",
		"[Núi Doom](../M/Mount_Doom.md)":                         "[Núi Diệt Vong](../M/Mount_Doom.md)",
		"Kỷ Đệ Tam":                                              "Kỷ Đệ Tam",
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

func TestRulesWorkTitles(t *testing.T) {
	in := "2004: The Lord of the Rings: Trận chiến vì Trung Địa II và The Lord of the Rings Trực tuyến"
	want := "2004: The Lord of the Rings: The Battle for Middle-earth II và The Lord of the Rings Online"
	if got := applyRules(in); got != want {
		t.Errorf("got %q", got)
	}
}

func TestHTMLTranslatableNotElvish(t *testing.T) {
	for _, s := range []string{"mh m", "th s z", "zadan", "bâr", "nê nud"} {
		if htmlTranslatable(s) {
			t.Errorf("%q taken for English", s)
		}
	}
	for _, s := range []string{"Frodo is voiced by Ian Holm.", "(1978 film)", "zadan (neuter) 'house'"} {
		if !htmlTranslatable(s) {
			t.Errorf("%q not taken for English", s)
		}
	}
}

func TestDictSentences(t *testing.T) {
	got := splitSentences("In The Letters of J. R. R. Tolkien, Donald A. Wollheim wrote. Then he left")
	want := []string{"In The Letters of J. R. R. Tolkien, Donald A. Wollheim wrote", "Then he left"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	lower := map[string]int{"king": 9, "ring": 9}
	english := func(w string) bool { return lower[w] >= 5 }
	for term, names := range map[string]bool{"Sauron": true, "Bag End": true, "King of Gondor": false, "Ring-bearer": false} {
		if namesOnly(term, english) != names {
			t.Errorf("namesOnly(%q) = %v", term, !names)
		}
	}
}

func TestGlossaryReplaceLongest(t *testing.T) {
	g := newGlossary(map[string]string{"Mount": "Núi", "Mount Doom": "Núi Diệt Vong", "Doom": "Diệt Vong"})
	got := g.replace("From Mount Doom to Mountains, Mount.", func(_, vi string) string { return "<" + vi + ">" })
	if want := "From <Núi Diệt Vong> to Mountains, <Núi>."; got != want {
		t.Fatalf("got %q", got)
	}
}
