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
	`Words in *x* and *a b c d e f g h* italics.`,
	`See http://en.wikipedia.org/wiki/Roland and ` + "`code`" + `, a ![map](map.png) and 2*3*4.`,
}

func TestMaskRoundTrips(t *testing.T) {
	for _, md := range maskCases {
		m := maskMarkdown(md, nil)
		got, err := m.unmask(m.text)
		if err != nil || got != md {
			t.Errorf("round trip of %q\nmasked %q\ngot    %q (%v)", md, m.text, got, err)
		}
	}
}

func TestMaskHidesTitlesAndNames(t *testing.T) {
	m := maskMarkdown(maskCases[3], nil)
	if strings.Contains(m.text, "Lord of the Rings") || strings.Contains(m.text, ".md") {
		t.Fatalf("title or target left for the engine: %q", m.text)
	}
	m = maskMarkdown(`for *[The Lord of the Rings: The Fellowship of the Ring](../T/The_Lord_of_the_Rings%253A_The_Fellowship_of_the_Ring.md)*`, nil)
	if strings.Contains(m.text, "Fellowship") {
		t.Fatalf("long linked title left for the engine: %q", m.text)
	}
	m = maskMarkdown(`However, J.R.R. Tolkien himself, in T.A. 3018, met C. S. Lewis.`, nil)
	if strings.Contains(m.text, "Tolkien") || strings.Contains(m.text, "T.A.") || strings.Contains(m.text, "Lewis") {
		t.Fatalf("initials left for the engine: %q", m.text)
	}
	m = maskMarkdown(maskCases[6], nil)
	if !strings.Contains(m.text, "long quotation") {
		t.Fatalf("long italic span hidden: %q", m.text)
	}
}

func TestUnmaskRepairsSpacing(t *testing.T) {
	m := maskMarkdown(maskCases[0], nil)
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
	md := `In [T.A.](../T/Third_Age.md) [3018](../T/Third_Age_3018.md), the [Shire](../S/Shire.md) [fell](../F/Fell.md) [hard](../H/Hard.md) and J.R.R. Tolkien wrote, in C. S. Lewis's words.`
	m := maskMarkdown(md, nil)
	answer := strings.NewReplacer("/> <", "/><", "</a3> <a4>", "</a3><a4>", "</a5> ", "</a5>").Replace(m.text)
	answer = strings.Replace(answer, "/> wrote", "/>wrote", 1)
	md = strings.Replace(md, "Lewis's words", "Lewis words", 1)
	answer = strings.Replace(answer, "/>'s words", "/>words", 1)
	if got, err := m.unmask(answer); err != nil || got != md {
		t.Fatalf("got %q, %v from %q\nmasked %q", got, err, answer, m.text)
	}
}

func TestUnmaskSeparatesMovedWords(t *testing.T) {
	m := maskMarkdown(`invaded by any of the [Free Peoples](../F/Free_Peoples.md). The [Dwarf](../D/Dwarves.md)s and **Elves** came.`, nil)
	// Vietnamese puts "nào" after the link, and a word before the bold.
	answer := strings.NewReplacer("</a1>.", "</a1>nào.", "<b3>", "và<b3>").Replace(m.text)
	got, err := m.unmask(answer)
	want := `invaded by any of the [Free Peoples](../F/Free_Peoples.md) nào. The [Dwarf](../D/Dwarves.md)s and và **Elves** came.`
	if err != nil || got != want {
		t.Fatalf("got %q, %v from %q", got, err, answer)
	}
}

func TestUnmaskReportsTagProblems(t *testing.T) {
	m := maskMarkdown(`made a lord in [Dor-lómin](../D/Dor-lómin.md) by [Morgoth](../M/Morgoth.md)`, nil)
	answer := strings.ReplaceAll(m.text, "a2", "a1")
	if _, err := m.unmask(answer); err == nil || !strings.Contains(err.Error(), "repeated a1") || !strings.Contains(err.Error(), "lost a2") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckNumbers(t *testing.T) {
	src := maskMarkdown(`joined by Stoors around 1630, with 2,000 men in T.A. 3018.<sup id="cite_ref-12">[12]</sup>`, nil).text
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
	d, err := newMTDoc("p", path, "", nil, nil)
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

func TestMaskUsesGlossary(t *testing.T) {
	g := newGlossary(map[string]string{"Men": "Con Người", "Mount Doom": "Núi Diệt Vong", "Doom": "Diệt Vong"})
	m := maskMarkdown(`The [Men](../M/Men.md) of the West reached Mount Doom, but Menelvagor and Doomsday stayed.`, g)
	if strings.Contains(m.text, "Men]") || strings.Contains(m.text, "Mount") || !strings.Contains(m.text, "Menelvagor") || !strings.Contains(m.text, "Doomsday") {
		t.Fatalf("masked %q", m.text)
	}
	got, err := m.unmask(m.text)
	want := `The [Con Người](../M/Men.md) of the West reached Núi Diệt Vong, but Menelvagor and Doomsday stayed.`
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestFillDocTranslatesOnlyEnglish(t *testing.T) {
	dir := t.TempDir()
	en := "---\ntitle: x\n---\nThe Elves came to Middle-earth.\n\nThey left in the Third Age.\n"
	vi := "---\ntitle: x\n---\nTiên tộc đã đến Trung Địa.\n\nThey left in the Third Age.\n"
	if err := atomicWrite(dir+"/en.md", []byte(en)); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(dir+"/vi.md", []byte(vi)); err != nil {
		t.Fatal(err)
	}
	d, err := fillDoc(mtPage{"p", dir + "/en.md", dir + "/vi.md"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.units) != 1 || d.units[0].source != "They left in the Third Age." {
		t.Fatalf("units = %d", len(d.units))
	}
	d.units[0].text = "Họ rời đi vào Kỷ Đệ Tam."
	if text, _ := d.assemble(); text != strings.Replace(vi, "They left in the Third Age.", "Họ rời đi vào Kỷ Đệ Tam.", 1) {
		t.Fatalf("assembled %q", text)
	}
}

func TestParseReview(t *testing.T) {
	got, err := parseReview("```json\n[{\"id\": 1, \"errors\": [{\"cat\": \"terminology\", \"sev\": \"major\", \"en\": \"Second Age\", \"bad\": \"Thứ hai\", \"good\": \"Kỷ Đệ Nhị\"}], \"fix\": \"x\"}]\n```")
	if err != nil || len(got) != 1 || got[0].Errors[0].Good != "Kỷ Đệ Nhị" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestUnmaskSpacesAfterGlossaryLink(t *testing.T) {
	g := newGlossary(map[string]string{"Elves": "Tiên tộc"})
	m := maskMarkdown(`and was succeeded by the [Elves](../E/Elves.md). Then *[Elves](../E/Elves.md)* left.`, g)
	answer := strings.Replace(m.text, "<x2/>.", "<x2/>kế vị.", 1)
	got, err := m.unmask(answer)
	if err != nil || !strings.Contains(got, "(../E/Elves.md) kế vị.") {
		t.Fatalf("got %q, %v from %q", got, err, answer)
	}
}

func TestMaskKeepsWorksAndCitations(t *testing.T) {
	m := maskMarkdown("A character from [The Lord of the Rings: Heroes of Middle-earth](../T/x.md) and [Elves](../E/Elves.md).\n- GoodKnight, Glen H. Editorial Introduction: Fantasy (3).\n- Hyde, Paul Nolan. “Quenti Lambardillion”\n- Gandalf, a wizard of the Istari.", nil)
	for _, hidden := range []string{"Heroes", "Editorial", "Quenti"} {
		if strings.Contains(m.text, hidden) {
			t.Fatalf("%s left for the engine: %q", hidden, m.text)
		}
	}
	if !strings.Contains(m.text, "Elves") || !strings.Contains(m.text, "wizard") {
		t.Fatalf("ordinary text hidden: %q", m.text)
	}
}

func TestPolitePronouns(t *testing.T) {
	got := politePronouns("Anh ấy nói anh ta sẽ đi, cô ấy ở lại. Thanh ấy không đổi.")
	if want := "Ông nói ông sẽ đi, bà ở lại. Thanh ấy không đổi."; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestRestoreTargets(t *testing.T) {
	got := restoreTargets("dì [Jane](Jane_Neave.md) và [Edith](../E/Edith_Tolkien.md)", "dì [Jane](../J/Jane_Neave.md) cùng [Edith](../E/Edith.md)")
	if want := "dì [Jane](Jane_Neave.md) cùng [Edith](../E/Edith_Tolkien.md)"; got != want {
		t.Fatalf("got %q", got)
	}
}
