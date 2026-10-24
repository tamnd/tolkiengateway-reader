package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The rule layer of the quality harness. A model reviewing Vietnamese makes
// the same corrections again and again: Kỷ nguyên thứ Ba for Kỷ Đệ Tam,
// "phim 1978" for "phim năm 1978", "được viết bởi" where Vietnamese says
// "do ... viết". Each such correction, once it is settled, is a rule, and a
// rule costs nothing to run on every page and on every engine's output, and
// always gives the same answer. The model is left with what rules cannot
// decide.
//
// A rule rewrites only the text a reader sees: never a link target, an
// anchor, an HTML tag or a URL.

//go:embed rules/vi.tsv
var defaultRules string

type rule struct {
	kind, from, to, note string
	re                   *regexp.Regexp
}

type ruleSet struct{ rules []*rule }

var (
	rulesOnce *ruleSet
	// protectedText is what rules never touch.
	protectedText = regexp.MustCompile("\\]\\([^)\\s]*(?:\\s+\"[^\"]*\")?\\)|\\{#[^}\\n]*\\}|<[^<>\\n]*>|`[^`\\n]*`|https?://[^\\s<>)\\]]+")
)

// parseRules reads a rules file. Each line is kind, from, to and a note,
// tab-separated. A term is replaced as a whole word, as written; a re is a
// regular expression whose replacement may use $1.
func parseRules(text string) (*ruleSet, error) {
	rs := &ruleSet{}
	for n, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			return nil, fmt.Errorf("rules line %d: want kind, from, to", n+1)
		}
		r := &rule{kind: f[0], from: f[1], to: f[2]}
		if len(f) > 3 {
			r.note = f[3]
		}
		var err error
		switch r.kind {
		case "term":
			r.re, err = regexp.Compile(`(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(r.from) + `($|[^\p{L}\p{N}])`)
			r.to = "${1}" + r.to + "${2}"
		case "re":
			r.re, err = regexp.Compile(r.from)
		default:
			err = fmt.Errorf("unknown kind %q", r.kind)
		}
		if err != nil {
			return nil, fmt.Errorf("rules line %d: %v", n+1, err)
		}
		rs.rules = append(rs.rules, r)
	}
	return rs, nil
}

// builtinRules are the rules tgw ships with.
func builtinRules() *ruleSet {
	if rulesOnce == nil {
		rs, err := parseRules(defaultRules)
		if err != nil {
			panic(err)
		}
		rulesOnce = rs
	}
	return rulesOnce
}

// apply runs every rule over the visible text of a Markdown fragment and
// counts the rewrites of each.
func (rs *ruleSet) apply(text string, hits map[string]int) string {
	if rs == nil || len(rs.rules) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	free := func(s string) string {
		for _, r := range rs.rules {
			// Twice, for matches that share the character between them.
			for range 2 {
				if !r.re.MatchString(s) {
					break
				}
				if hits != nil {
					hits[r.kind+"\t"+r.from] += len(r.re.FindAllStringIndex(s, -1))
				}
				s = r.re.ReplaceAllString(s, r.to)
			}
		}
		return s
	}
	for _, loc := range protectedText.FindAllStringIndex(text, -1) {
		b.WriteString(free(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(free(text[last:]))
	return b.String()
}

// applyRules runs the built-in rules over a fragment.
func applyRules(text string) string { return builtinRules().apply(text, nil) }

// applyPage runs rules over a whole page: its body and its short
// description, not the rest of its front matter.
func (rs *ruleSet) applyPage(page string, hits map[string]int) string {
	head := frontMatter(page)
	if loc := shortLine.FindStringSubmatchIndex(head); loc != nil {
		if v, ok := shortDescription(head); ok {
			if w := rs.apply(v, hits); w != v {
				head = head[:loc[2]] + fmt.Sprintf("%q", w) + head[loc[3]:]
			}
		}
	}
	return head + rs.apply(page[len(frontMatter(page)):], hits)
}

// runRules reports what the rules would change across the corpus, and with
// -fix changes it. With -mine it reads the review log for corrections the
// model made the same way several times, which are rules to add.
func runRules(args []string) error {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	root := fs.String("corpus", corpusRoot(), "corpus repository")
	targetLang := fs.String("target-lang", "vi", "target language directory")
	dirs := fs.String("dirs", "main", "namespace directories or pages under content/<target-lang>, or @file")
	file := fs.String("rules", "", "rules file; defaults to the built-in rules")
	fix := fs.Bool("fix", false, "write the rewritten pages")
	mine := fs.Int("mine", 0, "list corrections the review log made at least this many times, the same way, as candidate rules")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mine > 0 {
		return mineRules(filepath.Join(*root, ".tgw", "review.jsonl"), *mine)
	}
	rs := builtinRules()
	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		if rs, err = parseRules(string(data)); err != nil {
			return err
		}
	}
	targets, err := translateTargets(*dirs)
	if err != nil {
		return err
	}
	base := filepath.Join(*root, "content", *targetLang)
	hits := map[string]int{}
	pages, changed := 0, 0
	var logFile *os.File
	if *fix {
		if logFile, err = os.OpenFile(filepath.Join(*root, ".tgw", "rules.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return err
		}
		defer logFile.Close()
	}
	for _, t := range targets {
		err := filepath.WalkDir(filepath.Join(base, strings.TrimSpace(t)), func(path string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || filepath.Ext(path) != ".md" {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			pages++
			page := map[string]int{}
			text := rs.applyPage(string(data), page)
			if text == string(data) {
				return nil
			}
			changed++
			for k, n := range page {
				hits[k] += n
			}
			if !*fix {
				return nil
			}
			if err := atomicWrite(path, []byte(text)); err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, path)
			line, _ := json.Marshal(map[string]any{"page": filepath.ToSlash(rel), "hits": page, "at": time.Now().UTC().Format(time.RFC3339)})
			fmt.Fprintln(logFile, string(line))
			return nil
		})
		if err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return hits[keys[i]] > hits[keys[j]] })
	verb := "would change"
	if *fix {
		verb = "changed"
	}
	fmt.Printf("rules: %d rules %s %d of %d pages\n", len(rs.rules), verb, changed, pages)
	for _, k := range keys {
		fmt.Printf("%8d  %s\n", hits[k], strings.Replace(k, "\t", "  ", 1))
	}
	return nil
}

// mineRules lists the corrections in the review log that were made at
// least min times and always the same way.
func mineRules(path string, min int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	type key struct{ cat, bad string }
	goods := map[key]map[string]int{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<24)
	for s.Scan() {
		var it reviewItem
		if json.Unmarshal(s.Bytes(), &it) != nil || it.Verdict == nil {
			continue
		}
		for _, e := range it.Verdict.Errors {
			bad, good := strings.TrimSpace(e.Bad), strings.TrimSpace(e.Good)
			// Only short rewrites that VI really had generalise.
			if bad == "" || good == "" || len([]rune(bad)) > 40 || !strings.Contains(it.VI, bad) {
				continue
			}
			k := key{e.Cat, bad}
			if goods[k] == nil {
				goods[k] = map[string]int{}
			}
			goods[k][good]++
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	type cand struct {
		k     key
		good  string
		n, of int
	}
	var out []cand
	for k, g := range goods {
		total, best, top := 0, "", 0
		for good, n := range g {
			total += n
			if n > top {
				best, top = good, n
			}
		}
		// Consistent: the usual fix is at least four in five.
		if top >= min && top*5 >= total*4 {
			out = append(out, cand{k, best, top, total})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n > out[j].n })
	for _, c := range out {
		fmt.Printf("term\t%s\t%s\t%s, %d of %d\n", c.k.bad, c.good, c.k.cat, c.n, c.of)
	}
	return nil
}
