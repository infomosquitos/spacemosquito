# Architecture Decision Records

ADRs capture **durable** choices (stack, boundaries, contracts) that should outlive any single task.

## When to write an ADR

- Choosing or changing language, frameworks, hosting, or major libraries.
- Defining system boundaries or data/API contracts that other work depends on.
- Recording a trade-off so future agents do not re-litigate it in every task.

Prefer a **task doc** (`dev/tasks/…`) for short-lived design while building a feature. On cleanup, promote lasting decisions from the task doc into an ADR (or FAQ / layer README) before deleting the task file — see [`dev/CLEANUP.md`](../../dev/CLEANUP.md).

## Conventions

- Files: `docs/adr/adr-NNN-short-title.md` (zero-padded number, kebab-case title).
- Status: `Proposed` | `Accepted` | `Superseded` | `Deprecated`.
- Link related ADRs and FAQ pages.
- Copy [`TEMPLATE.md`](TEMPLATE.md) for a new record.

## Index

| ADR | Title | Status |
|-----|-------|--------|
| 001 | Firefox extension — WebExtensions API | Accepted |
| 002 | Hybrid authentication approach | Accepted |
| 004 | Headless browser for scraper | Accepted |
| 005 | Session storage — encrypted file | Accepted |
| 007 | MCP transport — SSE + HTTP | Superseded by 017 |
| 008 | Configuration format — YAML | Accepted |
| 009 | Database migrations — golang-migrate | Accepted |
| 010 | HTML extraction — Trafilatura-style | Accepted |
| 011 | Go backend language | Accepted |
| 013 | Go Rod over ChromeDP | Accepted |
| 015 | Saved page format with Markdown | Accepted (`index.html` / HTML navigation superseded by 018) |
| 016 | SQLite-only distribution | Accepted |
| 017 | MCP transport — Streamable HTTP | Accepted |
| 018 | Browse pages rendered on request from content.md | Accepted |
| 019 | Semantic search — embedded llama.cpp + bge-m3, hybrid with BM25 | Proposed |
