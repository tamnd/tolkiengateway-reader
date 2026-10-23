package main

import (
	"strings"
	"testing"
)

var maskCases = []string{
	`**Ethir Anduin**, also known as the **Mouths of Anduin**,<sup id="cite_ref-1" class="reference"><a href="#cite_note-1">[1]</a></sup> was the delta of the [Anduin](../A/Anduin.md).`,
	`## Etymology {#Etymology}`,
	`*firien holt* means "mountain wood", though it is more often called the <i><b>Firien Wood</b></i>.`,
	`In [Appendix F](../A/Appendix_F.md) to *[The Lord of the Rings](../T/The_Lord_of_the_Rings.md)* he explains it.`,
	`* [Easterling](<../E/Easterlings_(First_Age).md>) lord of [Dor-lómin](../D/Dor-lómin.md "Dor-lómin")`,
	`| Name | Meaning |` + "\n" + `|---|---|` + "\n" + `| [Celeb](../C/Celeb.md) | silver |`,
	`> *A long quotation in italics that runs well past six words and is translated.*`,
	`See http://en.wikipedia.org/wiki/Roland and ` + "`code`" + `, a ![map](map.png) and 2*3*4.`,
}

func TestMaskRoundTrips(t *testing.T) {
	for _, md := range maskCases {
		m := maskMarkdown(md)
		got, err := m.unmask(m.text)
		if err != nil || got != md {
			t.Errorf("round trip of %q\nmasked %q\ngot    %q (%v)", md, m.text, got, err)
		}
	}
}

func TestMaskHidesTitlesAndNames(t *testing.T) {
	m := maskMarkdown(maskCases[3])
	if strings.Contains(m.text, "Lord of the Rings") || strings.Contains(m.text, ".md") {
		t.Fatalf("title or target left for the engine: %q", m.text)
	}
	m = maskMarkdown(`for *[The Lord of the Rings: The Fellowship of the Ring](../T/The_Lord_of_the_Rings%253A_The_Fellowship_of_the_Ring.md)*`)
	if strings.Contains(m.text, "Fellowship") {
		t.Fatalf("long linked title left for the engine: %q", m.text)
	}
	m = maskMarkdown(`However, J.R.R. Tolkien himself, in T.A. 3018, met C. S. Lewis.`)
	if strings.Contains(m.text, "Tolkien") || strings.Contains(m.text, "T.A.") || strings.Contains(m.text, "Lewis") {
		t.Fatalf("initials left for the engine: %q", m.text)
	}
	m = maskMarkdown(maskCases[6])
	if !strings.Contains(m.text, "long quotation") {
		t.Fatalf("long italic span hidden: %q", m.text)
	}
}

func TestUnmaskRepairsSpacing(t *testing.T) {
	m := maskMarkdown(maskCases[0])
	// What Google does: spaces inside pairs and before a citation.
	answer := strings.ReplaceAll(m.text, "<b1>", "<b1> ")
	answer = strings.ReplaceAll(answer, ",<x3/>", ", <x3/>")
	got, err := m.unmask(answer)
	if err != nil {
		t.Fatal(err)
	}
	if got != maskCases[0] {
		t.Fatalf("got %q", got)
	}
}

func TestUnmaskKeepsSpaceBetweenTags(t *testing.T) {
	md := `In [T.A.](../T/Third_Age.md) [3018](../T/Third_Age_3018.md), the [Shire](../S/Shire.md) [fell](../F/Fell.md).`
	m := maskMarkdown(md)
	answer := strings.NewReplacer("/> <", "/><", "</a3> <a4>", "</a3><a4>").Replace(m.text)
	if got, err := m.unmask(answer); err != nil || got != md {
		t.Fatalf("got %q, %v from %q", got, err, answer)
	}
}

func TestUnmaskReportsTagProblems(t *testing.T) {
	m := maskMarkdown(`made a lord in [Dor-lómin](../D/Dor-lómin.md) by [Morgoth](../M/Morgoth.md)`)
	answer := strings.ReplaceAll(m.text, "a2", "a1")
	if _, err := m.unmask(answer); err == nil || !strings.Contains(err.Error(), "repeated a1") || !strings.Contains(err.Error(), "lost a2") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckNumbers(t *testing.T) {
	src := maskMarkdown(`joined by Stoors around 1630, with 2,000 men in T.A. 3018.<sup id="cite_ref-12">[12]</sup>`).text
	for answer, ok := range map[string]bool{
		"cùng người Stoors vào khoảng năm 1630, với 2.000 người vào T.A. 3018.<x1/>": true,
		"cùng người Stoors vào khoảng 16:30, với 2.000 người vào T.A. 3018.<x1/>":    false,
		"cùng người Stoors vào khoảng 1630, với hai nghìn người vào T.A. 3018.<x1/>": false,
	} {
		if err := checkNumbers(src, answer); (err == nil) != ok {
			t.Errorf("%q: err = %v", answer, err)
		}
	}
}

func TestParseGoogle(t *testing.T) {
	got, err := parseGoogle([]byte(`["Ngọn núi <b1>Cô Đơn</b1>","Xin chào"]`), 2)
	if err != nil || got[1] != "Xin chào" || !strings.Contains(got[0], "<b1>") {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := parseGoogle([]byte(`"Xin chào"`), 1); err != nil || got[0] != "Xin chào" {
		t.Fatalf("single: got %q, %v", got, err)
	}
	if got, err := parseGoogle([]byte(`[["Xin chào","en"]]`), 1); err != nil || got[0] != "Xin chào" {
		t.Fatalf("auto: got %q, %v", got, err)
	}
}

func TestMTDocAssembles(t *testing.T) {
	d := &mtDoc{source: "---\ntitle: x\n---\n" + sampleBody}
	path := t.TempDir() + "/p.md"
	if err := atomicWrite(path, []byte(d.source)); err != nil {
		t.Fatal(err)
	}
	d, err := newMTDoc("p", path, "")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing translated: the page comes back as it was.
	if text, failed := d.assemble(); text != d.source || failed != len(d.units) {
		t.Fatalf("assemble = %q, %d failed", text, failed)
	}
	for _, u := range d.units {
		u.answer = u.m.text
		if err := u.check(); err != nil {
			t.Fatalf("%q: %v", u.source, err)
		}
	}
	if text, failed := d.assemble(); text != d.source || failed != 0 {
		t.Fatalf("assemble = %q, %d failed", text, failed)
	}
}
