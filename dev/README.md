# Development

How we run this project: ground rules, setup, cleanup, release, and product-specific notes.

## Ground rules

- Breaking changes in API are acceptable.
- When writing documentation use Mermaid for diagrams.
- When cleaning finished or cancelled work, follow [`CLEANUP.md`](CLEANUP.md). When cutting a version, follow [`RELEASE.md`](RELEASE.md).

## What's in this folder

| Doc | Role | Lifetime |
|-----|------|----------|
| [`README.md`](README.md) (this file) | Setup, ground rules, product notes | Durable |
| [`CLEANUP.md`](CLEANUP.md) | Promote durable content; remove finished tasks | Durable |
| [`RELEASE.md`](RELEASE.md) | Version changelog, bump extensions, tag / GitHub Release | Durable |
| [`BACKLOG.md`](BACKLOG.md) | Flat task index | Living — cleaned via CLEANUP.md |
| [`STATUS.md`](STATUS.md) | Current focus / blockers / session notes | Living — refreshed each session |
| [`tasks/`](tasks/) | Optional detail for a backlog item | Temporary — deleted on cleanup |

## Local Development & Build

```sh
cd spacemosquito
go build ./cmd/server
go build ./cmd/cli
go build ./cmd/spacemosquito
```

Release binaries (cross-compile, embedded SQLite migrations):

```sh
cd spacemosquito
./scripts/build-release.sh v0.2.0
ls dist/
```

## Browser extensions build

Each extension (`firefox-extension/`, `chrome-extension/`) builds on its own — config is duplicated, no shared build package:

```
npm run build  →  tsc --noEmit (TypeScript 7, typecheck only)  &&  node esbuild.mjs (bundles background + popup)
```

Node `>=16.20.0` (`engines`); CI uses Node 22. Dependabot may bump TypeScript majors — the `Extension Build` CI workflow is the gate.

## Run unit tests

```sh
cd spacemosquito
go test ./...
```

With the race detector (same as CI):

```sh
cd spacemosquito
go test -race ./...
```

## Integration tests (REST + MCP, in-process)

Requires the `integration` build tag. Boots a real SQLite DB with embedded migrations, seeds fixtures, and exercises HTTP + MCP Streamable HTTP.

```sh
cd spacemosquito
go test -race -tags=integration ./internal/app/...
```

Not run in CI by default.

## Search

`search.Service` (`internal/search/service.go`) serves MCP, REST and the CLI with `mode` = `keyword` | `semantic` | `hybrid` (default `hybrid`; falls back to `keyword` when semantic search is off, not installed, not indexed yet, or the helper fails).

Keyword (`store.SearchPages`, FTS5/BM25):

- Multi-word queries use **AND** — all terms must appear in title or body.
- **Title is weighted 10×** over body in BM25 ranking.
- Default result limit is **10** (CLI: `--limit N`; REST: `?limit=N`; MCP: `limit` field).
- If FTS returns no rows, search falls back to case-insensitive **title substring** match.

Semantic (ADR-019, `internal/embed`):

- `ChunkPage` splits `pages.content` by headings (~1,200 bytes ≈ 360 tokens; tables and code blocks kept whole, oversized tables split by rows with the header repeated) and embeds `title + heading path + chunk`.
- `spacemosquito-embedder` (`cmd/spacemosquito-embedder`, the only package importing yzma/llama.cpp — enforced by `TestMainBinaryDoesNotLinkLlama`) loads `lib/` + the GGUF model and answers length-prefixed JSON frames on stdin/stdout. `embed.Process` starts it lazily, restarts it after crashes (backoff), stops it after 10 idle minutes; it exits on stdin EOF, so it never outlives `serve`.
- `Locate` finds helper, libs and model: `embedder.*_path` config → next to the (symlink-resolved) executable (`lib/`, `models/`) → `<prefix>/share/spacemosquito/models` (Homebrew model cask) → `~/.spacemosquito/{lib,models}`.
- Storage: `page_chunks` (text, heading path, content hash, float32 vector) and `page_embeddings` (model + sha256 of title and content per page), both cascading with pages (migration 014).
- `Indexer.Backfill` embeds pages whose source hash or model differs, re-embedding only chunks whose hash changed. `serve` runs it in `embed.Background`: at start, 3 s after crawl page saves (`CrawlJobManager.PageSaved`), and every minute (catches cron and `reindex --content`); `reindex --embeddings` runs it in the foreground.
- Query: embed the query, brute-force dot product over all chunk vectors in memory (reloaded after index changes), best chunk per page; hybrid merges BM25 top 50 and vector top 50 with RRF (k = 60). Semantic-only hits get their best chunk as excerpt plus `heading_path`; every hit reports `match`.
- Real-model test: `SPACEMOSQUITO_LLAMA_LIB=… SPACEMOSQUITO_LLAMA_MODEL=… go test -tags=llama ./internal/embed -run Llama`. Libraries for a platform: `scripts/fetch-llama-libs.sh <goos> <goarch> <dir>`.

## Page content (Markdown)

Crawls and imports store page body text as **Markdown** (`content.md` on disk, `pages.content` in the DB) using HTML→Markdown conversion — not flat `doc.Text()` extraction. This preserves paragraph boundaries and improves search/MCP readability.

```
raw.html (CSF, API crawls)           →  csf.CSFToMarkdown()        →  content.md + pages.content  →  FTS
index.html (browser-fallback pages)  →  contentmd.HTMLToMarkdown() →  content.md + pages.content  →  FTS
```

Nothing else is generated per page: the browse UI renders `content.md` on request (ADR-018). Markdown navigation for agents (`saved/index.md`, `saved/<SPACE>/index.md`) is rebuilt from the DB page tree by `catalogindex` during crawls and in `reindex --content`, which also removes HTML generated by versions before 0.8 (`catalogindex.RemoveLegacyHTML`; browser-fallback pages keep their `index.html`, the source of their Markdown).

`serve` renders the catalog at `GET /browse/` (`internal/browse`): `/browse/` lists spaces, `/browse/<KEY>/` shows the page tree (first level open, child counts, title filter, expand/collapse) and full content search (`?q=` pre-fills it), `/browse/<KEY>/<page dir>/` renders `content.md` with goldmark (raw HTML dropped) plus a server-rendered tree sidebar (path to the current page open) and a top-bar quick search (`/api/search?space_key=…&highlight=1&limit=10`, `/` to focus). The tree is built per request from the DB (`catalogindex.LoadSpace`), so it grows during crawls. Only `assets/**`, `content.md` and `raw.html` (as `text/plain`) are served from page directories, all with `Content-Security-Policy: sandbox`; `…/index.html` redirects to the page directory; no listings. CSS/JS are embedded (`/browse/.static/`, cache-busted by content hash). `GET /api/spaces` reports `browse_path` once `saved/<KEY>` exists, which a crawl creates when it starts; the extension's Spaces tab shows 📖 for it. `export-html` renders the same views to static files (relative `…/index.html` links, assets copied, no search). Browsers do not let extensions open `file://` pages, hence the HTTP route.

Removing a space (`spaceops.Delete`, used by `DELETE /api/spaces/{key}[?delete_files=1]`, the `delete-space` CLI and the extension 🗑 button) drops the space row, its pages and FTS entries (DB cascade + trigger), its per-space cron config, and its root-index entry; `saved/<KEY>` is deleted only on request. Keys must match `^~?[A-Za-z0-9_-]+$` so the folder can never resolve outside `saved/`. The REST delete returns 409 while that space is being crawled; the CLI cannot see `serve`'s crawls.

Served index pages also show a content search box calling `GET /api/search` (hidden for `file://`). REST search adds `browse_url` per hit and, with `highlight=1`, keeps match markers U+E000/U+E001 in `excerpt` (`search.MatchStart`/`MatchEnd`; stripped by default, so MCP/CLI output is unchanged). The page renders results with DOM `textContent` only.

Regenerate existing catalogs after upgrade:

```sh
./spacemosquito reindex --content
```

## Testing with curl

MCP is `POST /mcp` with a JSON body (not a long-lived stream). Example:

```sh
curl -s -X POST http://localhost:56403/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

Get a page by Confluence ID (REST):

```sh
curl -s http://localhost:56403/api/pages/42
curl -s "http://localhost:56403/api/pages/42?space_key=TST"
```

## Dependency vulnerabilities

Use **GitHub Dependabot** for:

- **Alerts** + **security-update PRs** (enable in Settings → Code security and analysis)
- **Monthly version bumps** — [`.github/dependabot.yml`](../.github/dependabot.yml) (gomod under `/spacemosquito`, npm for both extensions, GitHub Actions; grouped, limit 3 open PRs per ecosystem)

Expect separate PRs for `chrome-extension` and `firefox-extension` when both are
affected — that is OK for now.

CI:

- [`go-test.yml`](../.github/workflows/go-test.yml) — `go test -race` (+ warn-only `govulncheck`)
- [`extension-build.yml`](../.github/workflows/extension-build.yml) — `npm ci` + `npm run build` for both extensions
- [`deps-audit.yml`](../.github/workflows/deps-audit.yml) — warn-only `govulncheck` + `npm audit --audit-level=high`
- Release tags also package extensions and update the Homebrew cask in [`infomosquitos/homebrew-tap`](https://github.com/infomosquitos/homebrew-tap) (see [`release.yml`](../.github/workflows/release.yml))

## Coming from Docker

Docker / Postgres packaging is removed. See [README.md](../README.md#coming-from-docker).
