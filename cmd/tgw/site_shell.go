package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// htmlClass is the root element's class list before any script runs. The
// shared chrome script swaps client-nojs for client-js and applies the
// reader's saved preferences before the first paint.
const htmlClass = "client-nojs skin-citizen-auto skin-theme-clientpref-os citizen-feature-autohide-navigation-clientpref-1 " +
	"citizen-feature-pure-black-clientpref-0 citizen-feature-custom-font-size-clientpref-standard citizen-feature-custom-width-clientpref-standard"

// pageScript is the per-page data the shared chrome needs to draw the page
// actions: the title, revision, talk page, namespace tab and languages.
type pageScript struct {
	Title    string    `json:"t"`
	NS       int       `json:"ns"`
	Revision int       `json:"r,omitempty"`
	Talk     string    `json:"k,omitempty"`
	Tab      [2]string `json:"n"`
	Langs    any       `json:"l,omitempty"`
}

// document wraps a rendered page in Citizen's page shell. The parts every
// page shares are drawn by skin/chrome.js, so each page carries only what
// is its own.
func (s *site) document(p *sitePage, r renderedPage) string {
	m := p.Meta
	ns, name := m.Namespace, strings.TrimPrefix(m.Title, namespacePrefix(m.Namespace))
	var b strings.Builder
	b.Grow(len(r.content) + len(r.toc) + 8192)

	ps := pageScript{Title: m.Title, NS: ns, Revision: m.Revision, Tab: namespaceTab(ns)}
	ps.Tab[1] = s.ui(ps.Tab[1])
	if ns >= 0 {
		ps.Talk = talkTitle(ns, name)
	}
	if len(m.LangLinks) > 0 {
		ps.Langs = m.LangLinks
	}
	config, _ := json.Marshal(ps)

	description := m.ShortDescription
	b.WriteString("<!DOCTYPE html>\n<html class=\"" + htmlClass + "\" lang=\"" + s.lang + "\" dir=\"ltr\">\n<head>\n<meta charset=\"UTF-8\">\n")
	b.WriteString("<title>" + htmlEscape(m.Title) + " - Tolkien Gateway</title>\n")
	b.WriteString("<script>var TGW_PAGE=" + strings.ReplaceAll(string(config), "</", `<\/`) + "</script>\n")
	b.WriteString(`<script src="` + s.base + `/skin/chrome.js"></script>` + "\n")
	b.WriteString(`<link rel="stylesheet" href="` + s.assets + `/skin/skin.css">` + "\n")
	b.WriteString(`<link rel="stylesheet" href="` + s.assets + `/skin/tgw.css">` + "\n")
	if s.templateStyles {
		b.WriteString(`<link rel="stylesheet" href="` + s.assets + `/skin/templatestyles.css">` + "\n")
	}
	if m.NoTitle {
		b.WriteString("<style>.firstHeading,.subtitle,#siteSub,#contentSub,.pagetitle{display:none}</style>\n")
	}
	b.WriteString(`<meta name="theme-color" content="#0d0e12">` + "\n")
	if description != "" {
		b.WriteString(`<meta name="description" content="` + htmlEscape(description) + `">` + "\n")
	}
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1.0, user-scalable=yes, minimum-scale=0.25, maximum-scale=5.0">` + "\n")
	b.WriteString(`<link rel="apple-touch-icon" href="` + s.assets + `/skin/assets/favicon/apple-touch-icon.png">` + "\n")
	b.WriteString(`<link rel="icon" href="` + s.assets + `/skin/assets/favicon/favicon.ico">` + "\n")
	if ns >= 0 {
		b.WriteString(`<link rel="canonical" href="` + htmlEscape(remoteURL(m.Title)) + `">` + "\n")
	}
	b.WriteString(`<link rel="license" href="https://creativecommons.org/licenses/by-sa/4.0/">` + "\n")
	b.WriteString(`<meta property="og:title" content="` + htmlEscape(m.Title) + `">` + "\n")
	b.WriteString(`<meta property="og:site_name" content="Tolkien Gateway">` + "\n")
	if m.Timestamp != "" {
		b.WriteString(`<meta property="article:modified_time" content="` + htmlEscape(m.Timestamp) + `">` + "\n")
	}
	b.WriteString("</head>\n")

	fmt.Fprintf(&b, `<body class="%s"><script>tgwHeader()</script>`+"\n", bodyClass(ns, m.Title))
	b.WriteString(`<div class="citizen-page-container"><div class="citizen-sitenotice-container"><div id="siteNotice"></div></div>` + "\n")
	mainClass := "mw-body"
	if r.toc != "" {
		mainClass += " citizen-toc-enabled"
	}
	b.WriteString(`<main class="` + mainClass + `" id="content"><header class="mw-body-header citizen-page-header" id="citizen-page-header"><div class="citizen-page-header-inner"><div class="citizen-page-heading"><div class="firstHeading-container">`)
	b.WriteString(`<h1 id="firstHeading" class="firstHeading mw-first-heading">` + pageHeading(m) + `</h1>`)
	b.WriteString(`<div class="mw-indicators">` + s.pageIndicators(ns) + "</div></div>")
	b.WriteString(`<div id="siteSub">` + htmlEscape(s.tagline(m)) + `</div><a href="#top" class="citizen-jumptotop" title="` + s.ui("Back to top") + `"></a></div>`)
	b.WriteString(`<div class="page-actions"><script>tgwActions()</script></div></div></header>` + "\n")
	b.WriteString(`<div id="citizen-page-header-sticky-placeholder"></div><div id="citizen-page-header-sticky-sentinel"></div>` + "\n")
	b.WriteString(`<div class="citizen-body-container"><div id="bodyContent" class="citizen-body" aria-labelledby="firstHeading"><div id="contentSub"><div id="mw-content-subtitle">` + s.subtitle(p) + `</div></div>` + "\n")
	if ns == 6 {
		b.WriteString(`<div id="mw-content-text" class="mw-body-content">`)
	} else {
		contentLang := s.lang
		if r.untranslated {
			contentLang = "en"
		}
		b.WriteString(`<div id="mw-content-text" class="mw-body-content mw-content-ltr" lang="` + contentLang + `" dir="ltr">`)
	}
	if r.untranslated {
		b.WriteString(`<div class="mw-message-box mw-message-box-warning tgw-untranslated" lang="` + s.lang + `"><p>` + s.ui("This page has not been translated into Vietnamese yet. It is shown in English.") + `</p></div>`)
	}
	b.WriteString(r.content)
	if r.borrow != "" {
		b.WriteString(`<div class="tgw-borrow" data-src="` + htmlEscape(r.borrow) + `"><p><a href="` + htmlEscape(r.borrow) + `" lang="en">` + htmlEscape(m.Title) + `</a></p></div>`)
	}
	if m.Revision > 0 {
		permalink := htmlEscape(permanentLink(m.Title, m.Revision))
		b.WriteString(`<div class="printfooter" data-nosnippet="">` + s.uif("Retrieved from \"%s\"", `<a dir="ltr" href="`+permalink+`">`+permalink+`</a>`) + `</div>`)
	}
	b.WriteString("</div></div>\n")

	b.WriteString(`<div class="citizen-page-sidebar">`)
	if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
		fmt.Fprintf(&b, `<nav id="citizen-sidebar-lastmod" class="citizen-menu "><div class="citizen-menu__heading">%s</div><div class="citizen-menu__content"><ul class="citizen-menu__content-list">`+
			`<li id="lm-time" class="mw-list-item"><a data-mw="interface" id="citizen-lastmod-relative" href="%s" title="%s" data-timestamp="%d"><span class="citizen-ui-icon mw-ui-icon-history mw-ui-icon-wikimedia-history"></span><span>%s</span></a></li></ul></div></nav>`,
			s.ui("Last modified"), htmlEscape(indexURL(m.Title, "diff="+strconv.Itoa(m.Revision))), htmlEscape(s.uif("This page was last edited on %s.", s.dateTime(t))), t.Unix(), s.relativeTime(t, s.built))
	}
	b.WriteString(r.toc)
	b.WriteString("</div>\n")

	b.WriteString(`<footer class="citizen-page-footer">`)
	if ns >= 0 && r.borrow == "" {
		b.WriteString(s.catlinks(m))
	}
	b.WriteString(`<div class="page-info">`)
	if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
		b.WriteString(`<section id="footer-info-lastmod" class="page-info__item"><div class="page-info__label">` + s.ui("Last modified") + `</div><div class="page-info__text"> ` + s.uif("This page was last edited on %s.", s.dateTime(t)) + `</div></section>`)
	}
	b.WriteString(`<section id="footer-info-copyright" class="page-info__item"><div class="page-info__label">` + s.ui("Copyright") + `</div><div class="page-info__text">` + s.uif("Content is available under %s unless otherwise noted.", `<a href="`+
		htmlEscape(s.titleURL("Tolkien Gateway:Copyrights", ""))+`" title="Tolkien Gateway:Copyrights">`+s.ui("Creative Commons Attribution-ShareAlike 4.0 License")+`</a>`) + `</div></section>`)
	if ns >= 0 && m.Revision > 0 {
		b.WriteString(`<section id="footer-info-source" class="page-info__item"><div class="page-info__label">` + s.ui("Source") + `</div><div class="page-info__text">` + s.uif("A static copy of %s on Tolkien Gateway, as of %s.",
			`<a href="`+htmlEscape(remoteURL(m.Title))+`">`+htmlEscape(m.Title)+`</a>`, `<a href="`+htmlEscape(permanentLink(m.Title, m.Revision))+`">`+s.uif("revision %d", m.Revision)+`</a>`) + `</div></section>`)
	}
	b.WriteString("</div></footer></div></main>\n")
	b.WriteString(`<div id="mw-data-after-content"><div class="read-more-container"></div></div>` + "\n")
	b.WriteString("<script>tgwFooter()</script></div>\n")
	b.WriteString(`<script src="` + s.assets + `/skin/tgw.js" defer></script>` + "\n")
	b.WriteString(`<div id="mw-teleport-target"></div></body></html>` + "\n")
	return b.String()
}

// pageHeading is the h1 content: the display title when the page sets one,
// otherwise the namespace and name in MediaWiki's title spans.
func pageHeading(m pageMeta) string {
	if m.DisplayTitle != "" {
		return m.DisplayTitle
	}
	prefix := namespacePrefix(m.Namespace)
	if prefix == "" || m.Namespace < 0 {
		return `<span class="mw-page-title-main">` + htmlEscape(m.Title) + `</span>`
	}
	return `<span class="mw-page-title-namespace">` + htmlEscape(strings.TrimSuffix(prefix, ":")) + `</span><span class="mw-page-title-separator">:</span>` +
		`<span class="mw-page-title-main">` + htmlEscape(strings.TrimPrefix(m.Title, prefix)) + `</span>`
}

func (s *site) pageIndicators(ns int) string {
	help := map[int]string{14: "Help:Categories", 6: "Help:Managing_files"}[ns]
	if help == "" {
		return ""
	}
	return `<div id="mw-indicator-mw-helplink" class="mw-indicator"><a href="https://www.mediawiki.org/wiki/Special:MyLanguage/` + help +
		`" target="_blank" class="mw-helplink">` + s.ui("Help") + `</a></div>`
}

// tagline is Citizen's line under the title: the short description, or a
// message naming what kind of page this is.
func (s *site) tagline(m pageMeta) string {
	if m.ShortDescription != "" {
		return m.ShortDescription
	}
	switch m.Namespace {
	case 6:
		return s.ui("File on Tolkien Gateway")
	case 14:
		return s.ui("Category page")
	case 4:
		return s.ui("Information about Tolkien Gateway")
	case 12:
		return s.ui("Help page")
	case -1:
		return s.ui("Special page")
	}
	return s.ui("From Tolkien Gateway")
}

// subtitle is the line under the heading: the breadcrumb of parent pages
// for subpages in namespaces that have them.
func (s *site) subtitle(p *sitePage) string {
	m := p.Meta
	if m.Namespace == 0 || !strings.Contains(m.Title, "/") {
		return ""
	}
	parts := strings.Split(m.Title, "/")
	var crumbs []string
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		if s.byTitle[parent] == nil {
			continue
		}
		label := parts[i-1]
		if i == 1 {
			label = parent
		}
		crumbs = append(crumbs, `<a href="`+htmlEscape(s.titleURL(parent, ""))+`" title="`+htmlEscape(parent)+`">`+htmlEscape(label)+`</a>`)
	}
	if len(crumbs) == 0 {
		return ""
	}
	return `<div class="subpages">&lt; ` + strings.Join(crumbs, "\u200e | ") + `</div>`
}

// catlinks is the category box under the content, as MediaWiki writes it.
func (s *site) catlinks(m pageMeta) string {
	list := func(cats []string) string {
		var b strings.Builder
		b.WriteString("<ul>")
		for _, c := range cats {
			title := "Category:" + c
			b.WriteString(`<li><a href="` + htmlEscape(s.titleURL(title, "")) + `" title="` + htmlEscape(title) + `">` + htmlEscape(c) + `</a></li>`)
		}
		b.WriteString("</ul>")
		return b.String()
	}
	class := "catlinks"
	if len(m.Categories) == 0 {
		class += " catlinks-allhidden"
	}
	var b strings.Builder
	b.WriteString(`<div id="catlinks" class="` + class + `" data-mw="interface">`)
	if n := len(m.Categories); n > 0 {
		label := "Categories"
		if n == 1 {
			label = "Category"
		}
		b.WriteString(`<div id="mw-normal-catlinks" class="mw-normal-catlinks"><a href="` + wikiOrigin + `/wiki/Special:Categories" title="Special:Categories">` + s.ui(label) + `</a>: ` + list(m.Categories) + `</div>`)
	}
	if n := len(m.HiddenCategories); n > 0 {
		label := "Hidden categories:"
		if n == 1 {
			label = "Hidden category:"
		}
		b.WriteString(`<div id="mw-hidden-catlinks" class="mw-hidden-catlinks mw-hidden-cats-hidden">` + s.ui(label) + ` ` + list(m.HiddenCategories) + `</div>`)
	}
	b.WriteString("</div>")
	return b.String()
}

// bodyClass is the class list MediaWiki puts on body, which site styles
// key on, the main page's above all.
func bodyClass(ns int, title string) string {
	kind := "ns-subject"
	if ns < 0 {
		kind = "ns-special"
	} else if ns%2 == 1 {
		kind = "ns-talk"
	}
	root := title
	if ns != 0 {
		root, _, _ = strings.Cut(title, "/")
	}
	return fmt.Sprintf("citizen-sections-enabled mediawiki ltr sitedir-ltr mw-hide-empty-elt ns-%d %s page-%s rootpage-%s skin-citizen action-view skin--responsive",
		ns, kind, escapeClass(title), escapeClass(root))
}

var (
	classUnsafeRE = regexp.MustCompile("(^[0-9-])|[\\x00-\\x20!\"#$%&'()*+,./:;<=>?@\\[\\]^`{|}~]| ")
	underscoresRE = regexp.MustCompile("_+")
)

// escapeClass is Sanitizer::escapeClass.
func escapeClass(s string) string {
	s = classUnsafeRE.ReplaceAllString(strings.ReplaceAll(s, " ", "_"), "_")
	return strings.Trim(underscoresRE.ReplaceAllString(s, "_"), "_")
}

// namespaceTab is the id and label of the subject tab in the page actions.
func namespaceTab(ns int) [2]string {
	switch ns {
	case 0:
		return [2]string{"ca-nstab-main", "Page"}
	case 4:
		return [2]string{"ca-nstab-project", "Project page"}
	case 6:
		return [2]string{"ca-nstab-image", "File"}
	case 12:
		return [2]string{"ca-nstab-help", "Help page"}
	case 14:
		return [2]string{"ca-nstab-category", "Category"}
	case -1:
		return [2]string{"ca-nstab-special", "Special page"}
	}
	return [2]string{"ca-nstab-" + strings.ToLower(strings.ReplaceAll(namespaceNames[ns], " ", "_")), namespaceNames[ns]}
}

func talkTitle(ns int, name string) string {
	if ns == 0 {
		return "Talk:" + name
	}
	if talk, ok := namespaceNames[ns+1]; ok && ns%2 == 0 {
		return talk + ":" + name
	}
	return ""
}

// indexURL is a /w/index.php URL on the live wiki for a title.
func indexURL(title, query string) string {
	u := wikiOrigin + "/w/index.php?title=" + wikiEscape(strings.ReplaceAll(title, " ", "_"))
	if query != "" {
		u += "&" + query
	}
	return u
}

func permanentLink(title string, revision int) string {
	return indexURL(title, "oldid="+strconv.Itoa(revision))
}

// lastEdited formats a time as the wiki's footer does: "1 August 2026, at
// 20:11". The wiki runs on UTC.
func lastEdited(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%d %s %d, at %s", t.Day(), t.Month(), t.Year(), t.Format("15:04"))
}

// historyTime is the file history's "20:53, 7 August 2022".
func historyTime(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%s, %d %s %d", t.Format("15:04"), t.Day(), t.Month(), t.Year())
}

// relativeTime is what the sidebar shows until the script recomputes it in
// the reader's own time: "3 months ago".
func relativeTime(t, now time.Time) string {
	d := now.Sub(t)
	units := []struct {
		name string
		size time.Duration
	}{
		{"year", 365 * 24 * time.Hour}, {"month", 30 * 24 * time.Hour}, {"week", 7 * 24 * time.Hour},
		{"day", 24 * time.Hour}, {"hour", time.Hour}, {"minute", time.Minute},
	}
	for _, u := range units {
		if n := int(d / u.size); n >= 1 {
			if n == 1 {
				if u.name == "day" {
					return "yesterday"
				}
				return "1 " + u.name + " ago"
			}
			return fmt.Sprintf("%d %ss ago", n, u.name)
		}
	}
	return "now"
}

// formatNum groups digits as the wiki's English interface does.
func formatNum(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// formatSize is Language::formatSize: whole kilobytes, and two decimals
// from megabytes up.
func formatSize(size int64) string {
	if size < 1024 {
		return formatNum(size) + " bytes"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(size) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return formatNum(int64(math.Round(v))) + " KB"
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".") + " " + units[i]
}

// compactNum is the drawer's site statistics format: "13.6K".
func compactNum(n int) string {
	switch {
	case n >= 1e6:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64), ".0") + "M"
	case n >= 1e3:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64), ".0") + "K"
	}
	return strconv.Itoa(n)
}

// filePageContent is what ImagePage puts around a file description: the
// preview with its other resolutions, the original's details, the history
// and the pages that use it.
func (s *site) filePageContent(p *sitePage, parserOutput string) string {
	m := p.Meta
	name := strings.TrimPrefix(m.Title, "File:")
	var b strings.Builder
	b.WriteString(`<ul id="filetoc"><li><a href="#file">` + s.ui("File") + `</a></li>` + "\n" + `<li><a href="#filehistory">` + s.ui("File history") + `</a></li>` + "\n" + `<li><a href="#filelinks">` + s.ui("File usage") + `</a></li></ul>`)
	f := m.File
	if f != nil {
		original := s.imageURL(f.URL)
		rel, _ := imagePath(f.URL)
		mime := `<span class="mime-type">` + htmlEscape(f.Mime) + `</span>`
		info := s.uif("file size: %s, MIME type: %s", s.size(f.Size), mime)
		if f.Width > 0 && f.Height > 0 {
			info = s.uif("%s × %s pixels, file size: %s, MIME type: %s", s.num(int64(f.Width)), s.num(int64(f.Height)), s.size(f.Size), mime)
		}
		label := s.ui("Original file")
		if thumbnailable[f.Mime] && f.Width > 0 && f.Height > 0 && rel != "" {
			preview := filePreview(rel, f.Mime, f.Width, f.Height)
			fmt.Fprintf(&b, `<div class="fullImageLink" id="file"><a href="%s"><img alt="%s" src="%s" decoding="async" loading="lazy" width="%d" height="%d"%s data-file-width="%d" data-file-height="%d"></a>`,
				htmlEscape(original), htmlEscape(m.Title), htmlEscape(s.imageURL("/w/images/"+preview.rel)), preview.width, preview.height,
				s.densitySrcset(rel, f, preview), f.Width, f.Height)
			if preview.rel != rel {
				b.WriteString(s.otherResolutions(rel, f, preview))
			} else {
				label = name
			}
			b.WriteString("</div>\n")
		} else {
			label = name
		}
		fmt.Fprintf(&b, `<div class="fullMedia"><p><a href="%s" class="internal" title="%s">%s</a> &lrm;<span class="fileInfo">(%s)</span>`+"\n"+`</p></div>`,
			htmlEscape(original), htmlEscape(name), htmlEscape(label), info)
	}
	b.WriteString(`<div id="mw-imagepage-content" lang="en" dir="ltr" class="mw-content-ltr">` + parserOutput + "</div>")

	if f != nil {
		b.WriteString(`<h2 id="filehistory">` + s.ui("File history") + `</h2>` + "\n" + `<div id="mw-imagepage-section-filehistory">` + "\n" +
			`<p>` + s.ui("Click on a date/time to view the file as it appeared at that time.") + "\n" + `</p>` + "\n" +
			`<div class="citizen-overflow-wrapper"><div class="citizen-overflow-content"><table class="wikitable filehistory">` + "\n" +
			`<tbody><tr><th></th><th>` + s.ui("Date/Time") + `</th><th>` + s.ui("Thumbnail") + `</th><th>` + s.ui("Dimensions") + `</th><th>` + s.ui("User") + `</th><th>` + s.ui("Comment") + `</th></tr>` + "\n")
		when := f.Timestamp
		if t, err := time.Parse(time.RFC3339, f.Timestamp); err == nil {
			when = s.historyTime(t)
		}
		original := htmlEscape(s.imageURL(f.URL))
		thumb := ""
		if rel, ok := imagePath(f.URL); ok && thumbnailable[f.Mime] && f.Width > 0 && f.Height > 0 {
			g := galleryThumb(rel, f.Mime, f.Width, f.Height)
			thumb = fmt.Sprintf(`<a href="%s"><img alt="%s" src="%s" decoding="async" loading="lazy" width="%d" height="%d" data-file-width="%d" data-file-height="%d"></a>`,
				original, htmlEscape(s.uif("Thumbnail for version as of %s", when)), htmlEscape(s.imageURL("/w/images/"+g.rel)), g.width, g.height, f.Width, f.Height)
		}
		dims := ""
		if f.Width > 0 && f.Height > 0 {
			dims = fmt.Sprintf("%s × %s ", s.num(int64(f.Width)), s.num(int64(f.Height)))
		}
		user := ""
		if f.User != "" {
			u := htmlEscape(f.User)
			user = fmt.Sprintf(`<a href="%s" class="mw-userlink" title="User:%s"><bdi>%s</bdi></a><span style="white-space: nowrap;"> <span class="mw-usertoollinks">(<a href="%s" class="mw-usertoollinks-talk" title="User talk:%s">`+s.ui("talk")+`</a> | <a href="%s" class="mw-usertoollinks-contribs" title="Special:Contributions/%s">`+s.ui("contribs")+`</a>)</span></span>`,
				htmlEscape(remoteURL("User:"+f.User)), u, u, htmlEscape(remoteURL("User talk:"+f.User)), u, htmlEscape(remoteURL("Special:Contributions/"+f.User)), u)
		}
		fmt.Fprintf(&b, `<tr><td>`+s.ui("current")+`</td><td class="filehistory-selected" style="white-space: nowrap;"><a href="%s">%s</a></td><td>%s</td><td>%s<span style="white-space: nowrap;">(%s)</span></td><td>%s</td><td dir="ltr"></td></tr>`+"\n",
			original, htmlEscape(when), thumb, dims, s.size(f.Size), user)
		b.WriteString("</tbody></table></div></div>\n\n</div>\n")
	}

	b.WriteString(`<h2 id="filelinks">` + s.ui("File usage") + `</h2>` + "\n")
	users := s.fileUsage[strings.ReplaceAll(name, " ", "_")]
	if len(users) == 0 {
		b.WriteString(`<div id="mw-imagepage-nolinkstoimage">` + "\n" + `<p>` + s.ui("There are no pages that use this file.") + "\n" + `</p>` + "\n" + `</div>`)
		return b.String()
	}
	sorted := append([]*sitePage(nil), users...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Meta.Namespace != sorted[j].Meta.Namespace {
			return sorted[i].Meta.Namespace < sorted[j].Meta.Namespace
		}
		return sorted[i].Meta.Title < sorted[j].Meta.Title
	})
	intro := s.ui("The following page uses this file:")
	if len(sorted) > 1 {
		intro = s.uif("The following %s pages use this file:", s.num(int64(len(sorted))))
	}
	b.WriteString(`<div id="mw-imagepage-section-linkstoimage">` + "\n<p>" + intro + "\n</p><ul class=\"mw-imagepage-linkstoimage\">\n")
	for _, u := range sorted {
		fmt.Fprintf(&b, `<li class="mw-imagepage-linkstoimage-ns%d"><a href="%s" title="%s">%s</a></li>`+"\n",
			u.Meta.Namespace, htmlEscape(s.pageURL(u)), htmlEscape(u.Meta.Title), htmlEscape(u.Meta.Title))
	}
	b.WriteString("</ul>\n</div>\n")
	return b.String()
}

// densitySrcset is the preview's 1.5x and 2x candidates. They are only
// offered when the preview itself is hotlinked: a bundled preview is not
// swapped for a sharper copy from the wiki.
func (s *site) densitySrcset(rel string, f *fileMeta, preview thumbSpec) string {
	if preview.rel == rel || s.images.has(preview.rel) {
		return ""
	}
	var parts []string
	for _, d := range []struct {
		factor float64
		label  string
	}{{1.5, "1.5x"}, {2, "2x"}} {
		w := int(math.Round(float64(preview.width) * d.factor))
		if w >= f.Width && f.Mime != "image/svg+xml" {
			break
		}
		t := scaledImage(rel, f.Mime, f.Width, f.Height, w)
		parts = append(parts, s.imageURL("/w/images/"+t.rel)+" "+d.label)
	}
	if len(parts) == 0 {
		return ""
	}
	return ` srcset="` + htmlEscape(strings.Join(parts, ", ")) + `"`
}

// imageLimits are $wgImageLimits, the sizes a file page offers.
var imageLimits = [][2]int{{320, 240}, {640, 480}, {800, 600}, {1024, 768}, {1280, 1024}, {2560, 2048}}

func (s *site) otherResolutions(rel string, f *fileMeta, preview thumbSpec) string {
	link := func(t thumbSpec) string {
		return `<a href="` + htmlEscape(s.imageURL("/w/images/"+t.rel)) + `" class="mw-thumbnail-link">` + s.uif("%s × %s pixels", s.num(int64(t.width)), s.num(int64(t.height))) + `</a>`
	}
	var b strings.Builder
	b.WriteString(`<div class="mw-filepage-resolutioninfo">` + s.uif("Size of this preview: %s.", link(preview)) + ` `)
	var others []string
	for _, limit := range imageLimits {
		w := fitBox(f.Width, f.Height, limit[0], limit[1])
		if w >= f.Width || w == preview.width {
			continue
		}
		others = append(others, link(scaledImage(rel, f.Mime, f.Width, f.Height, w)))
	}
	if f.Mime != "image/svg+xml" || len(others) > 0 {
		others = append(others, link(thumbSpec{rel: rel, width: f.Width, height: f.Height}))
	}
	b.WriteString(`<span class="mw-filepage-other-resolutions">` + s.uif("Other resolutions: %s.", strings.Join(others, " | ")) + `</span></div>`)
	return b.String()
}

// categoryListing is MediaWiki's CategoryViewer output: subcategories with
// their CategoryTree counts, pages, and a gallery of the files.
func (s *site) categoryListing(title string) string {
	name := strings.TrimPrefix(title, "Category:")
	members := s.categories[name]
	var subcats, pages, files []categoryMember
	for _, m := range members {
		switch m.page.Meta.Namespace {
		case 14:
			subcats = append(subcats, m)
		case 6:
			files = append(files, m)
		default:
			pages = append(pages, m)
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="mw-category-generated" lang="` + s.lang + `" dir="ltr">`)
	if len(members) == 0 {
		b.WriteString("<p><i>" + s.ui("This category currently contains no pages or media.") + "</i>\n</p>")
	}
	if len(subcats) > 0 {
		intro := s.ui("This category has only the following subcategory.")
		if n := s.num(int64(len(subcats))); len(subcats) > 1 {
			intro = s.uif("This category has the following %s subcategories, out of %s total.", n, n)
		}
		b.WriteString(`<div id="mw-subcategories">` + "\n<h2>" + s.ui("Subcategories") + "</h2>\n<p>" + intro + "\n</p>")
		b.WriteString(s.categoryColumns(subcats, s.categoryTreeItem))
		b.WriteString("</div>")
	}
	if len(pages) > 0 {
		intro := s.ui("This category contains only the following page.")
		if n := s.num(int64(len(pages))); len(pages) > 1 {
			intro = s.uif("The following %s pages are in this category, out of %s total.", n, n)
		}
		b.WriteString(`<div id="mw-pages">` + "\n<h2>" + s.uif("Pages in category \"%s\"", htmlEscape(name)) + "</h2>\n<p>" + intro + "\n</p>")
		b.WriteString(s.categoryColumns(pages, func(p *sitePage) string {
			return `<a href="` + htmlEscape(s.pageURL(p)) + `" title="` + htmlEscape(p.Meta.Title) + `">` + htmlEscape(p.Meta.Title) + `</a>`
		}))
		b.WriteString("</div>")
	}
	if len(files) > 0 {
		sortMembers(files)
		intro := s.ui("This category contains only the following file.")
		if n := s.num(int64(len(files))); len(files) > 1 {
			intro = s.uif("The following %s files are in this category, out of %s total.", n, n)
		}
		b.WriteString(`<div id="mw-category-media">` + "\n<h2>" + s.uif("Media in category \"%s\"", htmlEscape(name)) + "</h2>\n<p>" + intro + "\n</p>")
		b.WriteString(`<ul class="gallery mw-gallery-traditional">` + "\n")
		for _, m := range files {
			b.WriteString(s.galleryBox(m.page))
		}
		b.WriteString("</ul></div>")
	}
	b.WriteString("</div>")
	return b.String()
}

func sortMembers(members []categoryMember) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := strings.ToUpper(members[i].sortKey), strings.ToUpper(members[j].sortKey)
		if a != b {
			return a < b
		}
		return members[i].page.Meta.Title < members[j].page.Meta.Title
	})
}

// categoryColumns groups members under the first letter of their sort key,
// in columns once there are more than six.
func (s *site) categoryColumns(members []categoryMember, item func(*sitePage) string) string {
	sortMembers(members)
	var b strings.Builder
	class := "mw-category"
	if len(members) > 6 {
		class += " mw-category-columns"
	}
	b.WriteString(`<div lang="en" dir="ltr" class="mw-content-ltr"><div class="` + class + `">`)
	current := "\x00"
	for i, m := range members {
		head := "&nbsp;"
		if r, _ := utf8.DecodeRuneInString(strings.ToUpper(m.sortKey)); r != utf8.RuneError && !unicode.IsSpace(r) {
			head = htmlEscape(string(r))
		}
		if head != current {
			if i > 0 {
				b.WriteString("</ul></div>")
			}
			b.WriteString(`<div class="mw-category-group"><h3>` + head + "</h3>\n<ul>")
			current = head
		} else {
			b.WriteString("\n")
		}
		b.WriteString("<li>" + item(m.page) + "</li>")
	}
	b.WriteString("</ul></div></div></div>")
	return b.String()
}

// categoryTreeItem is a subcategory as the CategoryTree extension lists
// it, with a toggle when it has subcategories of its own.
func (s *site) categoryTreeItem(p *sitePage) string {
	name := strings.TrimPrefix(p.Meta.Title, "Category:")
	var cats, pages, files int
	for _, m := range s.categories[name] {
		switch m.page.Meta.Namespace {
		case 14:
			cats++
		case 6:
			files++
		default:
			pages++
		}
	}
	bullet := `<span class="CategoryTreeEmptyBullet"></span>`
	if cats > 0 {
		bullet = `<span class="CategoryTreeBullet"><span class="CategoryTreeToggle" data-ct-title="` + htmlEscape(strings.ReplaceAll(name, " ", "_")) +
			`" data-ct-state="collapsed" title="` + s.ui("expand") + `"></span> </span>`
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return s.num(int64(n)) + " " + many
	}
	contains := "Contains " + plural(cats, "subcategory", "subcategories") + ", " + plural(pages, "page", "pages") + ", and " + plural(files, "file", "files")
	if s.lang != "en" {
		contains = s.uif("Contains %s subcategories, %s pages, and %s files", s.num(int64(cats)), s.num(int64(pages)), s.num(int64(files)))
	}
	var counts []string
	for _, c := range []struct {
		n      int
		letter string
	}{{cats, "C"}, {pages, "P"}, {files, "F"}} {
		if c.n > 0 {
			counts = append(counts, s.num(int64(c.n))+" "+c.letter)
		}
	}
	summary := s.ui("empty")
	if len(counts) > 0 {
		summary = strings.Join(counts, ", ")
	}
	return `<div class="CategoryTreeSection"><div class="CategoryTreeItem">` + bullet + ` <a href="` + htmlEscape(s.pageURL(p)) + `" title="` + htmlEscape(p.Meta.Title) + `">` +
		htmlEscape(name) + `</a>&lrm; <span title="` + htmlEscape(contains) + `" dir="ltr">(` + summary + `)</span></div><div class="CategoryTreeChildren" style="display:none"></div></div>`
}

// galleryBox is one file in a category's traditional gallery.
func (s *site) galleryBox(p *sitePage) string {
	name := strings.TrimPrefix(p.Meta.Title, "File:")
	href := htmlEscape(s.pageURL(p))
	image := ""
	caption := ""
	if f := p.Meta.File; f != nil {
		if rel, ok := imagePath(f.URL); ok && thumbnailable[f.Mime] && f.Width > 0 && f.Height > 0 {
			g := galleryThumb(rel, f.Mime, f.Width, f.Height)
			image = fmt.Sprintf(`<span typeof="mw:File"><a href="%s" class="mw-file-description"><img src="%s" decoding="async" loading="lazy" width="%d" height="%d" class="mw-file-element" data-file-width="%d" data-file-height="%d"></a></span>`,
				href, htmlEscape(s.imageURL("/w/images/"+g.rel)), g.width, g.height, f.Width, f.Height)
		}
		if f.Width > 0 && f.Height > 0 {
			caption = fmt.Sprintf("%s × %s; %s", s.num(int64(f.Width)), s.num(int64(f.Height)), s.size(f.Size))
		} else {
			caption = s.size(f.Size)
		}
	}
	if image == "" {
		image = `<span typeof="mw:File"><a href="` + href + `" class="mw-file-description">` + htmlEscape(name) + `</a></span>`
	}
	return "\t\t<li class=\"gallerybox\" style=\"width: 155px\">\n\t\t\t<div class=\"thumb\" style=\"width: 150px; height: 150px;\">" + image + "</div>\n" +
		"\t\t\t<div class=\"gallerytext\">\n\t\t\t\t<a href=\"" + href + "\" class=\"galleryfilename galleryfilename-truncate\" title=\"" + htmlEscape(p.Meta.Title) + "\">" + htmlEscape(name) + "</a>\n\t\t\t\t" +
		caption + "\n\t\t\t</div>\n\t\t</li>\n"
}

// addGeneratedCategories gives every category that has members but no page
// of its own a generated page, so category links never leave the site
// for a listing the site can draw itself.
func (s *site) addGeneratedCategories() {
	names := make([]string, 0)
	for name := range s.categories {
		if s.byTitle["Category:"+name] == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		title := "Category:" + name
		p := &sitePage{Meta: pageMeta{Title: title, Namespace: 14}, Path: "wiki/" + titleFilePath(title) + ".html"}
		s.pages = append(s.pages, p)
		s.byTitle[title] = p
	}
}

// writeSpecialPages writes the site root, the not-found page and the
// random page.
func (s *site) writeSpecialPages(root404 bool) error {
	main := s.titleURL("Main Page", "")
	root := `<!DOCTYPE html><html lang="` + s.lang + `"><head><meta charset="UTF-8"><title>Tolkien Gateway</title>` +
		`<meta http-equiv="refresh" content="0; url=` + htmlEscape(main) + `"><link rel="canonical" href="` + wikiOrigin + `/wiki/Main_Page">` +
		`<script>location.replace(` + jsString(main) + `)</script></head><body><p><a href="` + htmlEscape(main) + `">Tolkien Gateway</a></p></body></html>`
	if err := s.writeFile("index.html", []byte(root)); err != nil {
		return err
	}
	special := func(title, content string) string {
		p := &sitePage{Meta: pageMeta{Title: title, Namespace: -1}}
		return s.document(p, renderedPage{content: `<div class="mw-parser-output">` + content + `</div>`})
	}
	// Pages serves the root 404.html for every missing path, whichever
	// language it is under; its script sends readers on to the page they
	// meant in that language.
	if root404 {
		notFound := special("Special:Badtitle", `<p id="tgw-notfound">`+s.ui("There is no copy of this page here.")+` <span id="tgw-notfound-remote"></span></p>`+
			`<script>tgwNotFound()</script>`)
		if err := s.writeFile("404.html", []byte(notFound)); err != nil {
			return err
		}
	}
	random := special("Special:Random", `<p>`+s.ui("Finding a random page…")+`</p><script>tgwRandom()</script>`)
	return s.writeFile("wiki/Special:Random.html", []byte(random))
}

// chromeScript is skin/chrome.js: it runs in the head of every page, sets
// the root classes from the reader's preferences, sends broken bundled
// images to the wiki, and writes the header, page actions and footer that
// every page shares.
func (s *site) chromeScript() string {
	link := func(title string) string { return htmlEscape(s.titleURL(title, "")) }
	asset := func(p string) string { return s.assets + "/skin/assets" + p }
	u := s.ui
	icon := func(name string) string {
		return `<span class="citizen-ui-icon mw-ui-icon-` + name + ` mw-ui-icon-wikimedia-` + name + `"></span>`
	}
	item := func(id, href, title, label, iconName, extra string) string {
		t := ""
		if title != "" {
			t = ` title="` + htmlEscape(u(title)) + `"`
		}
		ic := ""
		if iconName != "" {
			ic = icon(iconName)
		}
		return `<li id="` + id + `" class="mw-list-item"><a href="` + href + `"` + t + extra + `>` + ic + `<span>` + htmlEscape(u(label)) + `</span></a></li>`
	}
	menu := func(id, heading, extraClass, items string) string {
		return `<nav id="` + id + `" class="citizen-menu mw-portlet mw-portlet-` + strings.TrimPrefix(id, "p-") + extraClass + `"><div class="citizen-menu__heading">` + u(heading) +
			`</div><div class="citizen-menu__content"><ul class="citizen-menu__content-list">` + items + `</ul></div></nav>`
	}
	dropdown := func(class, title, card, summaryIcon string) string {
		title = u(title)
		return `<details class="citizen-dropdown-details"><summary class="citizen-dropdown-summary" title="` + title + `" aria-details="` + card + `">` + summaryIcon + `<span>` + title + `</span></summary></details>`
	}
	burger := `<span class="citizen-ui-icon"><span></span><span></span><span></span></span>`
	stamp := asset("/w/resources/assets/tg-stamp.png")
	wordmark := asset("/w/images/8/85/Tolkien_Gateway_wordmark_-_no_underline.svg")
	random := s.base + "/wiki/Special:Random.html"

	var h strings.Builder
	h.WriteString(`<header class="mw-header citizen-header"><div class="citizen-header__logo"><a href="` + link("Main Page") + `" class="mw-logo citizen-header__button" title="` + u("Visit the main page") + `">` +
		`<img class="mw-logo-icon" src="` + stamp + `" alt="" aria-hidden="true" height="32" width="32">` + icon("home") + `</a></div>`)
	h.WriteString(`<div class="citizen-search citizen-header__item citizen-dropdown"><details id="citizen-search-details" class="citizen-dropdown-details"><summary class="citizen-dropdown-summary" title="` + u("Toggle search") + ` [/]" aria-details="citizen-search__card">` +
		burger + `<span>` + u("Toggle search") + `</span></summary></details>`)
	h.WriteString(`<div role="search" id="citizen-search__card" class="citizen-search-box citizen-search__card citizen-menu__card"><form action="` + wikiOrigin + `/w/index.php" class="citizen-search__form" id="searchform" autocomplete="off">` +
		`<input type="hidden" name="title" value="Special:Search"><label class="citizen-search__formIcon" for="searchInput"><span class="citizen-search__icon citizen-ui-icon mw-ui-icon-wikimedia-search"></span><span class="screen-reader-text">` + u("Search") + `</span></label>` +
		`<input type="search" name="search" placeholder="` + u("Search Tolkien Gateway") + `" aria-label="` + u("Search Tolkien Gateway") + `" autocapitalize="sentences" title="` + u("Search Tolkien Gateway") + ` [alt-shift-f]" accesskey="f" id="searchInput">` +
		`<a class="citizen-search__random citizen-search__formButton" href="` + random + `" title="` + u("Random page") + `"><span class="citizen-ui-icon mw-ui-icon-wikimedia-die"></span><span class="screen-reader-text"></span></a></form>` +
		`<div class="citizen-search__footer"><div class="citizen-search__footer-start">` + u("Searching titles on this copy") + `</div><div class="citizen-search__footer-end">` +
		`<div class="citizen-keyboard-hint"><div class="citizen-keyboard-hint-label">` + u("Select item") + `</div><div class="citizen-keyboard-hint-key">↑ ↓</div></div>` +
		`<div class="citizen-keyboard-hint"><div class="citizen-keyboard-hint-label">` + u("Open search") + `</div><div class="citizen-keyboard-hint-key">/</div></div>` +
		`<div class="citizen-keyboard-hint"><div class="citizen-keyboard-hint-label">` + u("Exit search") + `</div><div class="citizen-keyboard-hint-key">Esc</div></div></div></div></div></div>`)

	h.WriteString(`<div class="citizen-drawer citizen-header__item citizen-dropdown">` + dropdown("", "Toggle menu", "citizen-drawer__card", burger) +
		`<div id="citizen-drawer__card" class="citizen-drawer__card citizen-menu__card"><header class="citizen-drawer__header"><a href="` + link("Main Page") + `" class="mw-logo citizen-drawer__logo" title="` + u("Visit the main page") + `">` +
		`<img class="mw-logo-icon" src="` + stamp + `" alt="" aria-hidden="true" height="80" width="80" loading="lazy"></a><div class="citizen-drawer__siteinfo"><div class="citizen-siteStats">`)
	for _, st := range []struct {
		id, title, icon string
		n               int
	}{{"articles", "articles", "article", s.stats.Articles}, {"images", "files", "image", s.stats.Files}, {"users", "users", "userAvatar", s.stats.Users}, {"edits", "edits", "edit", s.stats.Edits}} {
		if st.n == 0 {
			continue
		}
		h.WriteString(`<div class="citizen-siteStats__item" id="citizen-siteStats__item--` + st.id + `" title="` + u(st.title) + `">` + icon(st.icon) + `<span>` + compactNum(st.n) + `</span></div>`)
	}
	h.WriteString(`</div><img class="mw-logo-wordmark" alt="Tolkien Gateway" src="` + wordmark + `" width="135" height="20"></div></header>`)
	h.WriteString(`<section id="citizen-main-menu" class="citizen-main-menu citizen-drawer__menu">`)
	h.WriteString(menu("p-navigation", "Navigation", "",
		item("n-mainpage", link("Main Page"), "Visit the main page [alt-shift-z]", "Main Page", "", ` accesskey="z"`)+
			item("n-recentchanges", wikiOrigin+"/wiki/Special:RecentChanges", "A list of recent changes in the wiki [alt-shift-r]", "Recent changes", "recentChanges", ` accesskey="r"`)+
			item("n-randompage", random, "Load a random page [alt-shift-x]", "Random page", "die", ` accesskey="x"`)+
			item("n-Community-Portal", link("Tolkien Gateway:Community Portal"), "", "Community Portal", "", "")+
			item("n-To-do", link("Tolkien Gateway:To-do"), "", "To-do", "", "")+
			item("n-Forums", wikiOrigin+"/wiki/Forums:Council", "", "Forums", "", "")+
			item("n-Chat", link("Tolkien Gateway:Chat"), "", "Chat", "", "")+
			item("n-Help", link("Tolkien Gateway:Help"), "The place to find out", "Help", "", "")+
			item("t-specialpages", wikiOrigin+"/wiki/Special:SpecialPages", "A list of all special pages [alt-shift-q]", "Special pages", "specialPages", ` accesskey="q"`)+
			item("t-upload", wikiOrigin+"/wiki/Special:UploadWizard", "Upload files [alt-shift-u]", "Upload file", "upload", ` accesskey="u"`)))
	var portals strings.Builder
	for _, p := range []string{"Adaptations", "Books", "Characters", "Games", "Images", "Languages", "Locations", "Music", "People", "Timeline"} {
		portals.WriteString(item("n-"+p, link("Portal:"+p), "", p, "", ""))
	}
	portals.WriteString(item("n-Writings", link("Index:Writings by J.R.R. Tolkien"), "", "Writings", "", ""))
	h.WriteString(menu("p-Portals", "Portals", "", portals.String()))
	var external strings.Builder
	for _, e := range [][2]string{
		{"discord", "https://discord.gg/ktX6fhU9FY"}, {"bluesky", "https://bsky.app/profile/tolkiengateway.net"}, {"x", "https://x.com/TolkienGateway"},
		{"facebook", "https://www.facebook.com/tolkiengateway"}, {"reddit", "https://www.reddit.com/u/TolkienGateway"},
		{"instagram", "https://www.instagram.com/tolkiengatewayofficial"}, {"youtube", "https://www.youtube.com/@Tolkien-Gateway"},
	} {
		external.WriteString(item("n-sidebar-icon-"+e[0], e[1], "", "sidebar-icon-"+e[0], "", ` target="_blank"`))
	}
	h.WriteString(menu("p-External", "External", "", external.String()))
	h.WriteString(`</section></div></div>`)

	h.WriteString(`<div class="citizen-header__inner"><div class="citizen-header__start"></div><div class="citizen-header__end">`)
	h.WriteString(`<div class="citizen-preferences citizen-header__item citizen-dropdown"><details id="citizen-preferences-details" class="citizen-dropdown-details"><summary class="citizen-dropdown-summary" title="` + u("Toggle preferences menu") + `" aria-details="citizen-preferences__card">` +
		`<span class="citizen-ui-icon mw-ui-icon-wikimedia-settings"></span><span>` + u("Toggle preferences menu") + `</span></summary></details>` +
		`<div id="citizen-preferences__card" class="citizen-menu__card"><div id="citizen-preferences-content" class="citizen-preferences-content"></div></div></div>`)
	h.WriteString(menu("p-notifications", "Notifications", " emptyPortlet", ""))
	h.WriteString(`<div class="citizen-userMenu citizen-header__item citizen-dropdown">` + dropdown("", "Toggle personal menu", "citizen-userMenu__card", `<span class="citizen-ui-icon mw-ui-icon-wikimedia-userAvatar"></span>`) +
		`<div id="citizen-userMenu__card" class="citizen-menu__card"><div class="citizen-userInfo"><div class="citizen-userInfo-title"><div>` + u("Not logged in") + `</div></div><div class="citizen-userInfo-text"><div>` + u("Please sign up or log in to edit the wiki.") + `</div></div></div>`)
	h.WriteString(menu("p-user-interface-preferences", "user-interface-preferences", " emptyPortlet", ""))
	h.WriteString(strings.Replace(menu("p-personal", "Personal tools", "",
		item("pt-anontalk", wikiOrigin+"/wiki/Special:MyTalk", "Discussion about edits from this IP address [alt-shift-n]", "Talk", "userTalk", ` accesskey="n"`)+
			item("pt-anoncontribs", wikiOrigin+"/wiki/Special:MyContributions", "A list of edits made from this IP address [alt-shift-y]", "Contributions", "userContributions", ` accesskey="y"`)+
			item("pt-createaccount", wikiOrigin+"/w/index.php?title=Special:CreateAccount&amp;returnto=@RETURNTO@", "You are encouraged to create an account and log in; however, it is not mandatory", "Create account", "userAdd", "")+
			item("pt-login", wikiOrigin+"/w/index.php?title=Special:UserLogin&amp;returnto=@RETURNTO@", "You are encouraged to log in; however, it is not mandatory [alt-shift-o]", "Log in", "logIn", ` accesskey="o"`)),
		`class="citizen-menu mw-portlet mw-portlet-personal"`, `class="citizen-menu mw-portlet mw-portlet-personal" title="`+u("User menu")+`"`, 1))
	h.WriteString(`</div></div></div></div></header>`)

	var f strings.Builder
	f.WriteString(`<footer class="mw-footer citizen-footer"><div class="citizen-footer__container"><section class="citizen-footer__content"><div class="citizen-footer__siteinfo">` +
		`<div id="footer-sitetitle" class="mw-wiki-title"><img class="mw-logo-wordmark" alt="Tolkien Gateway" src="` + wordmark + `" width="135" height="20"></div>` +
		`<p id="footer-desc">` + u("The largest encyclopedia dedicated to J.R.R. Tolkien and The Lord of the Rings.") + `</p></div>` +
		`<nav id="footer-places"><ul><li id="footer-places-privacy"><a href="` + link("Tolkien Gateway:Privacy policy") + `">` + u("Privacy policy") + `</a></li>` +
		`<li id="footer-places-about"><a href="` + link("Tolkien Gateway") + `">` + u("About Tolkien Gateway") + `</a></li>` +
		`<li id="footer-places-source"><a href="https://github.com/tamnd/tolkiengateway">` + u("Source of this copy") + `</a></li></ul></nav></section>` +
		`<section class="citizen-footer__bottom"><div id="footer-tagline"><i>` + u("Speak friend and edit") + `</i></div><nav id="footer-icons" class="noprint"><ul>` +
		`<li id="footer-copyrightico"><a href="https://creativecommons.org/licenses/by-sa/4.0/" target="_blank"><img src="` + asset("/w/resources/assets/licenses/cc-by-sa.png") + `" alt="` + u("Creative Commons Attribution-ShareAlike 4.0 License") + `" width="88" height="31" loading="lazy"></a></li>` +
		`<li id="footer-poweredbyico"><a href="https://www.mediawiki.org/" target="_blank"><img src="` + asset("/w/resources/assets/poweredby_mediawiki_88x31.png") + `" alt="Powered by MediaWiki" srcset="` +
		asset("/w/resources/assets/poweredby_mediawiki_132x47.png") + ` 1.5x, ` + asset("/w/resources/assets/poweredby_mediawiki_176x62.png") + ` 2x" width="88" height="31" loading="lazy"></a></li>` +
		`</ul></nav></section></div></footer>`)

	var js strings.Builder
	js.WriteString("/* Generated by tgw publish. */\n")
	langs, _ := json.Marshal(s.langs)
	messages, _ := json.Marshal(uiMessages[s.lang])
	if s.lang == "en" {
		messages = []byte("{}")
	}
	fmt.Fprintf(&js, "var TGW={base:%s,assets:%s,lang:%s,langs:%s,m:%s,origin:%s,main:%s,random:%s};\n",
		jsString(s.base), jsString(s.assets), jsString(s.lang), strings.ReplaceAll(string(langs), "</", `<\/`), strings.ReplaceAll(string(messages), "</", `<\/`),
		jsString(wikiOrigin), jsString(s.titleURL("Main Page", "")), jsString(random))
	js.WriteString(chromeBoot)
	fmt.Fprintf(&js, "var TGW_HEADER=%s;\nvar TGW_FOOTER=%s;\n", jsString(h.String()), jsString(f.String()))
	js.WriteString(chromeWriters)
	return js.String()
}

// chromeBoot runs before the page paints.
const chromeBoot = `(function () {
	var root = document.documentElement, cls = root.className.replace(/(^|\s)client-nojs(\s|$)/, '$1client-js$2');
	var prefs = {};
	try { prefs = JSON.parse(localStorage.getItem('tgw-clientprefs')) || {}; } catch (e) {}
	for (var key in prefs) {
		if (/^[a-z-]+$/.test(key) && /^[a-z0-9]+$/.test(prefs[key])) {
			cls = cls.replace(new RegExp('(^|\\s)' + key + '-clientpref-\\S+'), '$1' + key + '-clientpref-' + prefs[key]);
		}
	}
	root.className = cls;
	// A bundled image that is missing is fetched from the wiki instead.
	document.addEventListener('error', function (e) {
		var img = e.target, local = TGW.assets + '/images/';
		if (!img || img.tagName !== 'IMG') return;
		var src = img.getAttribute('src') || '';
		if (src.indexOf(local) !== 0) return;
		img.removeAttribute('srcset');
		img.src = TGW.origin + '/w/images/' + src.slice(local.length);
	}, true);
})();
`

// chromeWriters draw the shared chrome where each page asks for it.
const chromeWriters = `function tgwT(s) {
	return TGW.m[s] || s;
}
function tgwEscape(s) {
	return String(s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; });
}
function tgwWikiEscape(s) {
	return encodeURIComponent(s).replace(/%(2F|3A|3B|40|24|21|2A|28|29|2C|7E)/g, function (m) { return decodeURIComponent(m); }).replace(/'/g, '%27');
}
function tgwRemote(title, query) {
	var t = tgwWikiEscape(title.replace(/ /g, '_'));
	return query ? TGW.origin + '/w/index.php?title=' + t + '&' + query : TGW.origin + '/wiki/' + t;
}
function tgwHeader() {
	var p = window.TGW_PAGE || { t: '' };
	document.write(TGW_HEADER.split('@RETURNTO@').join(encodeURIComponent(p.t).replace(/%20/g, '+')));
}
function tgwFooter() {
	document.write(TGW_FOOTER);
}
function tgwActions() {
	var p = window.TGW_PAGE;
	if (!p || p.ns < 0) return;
	var e = tgwEscape, here = location.pathname, h = '';
	function icon(n) { return '<span class="citizen-ui-icon mw-ui-icon-' + n + ' mw-ui-icon-wikimedia-' + n + '"></span>'; }
	function li(id, href, title, label, ic, extra, cls) {
		return '<li id="' + id + '" class="' + (cls ? cls + ' ' : '') + 'mw-list-item"><a href="' + e(href) + '"' + (title ? ' title="' + e(tgwT(title)) + '"' : '') + (extra || '') + '>' +
			(ic ? icon(ic) : '') + '<span>' + e(tgwT(label)) + '</span></a></li>';
	}
	function menu(id, heading, items, extra) {
		return '<nav id="' + id + '" class="citizen-menu mw-portlet mw-portlet-' + id.slice(2) + (items ? '' : ' emptyPortlet') + '"' + (extra || '') +
			'><div class="citizen-menu__heading">' + tgwT(heading) + '</div><div class="citizen-menu__content"><ul class="citizen-menu__content-list">' + items + '</ul></div></nav>';
	}
	function dropdown(cls, title, card, ic, counter) {
		title = tgwT(title);
		return '<div class="' + cls + ' page-actions__item citizen-dropdown"><details class="citizen-dropdown-details"><summary class="citizen-dropdown-summary" title="' + title +
			'" aria-details="' + card + '"' + (counter ? ' data-counter-text="' + counter + '"' : '') + '><span class="citizen-ui-icon mw-ui-icon-wikimedia-' + ic + '"></span><span>' + title + '</span></summary></details>';
	}
	h += '<button id="citizen-share" class="citizen-share citizen-button citizen-dropdown-summary" title="' + tgwT('Share this page') + '"><span class="citizen-ui-icon mw-ui-icon-wikimedia-share"></span><span>' + tgwT('Share this page') + '</span></button>';
	// This copy's own languages come first: the same page in each.
	var own = '', rest = here.slice(TGW.base.length);
	for (var j = 0; j < TGW.langs.length; j++) {
		var o = TGW.langs[j];
		if (o.c === TGW.lang) continue;
		own += '<li class="interlanguage-link interwiki-' + e(o.c) + ' mw-list-item"><a href="' + e(o.b + rest + location.hash) + '" title="' + e(p.t + ' – ' + o.n) + '" lang="' + e(o.c) +
			'" hreflang="' + e(o.c) + '" class="interlanguage-link-target"><span>' + e(o.n) + '</span></a></li>';
	}
	if (own || (p.l && p.l.length)) {
		var langs = own, count = TGW.langs.length - 1;
		for (var i = 0; p.l && i < p.l.length; i++) {
			var l = p.l[i], name = l.n.charAt(0).toUpperCase() + l.n.slice(1);
			langs += '<li class="interlanguage-link interwiki-' + e(l.l) + ' mw-list-item"><a href="' + e(l.u) + '" title="' + e(l.t + ' – ' + l.n) + '" lang="' + e(l.l) +
				'" hreflang="' + e(l.l) + '" class="interlanguage-link-target"><span>' + e(name) + '</span></a></li>';
		}
		count += p.l ? p.l.length : 0;
		h += dropdown('citizen-page-languages', 'More languages', 'citizen-languages__card', 'language', count) +
			'<aside id="citizen-languages__card" class="citizen-menu__card">' + menu('p-lang', 'In other languages', langs) + menu('p-variants', 'Variants', '') + '</aside></div>';
	}
	h += menu('p-views', 'Views',
		li('ca-view', here, '', 'Read', 'article', '', 'selected') +
		li('ca-viewsource', tgwRemote(p.t, 'action=edit'), 'You can view its source [alt-shift-e]', 'View source', 'editLock', ' accesskey="e"') +
		li('ca-history', tgwRemote(p.t, 'action=history'), 'Past revisions of this page [alt-shift-h]', 'View history', 'history', ' accesskey="h"'));
	var assoc = li(p.n[0], here, 'View the content page [alt-shift-c]', p.n[1], 'article', ' accesskey="c"', 'selected');
	if (p.k) assoc += li('ca-talk', tgwRemote(p.k), 'Discussion about the content page [alt-shift-t]', 'Discussion', 'speechBubbles', ' rel="discussion" accesskey="t"');
	h += menu('p-associated-pages', 'associated-pages', assoc);
	var canonical = TGW.origin + '/wiki/' + tgwWikiEscape(p.t.replace(/ /g, '_')), dbkey = tgwWikiEscape(p.t.replace(/ /g, '_'));
	var tools = li('t-whatlinkshere', TGW.origin + '/wiki/Special:WhatLinksHere/' + dbkey, 'A list of all wiki pages that link here [alt-shift-j]', 'What links here', 'articleRedirect', ' accesskey="j"') +
		li('t-recentchangeslinked', TGW.origin + '/wiki/Special:RecentChangesLinked/' + dbkey, 'Recent changes in pages linked from this page [alt-shift-k]', 'Related changes', 'recentChanges', ' rel="nofollow" accesskey="k"') +
		li('t-print', 'javascript:print();', 'Printable version of this page [alt-shift-p]', 'Printable version', 'printer', ' rel="alternate" accesskey="p"');
	if (p.r) {
		tools += li('t-permalink', tgwRemote(p.t, 'oldid=' + p.r), 'Permanent link to this revision of this page', 'Permanent link', 'link') +
			li('t-info', tgwRemote(p.t, 'action=info'), 'More information about this page', 'Page information', 'infoFilled') +
			li('t-cite', TGW.origin + '/w/index.php?title=Special:CiteThisPage&page=' + dbkey + '&id=' + p.r + '&wpFormIdentifier=titleform', 'Information on how to cite this page', 'Cite this page', 'quotes');
	}
	tools += li('t-urlshortener', TGW.origin + '/w/index.php?title=Special:UrlShortener&url=' + encodeURIComponent(canonical), '', 'Get shortened URL', 'link') +
		li('t-urlshortener-qrcode', TGW.origin + '/w/index.php?title=Special:QrCode&url=' + encodeURIComponent(canonical), '', 'Download QR code', 'qrCode');
	h += dropdown('page-actions-more', 'More actions', 'page-actions-more__card', 'ellipsis') + '<aside id="page-actions-more__card" class="citizen-menu__card">' +
		menu('p-cactions', 'More', '', ' title="More options"') + menu('p-tb', 'Tools', tools) + '</aside></div>';
	document.write(h);
}
function tgwIndex(done) {
	var x = new XMLHttpRequest();
	x.open('GET', TGW.assets + '/search.json');
	x.onload = function () { try { done(JSON.parse(x.responseText)); } catch (e) { done([]); } };
	x.onerror = function () { done([]); };
	x.send();
}
function tgwRandom() {
	tgwIndex(function (index) {
		var pages = index.filter(function (e) { return !e[3] && !/^(Category|File|Template|Tolkien Gateway|Portal|Index|Help|User|Forums?|MediaWiki|Module|Property|Special|[^:]* talk):/.test(e[0]); });
		if (!pages.length) { location.replace(TGW.main); return; }
		location.replace(TGW.base + '/' + tgwWikiEscape(pages[Math.floor(Math.random() * pages.length)][1]));
	});
}
function tgwNotFound() {
	// The one 404 page serves every language: answer in the language whose
	// pages the missing path was under.
	var base = TGW.base, path = location.pathname, title = '';
	for (var i = 0; i < TGW.langs.length; i++) {
		var b = TGW.langs[i].b;
		if (path.indexOf(b + '/') === 0 && b.length > base.length) base = b;
	}
	var prefix = base + '/wiki/';
	if (path.indexOf(prefix) === 0) {
		try { title = decodeURIComponent(path.slice(prefix.length).replace(/\.html$/, '')).replace(/_/g, ' '); } catch (e) {}
	}
	var remote = document.getElementById('tgw-notfound-remote');
	if (!title) return;
	remote.innerHTML = tgwT('Read %s on Tolkien Gateway.').replace('%s', '<a href="' + tgwEscape(tgwRemote(title)) + '">' + tgwEscape(title) + '</a>');
	tgwIndex(function (index) {
		var want = title.charAt(0).toUpperCase() + title.slice(1), lower = title.toLowerCase(), loose = null;
		for (var i = 0; i < index.length; i++) {
			if (index[i][0] === want) { location.replace(base + '/' + tgwWikiEscape(index[i][1]) + location.hash); return; }
			if (!loose && index[i][0].toLowerCase() === lower) loose = index[i];
		}
		if (loose) location.replace(base + '/' + tgwWikiEscape(loose[1]) + location.hash);
	});
}
`
