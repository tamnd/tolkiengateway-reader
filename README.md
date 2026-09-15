# tolkiengateway-reader

The toolchain behind [tamnd/tolkiengateway](https://github.com/tamnd/tolkiengateway). It pulls pages from Tolkien Gateway, converts them into Markdown, keeps the corpus in sync with the live wiki, and publishes it as a website and an EPUB.

The binary is called `tgw`.

## What it does

`tgw acquire` fetches the page list and individual pages from the MediaWiki API.

`tgw extract` converts a fetched page into Markdown with a full YAML front matter block, keeping the infobox fields, the categories and the revision id.

`tgw sync` reads recent changes from the wiki and re-extracts only the pages that moved since the last run.

`tgw publish` builds a static website and an EPUB from the committed corpus, with no network access needed at build time.

`tgw audit` checks the corpus against a small set of rules, including the one rule that matters most here: no image file is ever committed, because Tolkien Gateway's images are not covered by the same licence as its text.

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
