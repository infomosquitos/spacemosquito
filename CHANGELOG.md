# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.1] - 2026-11-13

### Added
- `spacemosquito model install [--dir DIR]` downloads the semantic search model (635 MB, checksum-verified; skipped when already installed). Use it for archive installs on Linux and Windows.

### Fixed
- Homebrew: `brew upgrade` to 0.9.0 stopped with "Refusing to load cask … spacemosquito-bge-m3-embeddings-model from untrusted tap". The separate model cask is gone; the `spacemosquito` cask downloads the model itself at install, once (upgrades keep it). If you trusted and installed the model cask: `brew uninstall --cask spacemosquito-bge-m3-embeddings-model` removes the model too, so follow it with `spacemosquito model install --dir "$(brew --prefix)/share/spacemosquito/models"`.

## [0.9.0] - 2026-11-12

### Added
- **Semantic search**: search understands questions, not only keywords, in any language ("how is the cart recalculated when prices are outdated" finds the order simulation pages). Search is now hybrid by default — exact terms, IDs and titles still rank first, pages that match by meaning fill in — in MCP `confluence_search`, REST `/api/search`, the browse search bars and `spacemosquito search`. Choose with `mode` (`hybrid`, `keyword`, `semantic`); results say how each page matched and, for meaning matches, the section.
- Pages are embedded locally with the multilingual bge-m3 model through llama.cpp (Metal on Apple Silicon): `serve` indexes in the background (about 10 minutes per 1,000 pages the first time, then only changed pages). Nothing leaves your machine. `spacemosquito stats` shows coverage; `reindex --embeddings` indexes in the foreground; `embedder.provider: none` turns it off.

### Changed
- Releases are archives (`spacemosquito-<os>-<arch>.tar.gz`, `.zip` on Windows) holding the CLI, its embedding helper and the llama.cpp libraries. Homebrew also installs the model (`spacemosquito-bge-m3-embeddings-model`, 635 MB) once; app upgrades do not download it again. See docs/INSTALL.md for other platforms.
- The unused `embedder` config placeholders (`model`, `openai`, `bge`) are replaced by `provider`, `model_path`, `lib_path`, `helper_path`, `gpu_layers` and `threads`; old keys are ignored.

## [0.8.10] - 2026-10-23

### Changed
- The default pause before each image/attachment download is 1.5 s (was 2.5 s); `storage.asset_delay_ms` still overrides it, and throttling by Confluence still slows downloads down.

### Fixed
- The extension's ▶ (crawl) and ⏱ (cron) buttons no longer turn disabled during and after a crawl longer than an hour: a crawl that keeps getting pages from Confluence renews the session's validation (every 5 min), which used to expire 60 min after the last capture.

## [0.8.9] - 2026-10-22

### Added
- `spacemosquito serve` also writes its log to `~/.spacemosquito/logs/serve.log`, rotated at 10 MB with 3 old files kept, so it outlives the terminal. Configure with `log_file` in `config.yaml` (`""` = terminal only).

## [0.8.8] - 2026-10-22

### Fixed
- @-mentions on older pages, which store the account ID as `ri:userkey`, also show names (they showed `@712020:…`).

## [0.8.7] - 2026-10-22

### Fixed
- @-mentions on Confluence Cloud show the person's name (`@Ann Larsen`) instead of nothing: crawls look up mentioned people once (bulk request, cached in the database; page authors are known for free) and `reindex --content` uses the cached names. Unknown accounts show as `@unknown user`.

## [0.8.6] - 2026-10-22

### Changed
- Faster crawls: the pause before each image/attachment download is 2.5 s instead of 5 s, configurable as `storage.asset_delay_ms` (`-1` = no pause).

### Added
- Confluence Cloud folders: crawls look up the folders pages sit in (one request per folder) and the page tree, sidebar and `index.md` show them with 📁; a folder page lists its contents. Pages inside folders no longer appear at the top level of the tree. Existing spaces get their folders on the next crawl.
- Throttling support: when Confluence answers HTTP 429, page requests and downloads wait as long as its `Retry-After` header asks and retry; downloads also slow down for the rest of the crawl (pause doubled, up to 30 s).

### Fixed
- Images pasted from another Confluence page are downloaded from that page (looked up by title, once per crawl) instead of failing with HTTP 404.
- Downloads that cannot succeed (404 and other client errors) are no longer retried four times with growing pauses, which slowed crawls down; rate limits and server errors are still retried.
- The server no longer logs an error when the extension asks for the session of a page that is not a Confluence site (e.g. the browse UI).

## [0.8.5] - 2026-10-22

### Changed
- Decision lists show as one light green box (dark green in dark mode), each decision on its own line, like Confluence's decision panel.

### Fixed
- Content of Info/Note/Warning panels, lists and tables no longer slips out of its block after an empty paragraph (Confluence writes `<p />`, which HTML parsing left open); some previously missing sections reappear.
- Images placed as their own block, and block/embed link cards, stay on separate lines instead of running together; image captions are shown below the image.
- Status labels are shown in capitals as in Confluence (`MODERATE`); titles typed in mixed case ("moderATE") no longer show through.

## [0.8.4] - 2026-10-22

### Fixed
- Jira issue macros link to the issue again on Confluence Cloud (`JIRA: [CCP-658](https://<site>.atlassian.net/browse/CCP-658)`); Jira query macros link to the matching issue search. Server/Data Center keeps the plain key (the Jira URL is unknown there).
- `reindex --content` keeps attachment links (file previews, attachment links, the attachments list) and Jira links: it now rebuilds the page context from `metadata.json` instead of converting without it.

## [0.8.3] - 2026-10-22

### Changed
- Page trees mark pages without children with a dot, aligned with the arrows of expandable pages.
- Horizontal rules on saved pages are left-aligned with the text instead of centered.
- Info, Note and Tip panels on saved pages show as light blue boxes and Warning panels as light rose boxes (darker shades in dark mode), like Confluence's panels.

### Fixed
- Emoji stored as escaped codes (e.g. `\uD83D\uDCD8` for 📘) or as Atlassian names (`:check_mark:`, `:info:`, `:question_mark:`, `:warning:`, `:light_bulb_on:`) show as emoji; unknown names are left out instead of shown as text.
- Smart links shown as cards in Confluence are separated from the following word ("…SAPCC for" instead of "…SAPCCfor").
- Words no longer run together around bold, italic, links and inline code when Confluence keeps the space inside the formatting (e.g. `Define<strong> data loading </strong>for` became `Define**data loading**for`). Run `spacemosquito reindex --content` to fix saved pages.

## [0.8.2] - 2026-10-22

### Added
- Update notice: `spacemosquito serve` (in the background, without delaying startup) and `spacemosquito version` check GitHub for a newer release at most once a day and print how to upgrade (`brew upgrade --cask spacemosquito && spacemosquito version` for Homebrew installs, otherwise the download link); other commands repeat the cached result on stderr. Off with `update_check: false` or `SPACEMOSQUITO_NO_UPDATE_CHECK=1`.

## [0.8.1] - 2026-10-22

### Added
- Page attachments are saved: crawls download each page's files (pptx, pdf, xlsx, docx, …) into `assets/attachments/` and list them in `metadata.json`. File previews and attachment links on the page link the local copy (PDFs show inline in browse; Office files download), the attachments macro lists the files, and every page shows an **Attachments** section. Files over `storage.max_attachment_mb` (default 100) are linked to Confluence instead. Pages saved before need `crawl --force` once (adding an attachment does not change the page version).
- Page authorship from Confluence: who created and last updated each page, and when. Shown as a byline under the page title in browse, saved in `metadata.json` (`author`, `created_at`, `updated_by`, `updated_at`; previously empty or the local save time), and returned as `history` by MCP/REST search and `get_page`. The next crawl fills it in for every page, including unchanged pages it skips; no `crawl --force` needed.

### Changed
- Wide tables on saved pages use the full width next to the sidebar (text keeps a readable width), and URLs in cells wrap instead of widening their column. Tables that are still too wide scroll sideways, with a shadow on the edge that hides more columns.

### Fixed
- Decision lists and panels from Confluence's newer editor no longer show internal IDs (e.g. `d7a0fb66-…05e99ba7-…DECIDED`) in saved pages: decisions render as **Decision:** items and panels as callouts. Run `spacemosquito reindex --content` to fix saved pages (no crawl needed).
- `reindex --content` only writes inside the configured catalog; with a copied or moved catalog it no longer rewrites `content.md` in the folders recorded in the database.

## [0.8.0] - 2026-10-22

### Changed
- Saved pages are rendered on request by `serve` from `content.md` ([ADR-018](docs/adr/018-browse-rendered-on-request.md)): browse UI changes reach every page after an upgrade and restart, with no reindex. Crawls no longer write `index.html` for API-saved pages, nor `saved/index.html`, `saved/<SPACE>/index.html` or `tree.js`; the `index.md` trees for agents stay. Run `spacemosquito reindex --content` once to remove HTML generated by earlier versions (old `…/index.html` links redirect).
- Search results (REST, MCP, CLI) report `file_path` as the page's `content.md`.
- Opening `~/.spacemosquito/saved/index.html` from disk no longer works; use 📖 / `http://localhost:56403/browse/` or `export-html`.
- The page tree sidebar shows the space's page count and last crawl time under the space name.

### Added
- `spacemosquito export-html [--space KEY] <dir>`: static copy of the catalog (page tree sidebar, images) for browsing without `serve`.

## [0.7.7] - 2026-10-22

### Added
- Content search on every saved page: a top bar above the page (press **/**) shows matching pages with highlighted excerpts as you type; **See all results** opens the space's full result list. Available through 📖 / `http://localhost:56403/browse/` (needs `serve`). Comes with the page tree sidebar, so after upgrading crawl the space or run `spacemosquito reindex --content` once.

## [0.7.6] - 2026-10-22

### Added
- Confluence-style page tree sidebar on every saved page: the current page is highlighted and its parents expanded, with a title filter and expand/collapse all; on narrow windows it opens from a ☰ Pages button. Run `spacemosquito reindex --content` once to add it to pages saved by earlier versions (a normal crawl only rewrites changed pages).

### Changed
- Space page tree is easier to navigate: **Expand all** / **Collapse all** buttons, a larger ▶ toggle, child page counts next to parent pages, and the first level opens by default. Clearing the title filter returns to that default view.

## [0.7.5] - 2026-10-22

### Fixed
- Crawls started from the extension now write the space's page tree when they start, refresh it as pages are saved, and update it when they finish or are stopped, so 📖 appears during the crawl. Previously only CLI and scheduled crawls updated the tree, and a first crawl of a space showed no 📖.

## [0.7.4] - 2026-10-22

### Changed
- 📖 in the extension's **Spaces** tab is available while a crawl is still running: the space's page tree is written when the crawl starts and refreshed as pages are saved (reload it to see newer pages).

## [0.7.3] - 2026-10-22

### Added
- Remove a space: 🗑 in the extension's **Spaces** tab (with confirmation and an optional "also delete saved files"), `spacemosquito delete-space [--delete-files] <key>`, or `DELETE /api/spaces/{key}?delete_files=1`. Removes its pages from search and MCP, its crawl schedule and its catalog index entry; refused while the space is being crawled.

## [0.7.2] - 2026-10-22

### Added
- Content search in the browse pages: space and catalog indexes served at `http://localhost:56403/browse/` search page text (same full-text search as MCP) and highlight matches. REST `/api/search` returns `browse_url` per result and supports `highlight=1`.

## [0.7.1] - 2026-10-22

### Fixed
- Confluence pages without content of their own (typically parents that only group other pages) are saved as empty pages; their `index.html` lists the child pages. Previously they fell back to the headless browser, which saved Jira's "Oops, you've found a dead link" page instead.
- "View in Confluence" links and saved `confluence_url` on Confluence Cloud include `/wiki` (they led to a 404).
- The browser fallback no longer saves Atlassian error pages as page content.
- Page tree keeps its full depth: parents are recorded during discovery, so pages saved via the browser fallback no longer appear at the top level of the space index.

Run `spacemosquito crawl --force <space URL>` once to repair pages saved by earlier versions.

## [0.7.0] - 2026-10-22

### Added
- Homebrew install on macOS: `brew install --cask infomosquitos/tap/spacemosquito`.
- Offline catalog navigation: `saved/index.html` lists spaces, each space gets an `index.html` page tree with a title filter (plus `index.md` versions), and pages link back to their space. Built after each crawl and by `reindex --content`.
- Extension **Spaces** tab: 📖 button opens a space's saved page tree in a new tab, served by `spacemosquito serve` at `http://localhost:56403/browse/`.

### Fixed
- Saved `index.html` for API-crawled pages now opens in a browser with images, diagrams, code blocks and tables (was raw Confluence markup). Run `spacemosquito reindex --content` to fix existing pages.
- Numbered lists that Confluence continues after other content (e.g. step 2 after a screenshot) keep their numbering in `content.md`.

### Changed
- HTTP request start/complete logs are **debug** (was info) to reduce spam.
- **Breaking:** `GET /api/session/status` requires `?url=`; `POST /api/session/validate` requires `confluence_url`. Unknown `session.enc` payloads are rejected (delete the session and re-capture).
- Browser extensions build with **TypeScript 7** + **esbuild** (`tsc --noEmit` then bundle); webpack/`ts-loader` removed.

## [0.6.0] - 2026-09-24

### Changed
- **Breaking**: Default HTTP/MCP listen port is **56403** (was 8081). Update extension backend URL and MCP client configs.

## [0.5.0] - 2026-09-17

### Changed
- **Breaking:** MCP transport is Streamable HTTP only (`POST /mcp` JSON). Deprecated HTTP+SSE (`GET /mcp` + `/mcp/session/<id>`) removed. Reconfigure agents with http / streamable-http (not SSE). See [Configure MCP](docs/guides/configure-mcp.md).

## [0.4.0] - 2026-09-16

### Added
- Toolbar icon animates (mosquito sucking blood) while any crawl is running.
- Spaces tab tooltip shows `Stored: N CSF · M HTML`.
- Extension Page tab shows stored body format (**CSF** / **HTML**).
- Extension popup remembers last tab (Page / Spaces / Settings).
- Extension toolbar icon: **green** / **gray**.
- Extension light/dark themes.
- New setting: auto-renew Confluence session.

### Changed
- Extension brand accents + active icon to muted green (`#4a7a5c`).
- Extension icon set.
- Extension popup major redesign.

### Fixed
- Spaces list could show crawled > total (e.g. 120/118) after a re-crawl.
- Session disc could stay green after Confluence auth died.
- Extension Page tab: Misdetection of the Confluence Cloud `homepageId`.
- Extension popup backend probe failure.

## [0.3.4] - 2026-09-04

### Fixed
- Several issues related to self-hosted wikis

### Changed
- On-disk page directories naming switched to `{confluence-id}-{title}`

### Added
- Support for capturing sessions from multiple confluence hosts.

## [0.3.3] - 2026-08-21

### Changed
- Firefox releases ship a signed `.xpi` for persistent install via Install Add-on From File (replacing temporary zip + `about:debugging` for end users).
- Browser extension display name is **Space Mosquito**.

## [0.3.2] - 2026-08-04

### Fixed
- Firefox extension Capture Session fixes.

## [0.3.1] - 2026-08-04

### Fixed
- Firefox extension Capture Session fixes.

## [0.3.0] - 2026-08-04

### Added
- `spacemosquito crawl-page <space-key> <confluence-id>` refreshes one page via the REST API, always overwriting local text and assets.

### Fixed
- Session Validate fixes.
- `GET /api/cron/config` panics fixes.

## [0.2.0] - 2026-08-03

### Added
- GitHub Releases and local `build-release.sh` publish Firefox/Chrome extension zips.

### Fixed
- Crawl Cancel now really cancels the started crawl.

### Changed
- Crawl skips already-downloaded non-empty assets. `crawl --force` re-scrapes every page and re-downloads every asset.

## [0.1.0] - 2026-08-02

### Added
- Rule-based **Confluence Storage Format → Markdown** converter (`internal/contentmd/csf`): macros (code, panel/info/note/warning/tip, jira, status, expand, toc, draw.io), task lists, native tables/links/emphasis, images and draw.io diagrams extracted as assets, emoticons mapped to Unicode. Replaces the generic converter on the API path; browser-fallback rendered HTML still uses the generic converter, selected by a format sniff.
- `metadata.json` records `body_format` (`storage`/`rendered`); `reindex --content` and `bootstrap import-saved` route each page to the correct converter (sniffing `raw.html` for legacy pages).
- `metadata.json` `diagrams` field for extracted draw.io previews.
- Chrome extension port (Manifest V3).
- REST API-first crawling with headless fallback.
- Multi-flavor support (Cloud / Server / Data Center).
- Standardized MCP SSE transport.
- Variable expansion in `config.yaml`.
- `AGENTS.md` for local development.

### Changed
- Removed the 50 KB `pages.content`/`content.md` truncation cap; full Markdown is stored (disk + DB + FTS).
- Unified all HTTP services on port 8081.
- Refactored session deletion for Docker compatibility.
- Broadened URL detection for custom domains.

### Fixed
- Asset downloads (images, diagrams, attachments) now send the session cookies, so SSO-protected instances return the file instead of a login page. Responses that are HTML (a login/redirect page) are rejected as a loud error rather than saved as a corrupt image.
- "Page Not Found" errors by using direct API extraction.
- "Device or resource busy" mount errors.
- Standardized MCP JSON-RPC handshake.
