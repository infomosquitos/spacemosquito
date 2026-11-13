# SpaceMosquito

[![SpaceMosquito](firefox-extension/assets/icon.svg)](https://github.com/infomosquitos/SpaceMosquito)

Confluence space scraper, indexer, and search engine. Uses the Confluence REST API for content extraction (with headless browser fallback) and stores pages locally as Markdown in **SQLite**. Search is **hybrid**: keyword search (FTS5/BM25) plus **semantic search** with embeddings computed on your machine (multilingual bge-m3 via llama.cpp, Metal on Apple Silicon), so people and agents can ask questions in plain language. **Browse** saved spaces in your web browser as readable pages, as in Confluence: page tree sidebar, images, attachments, author and dates, search bar (📖 in the extension, or `http://localhost:56403/browse/`). Exposes an MCP server for AI agents and browser extensions for session management and crawl control. See [Architecture](docs/ARCHITECTURE.md).

All state lives under `~/.spacemosquito/` (or a portable `--data-dir`).

## Install

- **[Install](docs/INSTALL.md)** — `brew install --cask infomosquitos/tap/spacemosquito` (macOS; also installs the 635 MB embedding model once), or download the archive for your platform + Firefox XPI / Chrome zip from GitHub Releases
- **[Install from source](docs/INSTALL-FROM-SOURCE.md)** — build with Go and npm

**Logs:** `spacemosquito serve` logs to the terminal and to `~/.spacemosquito/logs/serve.log` (rotated at 10 MB, 3 old files kept; set `log_file` in `config.yaml`, `""` for terminal only).

**Updates:** `spacemosquito serve` and `spacemosquito version` check GitHub for a newer release (at most once a day) and print how to upgrade, e.g. `brew upgrade --cask spacemosquito && spacemosquito version`; other commands repeat the last result without network access. Turn it off with `update_check: false` in `config.yaml` or `SPACEMOSQUITO_NO_UPDATE_CHECK=1`.

## First run

```sh
./spacemosquito init
# Optional: pre-download Chromium (~150 MB) for API-fallback crawls
./spacemosquito init --download-browser

./spacemosquito serve
```

`init` writes a generated **encryption key** to `config.yaml` (`session.encryption_key`). Keep that file — the same key is required to decrypt `session.enc`.

Portable mode:

```sh
./spacemosquito init --data-dir ./data
SPACEMOSQUITO_DATA_DIR=./data ./spacemosquito serve
```

## Capture a Confluence session

1. Start the server: `./spacemosquito serve`
2. Load the Space Mosquito extension ([Install](docs/INSTALL.md#browser-extension) or [from source](docs/INSTALL-FROM-SOURCE.md#browser-extensions)).
3. Open Confluence and use the extension to send cookies to `http://localhost:56403`.

## Crawl a space

```sh
./spacemosquito crawl "https://your-domain.atlassian.net/wiki/spaces/SPACEKEY"

# Self-hosted / Server-DC (context path is auto-detected, e.g. /confluence/)
./spacemosquito crawl "https://wiki.mycompany.com/confluence/display/SPACEKEY"

# Full refresh: re-scrape every page and re-download every asset
./spacemosquito crawl --force "https://your-domain.atlassian.net/wiki/spaces/SPACEKEY"
```

Refresh a single known page (always overwrites text + assets; no `--force`):

```sh
./spacemosquito crawl-page SPACEKEY 250347937
```

Or trigger a crawl via the extension or `POST /api/crawl`.

## MCP (agents)

SpaceMosquito serves MCP over Streamable HTTP at `http://127.0.0.1:56403/mcp` (same process as `serve`). Agents can search the local catalog with natural-language questions or exact terms, and read pages — see **[Configure MCP](docs/guides/configure-mcp.md)**.

## Search

Search understands **questions, not only keywords**. By default it is **hybrid**: keyword search (SQLite FTS5/BM25 — exact terms, IDs like `CCP-761`, titles; multi-word queries match **all** terms, title matches rank higher) merged with **semantic** search, which finds pages by meaning in any language ("how is the cart recalculated when prices are outdated" finds the order simulation pages even though no page contains all those words). Default: 10 results.

Semantic search runs locally: `serve` splits each page into sections, embeds them with the multilingual **bge-m3** model through llama.cpp (Metal on Apple Silicon, CPU elsewhere) in a helper process, and keeps them up to date in the background as pages are crawled. The first run takes a few minutes per thousand pages (about 10 minutes for 950 pages on an M-series Mac); afterwards only changed pages are embedded. Nothing leaves your machine. Homebrew installs the model with the app; see [INSTALL](docs/INSTALL.md) for archives. `spacemosquito stats` and `GET /api/search/stats` show coverage. Choose a mode with `--mode`, REST `mode=` or the MCP `mode` field: `hybrid` (default), `keyword`, `semantic`. Results say how each page matched (`keyword`, `semantic`, `both`) and, for semantic matches, which section (`heading_path`). Switch it off and on in the extension's Settings (Search), with no restart: off pauses indexing, frees the model's memory and searches by keyword only; the index is kept. `embedder.provider: none` in `config.yaml` turns it off for good. See [ADR-019](docs/adr/019-semantic-search-embeddings.md).

Page **content** is stored as Markdown (`content.md` on disk, `pages.content` in the DB). API-crawled pages are converted from **Confluence Storage Format** via a rule-based converter (macros, tables, code, images, draw.io, links); browser-fallback pages use generic HTML→Markdown. After upgrading, regenerate existing pages:

```sh
./spacemosquito reindex --content
```

`reindex --content` re-converts each saved page from the right source — Storage Format (`raw.html`) through the CSF converter, rendered HTML (`index.html`, browser-fallback pages only) through the generic one — routing by the `body_format` recorded in `metadata.json` (or a content sniff for older pages). It regenerates Markdown offline and removes HTML that versions before 0.8 generated into the catalog; image/diagram **assets** are (re)downloaded on the next crawl.

Browse the catalog by clicking **📖** next to a space in the extension's **Spaces** tab, or open `http://localhost:56403/browse/` while `serve` runs. Pages are rendered on request from `content.md` (images included), so every page always shows the current UI: a byline with who created and last updated the page in Confluence (and when), its attachments (file previews become links; PDFs open inline, PowerPoint/Excel/Word download; an **Attachments** list at the bottom; files over `storage.max_attachment_mb`, default 100, link to Confluence), a sidebar with the space's page tree (current page highlighted, title filter, expand/collapse), as in Confluence, and a content search bar on top (press **/**) with instant results (same full-text search as MCP, matches highlighted); "See all results" opens the space's full result list. The space page shows the whole tree with page counts. To browse without `serve`, write a static copy with `spacemosquito export-html [--space KEY] <dir>` and open `<dir>/index.html` (no content search there). `saved/index.md` and `saved/<SPACE>/index.md` hold the same tree for Markdown viewers and agents; `raw.html` is the original Confluence source. See [ADR-018](docs/adr/018-browse-rendered-on-request.md).

```sh
./spacemosquito search "your query"
./spacemosquito search "your query" SPACEKEY
./spacemosquito search "your query" --limit 50
./spacemosquito search "how do we import prices from SAP" --mode semantic
./spacemosquito reindex --embeddings   # embed now instead of waiting for serve
```

REST and MCP also accept `limit` and `mode` (`GET /api/search?q=...&limit=50&mode=keyword`, MCP `confluence_search` `limit` and `mode` fields).

## Get a page by Confluence ID

```sh
./spacemosquito get-page 250347937
./spacemosquito get-page 42 TST

curl -s http://localhost:56403/api/pages/250347937
curl -s "http://localhost:56403/api/pages/42?space_key=TST"
```

## Commands

| Command | Description |
|---------|-------------|
| `init` | Create data directory, config, migrations |
| `bootstrap import-saved` | Rebuild SQLite catalog from existing `saved/` files |
| `serve` | Start API + MCP server |
| `crawl [--force] <url>` | Crawl a Confluence space (`--force` re-scrapes all pages and assets) |
| `crawl-page <space-key> <id>` | Refresh one page (always overwrite text + assets) |
| `search <query>` | Hybrid search (`--limit N`, `--mode hybrid\|keyword\|semantic`) |
| `get-page <id>` | Get page by Confluence ID (optional space key) |
| `reindex` | Rebuild FTS indexes (`--content` regenerates Markdown from saved HTML; `--embeddings [--space KEY]` updates the semantic index) |
| `delete-space [--delete-files] <space-key>` | Remove a space from catalog, search/MCP and cron (saved files kept unless `--delete-files`); also via 🗑 in the extension's Spaces tab |
| `stats` | Database statistics, semantic search coverage |
| `version` | Print build version |

Run `./spacemosquito` with no arguments for the full command list.

## Coming from Docker?

Docker Compose / PostgreSQL mode has been removed. To keep crawl artifacts without recrawling:

1. Wipe leftover containers/volumes (optional): [`scripts/cleanup-docker-legacy.sh`](scripts/cleanup-docker-legacy.sh) — see [`docs/guides/cleanup-docker-legacy.md`](docs/guides/cleanup-docker-legacy.md)
2. `./spacemosquito init`
3. Copy your old Compose bind-mount `saved-data/` (or `./saved`) → `~/.spacemosquito/saved/`
4. `./spacemosquito bootstrap import-saved`
5. `./spacemosquito reindex --content`
6. Point the extension at `http://localhost:56403` and run `./spacemosquito serve`

Useful flags:

```sh
./spacemosquito bootstrap import-saved --from /path/to/saved
./spacemosquito bootstrap import-saved --force
./spacemosquito bootstrap import-saved --dry-run
```

Import does **not** read PostgreSQL. If you only have a Postgres volume and no `saved/` tree, recrawl instead.

## Environment

| Variable | Purpose |
|----------|---------|
| `SPACEMOSQUITO_DATA_DIR` | Data directory (default `~/.spacemosquito`) |
| `CONFIG_PATH` | Config file path |
| `CHROMIUM_PATH` | Override browser executable |
| `SPACEMOSQUITO_NO_UPDATE_CHECK` | Any value: do not check GitHub for a newer release |

## Development

See [dev/README.md](dev/README.md). Quick checks:

```sh
make test
cd spacemosquito && go test -race -tags=integration ./internal/app/...
```
