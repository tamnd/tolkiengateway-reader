//go:build uitest

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestUI drives a built site in headless Chrome and saves screenshots of
// the interactive parts of the skin. Serve the site first, then run
//
//	TGW_UI_URL=http://localhost:8765/tolkiengateway TGW_UI_OUT=/tmp/ui go test -tags uitest -run TestUI ./cmd/tgw
func TestUI(t *testing.T) {
	base := os.Getenv("TGW_UI_URL")
	out := os.Getenv("TGW_UI_OUT")
	if base == "" || out == "" {
		t.Skip("TGW_UI_URL and TGW_UI_OUT not set")
	}
	os.MkdirAll(out, 0o755)
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1400, 1000), chromedp.NoSandbox, chromedp.WSURLReadTimeout(2*time.Minute))
	actx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var errs []string
	chromedp.ListenTarget(ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventExceptionThrown:
			errs = append(errs, ev.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			if ev.Type == runtime.APITypeError {
				for _, a := range ev.Args {
					errs = append(errs, string(a.Value))
				}
			}
		}
	})
	shot := func(name string) chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			var buf []byte
			if err := chromedp.CaptureScreenshot(&buf).Do(ctx); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(out, name+".png"), buf, 0o644)
		})
	}
	pause := chromedp.Sleep(700 * time.Millisecond)
	page := strings.TrimRight(base, "/") + "/wiki/"
	var results int
	var collapsed bool
	var rootClass string
	err := chromedp.Run(ctx,
		chromedp.Navigate(page+"Gondor.html"), pause, shot("article"),
		chromedp.Click(`#citizen-search-details summary`), pause,
		chromedp.SendKeys(`#searchInput`, "bilbo"), chromedp.Sleep(1500*time.Millisecond), shot("search"),
		chromedp.Evaluate(`document.querySelectorAll('#citizen-typeahead-list-page li').length`, &results),
		chromedp.KeyEvent("\u001b"),
		chromedp.Click(`#citizen-preferences-details summary`), pause, shot("preferences"),
		chromedp.Click(`label[for="skin-client-pref-skin-theme-value-day"]`), pause, shot("light"),
		chromedp.Evaluate(`document.documentElement.className`, &rootClass),
		chromedp.Click(`label[for="skin-client-pref-skin-theme-value-os"]`),
		chromedp.KeyEvent("\u001b"),
		chromedp.Evaluate(`(() => { const h = document.querySelector('.citizen-section-heading'); h.click(); return h.nextElementSibling.hasAttribute('hidden'); })()`, &collapsed),
		chromedp.Evaluate(`window.scrollTo(0, 2500)`, nil), pause, shot("scrolled"),
		chromedp.Navigate(page+"Category:Actors_and_actresses.html"), pause, shot("category"),
		chromedp.Navigate(page+"Main_Page.html"), pause, shot("main"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if results == 0 {
		t.Error("search for bilbo listed no pages")
	}
	if !strings.Contains(rootClass, "skin-theme-clientpref-day") {
		t.Errorf("choosing Light left root classes %q", rootClass)
	}
	if !collapsed {
		t.Error("clicking a section heading did not collapse the section")
	}
	for _, e := range errs {
		t.Errorf("console: %s", e)
	}
}
