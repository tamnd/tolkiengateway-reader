package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"
)

// MinInterval is the floor between two requests. Conservative on purpose,
// since the goal is to look like one patient reader, not a scraper.
const MinInterval = 2 * time.Second

// Client is a plain HTTP client carrying the cookies a browser session
// earned from Cloudflare, rate limited so it never hammers the site.
type Client struct {
	BaseURL string

	httpClient  *http.Client
	mu          sync.Mutex
	nextAllowed time.Time
}

// NewClient warms a browser session against baseURL and builds a Client
// that reuses the cookies it earned.
func NewClient(ctx context.Context, baseURL string) (*Client, error) {
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
		httpClient: &http.Client{Jar: jar, Timeout: 30 * time.Second},
	}, nil
}

// Get fetches path, relative to BaseURL, after waiting out the rate limit.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	c.wait()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)

	return c.httpClient.Do(req)
}

// wait blocks until at least MinInterval has passed since the last request
// this client made.
func (c *Client) wait() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.Before(c.nextAllowed) {
		time.Sleep(c.nextAllowed.Sub(now))
	}
	c.nextAllowed = time.Now().Add(MinInterval)
}

// Challenged reports whether resp looks like a Cloudflare challenge page
// rather than the content that was asked for.
func Challenged(resp *http.Response) bool {
	return resp.StatusCode == http.StatusForbidden && resp.Header.Get("cf-mitigated") == "challenge"
}
