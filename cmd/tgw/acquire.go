package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/tamnd/tolkiengateway-reader/internal/fetch"
)

const baseURL = "https://tolkiengateway.net"

func runAcquire(args []string) error {
	fs := flag.NewFlagSet("acquire", flag.ContinueOnError)
	fs.Parse(args)

	if fs.NArg() == 0 {
		return fmt.Errorf("usage: tgw acquire <ping|pages> [flags]")
	}

	switch fs.Arg(0) {
	case "ping":
		return runAcquirePing()
	default:
		return fmt.Errorf("unknown acquire subcommand %q", fs.Arg(0))
	}
}

// runAcquirePing warms a browser session against the live wiki and asks the
// query API a small, harmless question, to prove the transport actually
// gets past the Cloudflare challenge instead of just theorizing about it.
func runAcquirePing() error {
	ctx := context.Background()

	client, err := fetch.NewClient(ctx, baseURL)
	if err != nil {
		return fmt.Errorf("warm a session: %w", err)
	}

	resp, err := client.Get(ctx, "/api.php?action=query&meta=siteinfo&format=json")
	if err != nil {
		return fmt.Errorf("call the query api: %w", err)
	}
	defer resp.Body.Close()

	if fetch.Challenged(resp) {
		return fmt.Errorf("the query api is still behind the Cloudflare challenge")
	}

	var siteInfo struct {
		Query struct {
			General struct {
				SiteName  string `json:"sitename"`
				Generator string `json:"generator"`
			} `json:"general"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&siteInfo); err != nil {
		return fmt.Errorf("decode the query api response: %w", err)
	}

	fmt.Printf("connected to %s (%s)\n", siteInfo.Query.General.SiteName, siteInfo.Query.General.Generator)
	return nil
}
