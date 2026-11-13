# Status

Last updated: 2026-11-12

## Current focus

Release **v0.9.1** (Homebrew: model downloaded by the app cask, no separate model cask, `model install`; v0.9.0: semantic search: hybrid keyword + bge-m3 embeddings via llama.cpp, release archives; v0.8.10: crawl buttons stay enabled on long crawls, 1.5 s asset pause; v0.8.9: serve log file; v0.8.8: mentions on older pages; v0.8.7: @-mentions by name; v0.8.6: Confluence folders, throttling, faster downloads; v0.8.5: panels keep their content, block images, decision panels; v0.8.4: Jira links, reindex keeps attachment links; v0.8.3: converter and browse polish; v0.8.2: update notice; v0.8.1: attachments, Confluence authorship, wide tables, ADF decisions).

## Active task

**[semantic-search](tasks/semantic-search.md)** — issue [#9](https://github.com/infomosquitos/spacemosquito/issues/9), target v0.9.0. Released in v0.9.0. Open: evaluation on ~30 real questions (user), RRF tuning, then ADR-019 → Accepted.

## Blockers

_None._

## Next action

1. Pick **[#5](https://github.com/infomosquitos/spacemosquito/issues/5)** — link Confluence page references to local saved copies (now resolvable at render time) — **[#6](https://github.com/infomosquitos/spacemosquito/issues/6)** — multi-line table cells and emoticon shortnames, **[#7](https://github.com/infomosquitos/spacemosquito/issues/7)** — attachment text in search/MCP.
2. Confirm Dependabot alerts + security updates in GitHub Settings if not already on after org transfer.
