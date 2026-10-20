# tolkiengateway-reader

The toolchain behind [tamnd/tolkiengateway](https://github.com/tamnd/tolkiengateway). It pulls pages from Tolkien Gateway, converts them into Markdown, keeps the corpus in sync with the live wiki, and publishes it as a website and an EPUB.

The binary is called `tgw`.

## Operator workflow

The acquisition commands use a real Chrome session. For a normal user session,
point the browser at a persistent Chrome profile. If the site presents a
security verification page, complete it in that browser profile once; the
reader reuses the resulting session cookies and does not create a new profile
for every request.

```sh
export TOLKIENGATEWAY_BROWSER_PROFILE=/path/to/persistent/chrome-profile
export TOLKIENGATEWAY_BROWSER_HEADLESS=false
export TOLKIENGATEWAY_BROWSER_CHALLENGE_WAIT=300
export TOLKIENGATEWAY_CORPUS=/path/to/tolkiengateway

tgw acquire pages
tgw acquire full --corpus /path/to/tolkiengateway
tgw acquire page "Bilbo Baggins"
tgw extract "$TOLKIENGATEWAY_CORPUS/raw/Bilbo_Baggins.json"
tgw audit
tgw publish -out site
```

`acquire pages` writes the complete inventory to
`manifests/pages.yaml` and `reports/pages.md`. `acquire page` saves the exact
revision and wikitext under `raw/`; extraction is therefore rerunnable without
fetching again. Writes use temporary files followed by rename, so interrupted
runs do not leave partial corpus files.

`acquire full` runs the initial inventory, raw revision download, and English
Markdown extraction in one resumable, rate-limited process. Use
`--max-pages 1` for the Bilbo-sized canary before starting the full corpus.

Translation is a separate durable stage. Configure the local ends of SSH
tunnels to the ChatGPT proxies on server1, server2 and server3 in
`~/.config/tolkiengateway/routes.json`; the route file contains no secret keys,
only `api_key_env` names.

```json
{
  "routes": [
    {"name":"server1","kind":"pool","base_url":"http://127.0.0.1:8101/v1","model":"gpt-5","api_key_env":"CHATGPT_PROXY_KEY","rank":1,"concurrency":2,"host":"server1","remote_port":8077,"local_port":8101,"jobs":["translate"]},
    {"name":"server2","kind":"pool","base_url":"http://127.0.0.1:8102/v1","model":"gpt-5","api_key_env":"CHATGPT_PROXY_KEY","rank":2,"concurrency":3,"host":"server2","remote_port":8077,"local_port":8102,"jobs":["translate"]},
    {"name":"server3","kind":"pool","base_url":"http://127.0.0.1:8103/v1","model":"gpt-5","api_key_env":"CHATGPT_PROXY_KEY","rank":3,"concurrency":4,"host":"server3","remote_port":8077,"local_port":8103,"jobs":["translate"]}
  ]
}
```

Then run:

```sh
tgw translate --corpus /path/to/tolkiengateway
```

The queue is stored in `.tgw/queue`, outputs go to `content/vi`, and each
model attempt is appended to `.tgw/ledger.jsonl`. Re-running the command skips
completed content-addressed jobs and resumes leased or failed work.

`sync` reads MediaWiki recent changes since `.tgw/sync.json`, refreshes only
those revisions, and advances the high-water mark after successful writes:

```sh
tgw sync --corpus /path/to/tolkiengateway
```

## What it does

`tgw acquire` fetches the page list and individual pages from the MediaWiki API.

`tgw extract` converts a fetched page into Markdown with a full YAML front matter block, keeping the infobox fields, the categories and the revision id.

`tgw sync` reads recent changes from the wiki and re-extracts only the pages that moved since the last run.

`tgw publish` builds a static website and an EPUB from the committed corpus, with no network access needed at build time.

`tgw audit` checks the corpus against a small set of rules, including that no image file is ever committed. The corpus repo holds Markdown only. Images are CC BY-SA like the text, but they are downloaded by `tgw crawl images` and bundled into the site by `tgw publish`, rather than kept in git.

## Status

This repo is a fresh scaffold. `tgw version`, `tgw publish -check` and `tgw audit` run today against an empty corpus and pass, which is the whole point at this stage: the pipeline shape works before there is anything in it to break. The milestones for the rest of it are tracked as issues in this repo.

## Corpus repo

This repo holds no content. The Markdown, front matter and manifests live in [tamnd/tolkiengateway](https://github.com/tamnd/tolkiengateway), which this repo's CI checks out as a sibling directory to build and audit it.

## Licence

The code in this repo is MIT licensed. See LICENSE. It has no bearing on the licence of the corpus it builds, which is CC BY-SA 4.0 and is stated in the corpus repo's own LICENSE.md.

## Building

```
go build ./cmd/tgw
```

Go 1.27 or later. No external dependencies yet.
