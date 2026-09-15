// Package fetch talks to Tolkien Gateway. The site puts a Cloudflare
// managed challenge in front of everything, including robots.txt, so a
// plain HTTP client cannot get past it. This package drives a real headless
// browser once to clear that challenge, then reuses the cookies it earns
// from a plain, rate limited HTTP client for the actual page fetches.
package fetch

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// UserAgent is shared between the browser session and the plain HTTP client
// that reuses its cookies, since Cloudflare ties a challenge pass to the
// user agent it was solved with.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

// WarmSession opens a headless browser, loads baseURL, waits for the
// Cloudflare challenge to clear, and returns the cookies the browser earned.
// Those cookies are what let a later plain HTTP request through.
func WarmSession(ctx context.Context, baseURL string) ([]*http.Cookie, error) {
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserAgent(UserAgent),
	)...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	browserCtx, cancelTimeout := context.WithTimeout(browserCtx, 60*time.Second)
	defer cancelTimeout()

	var cookies []*network.Cookie
	err := chromedp.Run(browserCtx,
		chromedp.Navigate(baseURL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForChallenge(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			cookies, err = network.GetCookies().Do(ctx)
			return err
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("warm session against %s: %w", baseURL, err)
	}

	out := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, &http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Domain: c.Domain,
			Path:   c.Path,
		})
	}
	return out, nil
}

// waitForChallenge polls the page title until it stops reading "Just a
// moment...", which is the Cloudflare interstitial's title while it runs
// its checks. A page that never carried the challenge clears immediately.
func waitForChallenge(ctx context.Context) error {
	deadline := time.Now().Add(45 * time.Second)
	for {
		var title string
		if err := chromedp.Title(&title).Do(ctx); err != nil {
			return err
		}
		if title != "Just a moment..." {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the Cloudflare challenge did not clear within the wait budget")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
