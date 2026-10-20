package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/tamnd/tolkiengateway-reader/internal/fetch"
)

// skinSamplePages are opened to learn which style modules the wiki loads.
// Between them they cover articles, categories, file pages and the main
// page, each of which pulls in a few modules the others do not.
var skinSamplePages = []string{
	"Main_Page", "Bilbo_Baggins", "Category:Hobbits", "File:Mike_Szabados_-_Bilbo_Baggins.jpg",
	"The_Lord_of_the_Rings", "Tolkien_Gateway:Copyrights",
}

// skinPageModules style a single page rather than the skin. ext.NoTitle
// hides the page heading, so folding it into the shared stylesheet would
// hide every title; the site builder emits its rule only where asked.
var skinPageModules = map[string]bool{"site.styles": true, "ext.NoTitle": true}

// skinStaticAssets are the images the page shell itself references.
var skinStaticAssets = []string{
	"/w/resources/assets/tg-stamp.png",
	"/w/images/8/85/Tolkien_Gateway_wordmark_-_no_underline.svg",
	"/w/resources/assets/licenses/cc-by-sa.png",
	"/w/resources/assets/poweredby_mediawiki_88x31.png",
	"/w/resources/assets/poweredby_mediawiki_132x47.png",
	"/w/resources/assets/poweredby_mediawiki_176x62.png",
	"/favicon/favicon.ico",
	"/favicon/apple-touch-icon.png",
}

// runSkin vendors the wiki's own stylesheet. It drives a real browser,
// because load.php sits behind the Cloudflare challenge even where the API
// does not, and because some styles only arrive through ResourceLoader's
// JavaScript. The output is committed to this repo and embedded in tgw, so
// publishing never touches the network.
func runSkin(args []string) error {
	fs := flag.NewFlagSet("skin", flag.ContinueOnError)
	base := fs.String("base-url", baseURL, "Tolkien Gateway base URL")
	out := fs.String("out", filepath.Join("cmd", "tgw", "site", "skin"), "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := fetch.Browser(context.Background())
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 15*time.Minute)
	defer cancelTimeout()

	modules := map[string]bool{}
	var injected []string
	seenInjected := map[string]bool{}
	for _, page := range skinSamplePages {
		var links []string
		var styles []string
		var typeahead, preferences string
		err := chromedp.Run(ctx,
			chromedp.Navigate(strings.TrimRight(*base, "/")+"/wiki/"+page),
			chromedp.ActionFunc(fetch.WaitForChallenge),
			chromedp.Sleep(4*time.Second),
			// Search and preferences load their modules, and so their styles,
			// only when first opened.
			chromedp.Click(`#citizen-search-details > summary`, chromedp.ByQuery),
			chromedp.SendKeys(`#searchInput`, "Bilbo", chromedp.ByQuery),
			chromedp.Sleep(4*time.Second),
			chromedp.OuterHTML(`#citizen-search__card`, &typeahead, chromedp.ByQuery),
			chromedp.KeyEvent("\x1b"),
			chromedp.Click(`#citizen-preferences-details > summary`, chromedp.ByQuery),
			chromedp.Sleep(3*time.Second),
			chromedp.OuterHTML(`#citizen-preferences__card`, &preferences, chromedp.ByQuery),
			chromedp.Evaluate(`[...document.querySelectorAll('head link[rel=stylesheet]')].map(l => l.href)`, &links),
			chromedp.Evaluate(`[...document.querySelectorAll('head style')].map(s => s.textContent)`, &styles),
		)
		if err != nil {
			return fmt.Errorf("open %s: %w", page, err)
		}
		for _, link := range links {
			u, err := url.Parse(link)
			if err != nil {
				continue
			}
			for _, group := range strings.Split(u.Query().Get("modules"), "|") {
				for _, m := range expandModuleGroup(group) {
					if !skinPageModules[m] {
						modules[m] = true
					}
				}
			}
		}
		for _, s := range styles {
			if !seenInjected[s] {
				seenInjected[s] = true
				injected = append(injected, s)
			}
		}
		if page == skinSamplePages[0] {
			// Reference markup for the parts the site rebuilds in script.
			for name, sample := range map[string]string{"typeahead.html": typeahead, "preferences.html": preferences} {
				if err := atomicWrite(filepath.Join(*out, "samples", name), []byte(sample)); err != nil {
					return err
				}
			}
		}
		fmt.Printf("skin: %s, %d modules so far\n", page, len(modules))
	}
	names := make([]string, 0, len(modules))
	for m := range modules {
		names = append(names, m)
	}
	sort.Strings(names)

	get := func(target string) ([]byte, error) {
		var encoded string
		script := fmt.Sprintf(`fetch(%q).then(r => { if (!r.ok) throw new Error('HTTP ' + r.status); return r.arrayBuffer() })
			.then(b => { let s = ''; const a = new Uint8Array(b); for (let i = 0; i < a.length; i += 0x8000) s += String.fromCharCode.apply(null, a.subarray(i, i + 0x8000)); return btoa(s) })`, target)
		err := chromedp.Run(ctx, chromedp.Evaluate(script, &encoded, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}))
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", target, err)
		}
		return base64.StdEncoding.DecodeString(encoded)
	}

	moduleCSS, err := get("/w/load.php?lang=en&modules=" + url.QueryEscape(strings.Join(names, "|")) + "&only=styles&skin=citizen")
	if err != nil {
		return err
	}
	siteCSS, err := get("/w/load.php?lang=en&modules=site.styles&only=styles&skin=citizen")
	if err != nil {
		return err
	}
	// ResourceLoader puts JavaScript-loaded styles at its marker, which sits
	// between the module stylesheet and site.styles.
	css := string(moduleCSS) + "\n" + strings.Join(injected, "\n") + "\n" + string(siteCSS)

	assets := map[string]string{}
	css = cssURLRE.ReplaceAllStringFunc(css, func(match string) string {
		raw := strings.Trim(cssURLRE.FindStringSubmatch(match)[1], `"' `)
		if strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "#") {
			return match
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Host != "" && u.Host != "tolkiengateway.net") || !strings.HasPrefix(u.Path, "/") {
			return match
		}
		local := "assets" + u.Path
		if u.RawQuery != "" && strings.HasSuffix(u.Path, ".php") {
			// load.php serves the icon images under one path; the query is
			// what tells them apart.
			sum := sha256.Sum256([]byte(u.RawQuery))
			local = "assets/w/load/" + hex.EncodeToString(sum[:6]) + ".svg"
		}
		if prev, ok := assets[u.Path+queryPart(u)]; ok {
			local = prev
		}
		assets[u.Path+queryPart(u)] = local
		return "url(" + local + ")"
	})
	for _, asset := range skinStaticAssets {
		assets[asset] = "assets" + asset
	}
	for remote, local := range assets {
		data, err := get(remote)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skin: %v\n", err)
			continue
		}
		if err := atomicWrite(filepath.Join(*out, filepath.FromSlash(local)), data); err != nil {
			return err
		}
	}
	if err := atomicWrite(filepath.Join(*out, "skin.css"), []byte(css)); err != nil {
		return err
	}
	fmt.Printf("skin: %d modules, %d injected styles, %d assets, %d bytes of CSS\n", len(names), len(injected), len(assets), len(css))
	return nil
}

var cssURLRE = regexp.MustCompile(`url\(([^)]*)\)`)

func queryPart(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

// expandModuleGroup undoes load.php's module name packing, where
// "skins.citizen.icons,styles" means two modules sharing a prefix.
func expandModuleGroup(group string) []string {
	if !strings.Contains(group, ",") {
		return []string{group}
	}
	dot := strings.LastIndex(strings.Split(group, ",")[0], ".")
	prefix, first := "", group
	if dot >= 0 {
		prefix, first = group[:dot+1], group[dot+1:]
	}
	var out []string
	for _, suffix := range strings.Split(first, ",") {
		out = append(out, prefix+suffix)
	}
	return out
}
