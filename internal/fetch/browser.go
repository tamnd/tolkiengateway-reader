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
	"os"
	"strconv"
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
	browserCtx, cancelBrowser := Browser(ctx)
	defer cancelBrowser()

	wait := challengeWait()
	browserCtx, cancelTimeout := context.WithTimeout(browserCtx, wait+15*time.Second)
	defer cancelTimeout()

	var cookies []*network.Cookie
	err := chromedp.Run(browserCtx,
		chromedp.Navigate(baseURL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return WaitForChallenge(ctx)
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

// Browser starts a Chrome session configured from the environment: the
// persistent profile and headed mode the operator chose. Cancel the returned
// function to close it.
func Browser(ctx context.Context) (context.Context, context.CancelFunc) {
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.UserAgent(UserAgent))
	if profile := os.Getenv("TOLKIENGATEWAY_BROWSER_PROFILE"); profile != "" {
		options = append(options, chromedp.UserDataDir(profile))
	}
	// Headless is the safe default for CI. Set this to false when the session
	// needs to behave like a user's normal Chrome window and be completed by
	// the user interactively.
	if os.Getenv("TOLKIENGATEWAY_BROWSER_HEADLESS") == "false" {
		options = append(options, chromedp.Flag("headless", false))
	}
	if os.Getenv("TOLKIENGATEWAY_BROWSER_NO_SANDBOX") == "true" {
		options = append(options, chromedp.NoSandbox)
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, options...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	return browserCtx, func() {
		cancelBrowser()
		cancelAlloc()
	}
}

// WaitForChallenge polls the page title until it stops reading "Just a
// moment...", which is the Cloudflare interstitial's title while it runs
// its checks. A page that never carried the challenge clears immediately.
func WaitForChallenge(ctx context.Context) error {
	deadline := time.Now().Add(challengeWait())
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

func challengeWait() time.Duration {
	if raw := os.Getenv("TOLKIENGATEWAY_BROWSER_CHALLENGE_WAIT"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 45 * time.Second
}
