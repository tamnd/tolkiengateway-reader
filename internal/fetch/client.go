package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

// MinInterval is the default floor between two requests. Conservative on
// purpose, since the goal is to look like one patient reader, not a scraper.
// TOLKIENGATEWAY_FETCH_INTERVAL (a Go duration such as 750ms) overrides it.
const MinInterval = 2 * time.Second

// Client is a plain HTTP client, optionally carrying the cookies a browser
// session earned from Cloudflare, rate limited so it never hammers the site.
type Client struct {
	BaseURL string

	httpClient  *http.Client
	interval    time.Duration
	mu          sync.Mutex
	nextAllowed time.Time
}

// NewClient returns a client for baseURL. It first asks the API a cheap
// question over plain HTTP; only when that is answered by a Cloudflare
// challenge does it pay for a browser session and reuse its cookies. Some
// networks get the challenge and some never do, so the cheap path is tried
// first everywhere.
func NewClient(ctx context.Context, baseURL string) (*Client, error) {
	if os.Getenv("TOLKIENGATEWAY_FETCH") != "browser" {
		direct := NewDirectClient(baseURL)
		resp, err := direct.Get(ctx, "/w/api.php?action=query&meta=siteinfo&format=json")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return direct, nil
			}
		}
		if os.Getenv("TOLKIENGATEWAY_FETCH") == "direct" {
			if err != nil {
				return nil, fmt.Errorf("direct probe: %w", err)
			}
			return nil, fmt.Errorf("direct probe returned HTTP %d", resp.StatusCode)
		}
	}

	cookies, err := WarmSession(ctx, baseURL)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("build cookie jar: %w", err)
	}
	jar.SetCookies(u, cookies)

	return &Client{
		BaseURL:    baseURL,
		httpClient: &http.Client{Jar: jar, Timeout: 120 * time.Second},
		interval:   configuredInterval(),
	}, nil
}

// NewDirectClient builds a client without a browser session, for networks
// where the API answers plain requests.
func NewDirectClient(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		interval:   configuredInterval(),
	}
}

func configuredInterval() time.Duration {
	if raw := os.Getenv("TOLKIENGATEWAY_FETCH_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
			return d
		}
	}
	return MinInterval
}

// Get fetches path, relative to BaseURL, after waiting out the rate limit.
// Absolute URLs are fetched as given. Server errors, 429s and transport
// failures are retried with backoff, honouring Retry-After when present.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	target := path
	if u, err := url.Parse(path); err != nil || !u.IsAbs() {
		target = c.BaseURL + path
	}
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		c.wait()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned HTTP %d", target, resp.StatusCode)
			if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds < 600 {
				time.Sleep(time.Duration(seconds) * time.Second)
			}
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// wait blocks until at least the configured interval has passed since the
// last request this client made.
func (c *Client) wait() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.Before(c.nextAllowed) {
		time.Sleep(c.nextAllowed.Sub(now))
	}
	c.nextAllowed = time.Now().Add(c.interval)
}

// Challenged reports whether resp looks like a Cloudflare challenge page
// rather than the content that was asked for.
func Challenged(resp *http.Response) bool {
	return resp.StatusCode == http.StatusForbidden && resp.Header.Get("cf-mitigated") == "challenge"
}
