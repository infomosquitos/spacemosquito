# Guide: Configure MCP

SpaceMosquito exposes an **MCP** (Model Context Protocol) server over
**Streamable HTTP** so agents can search and read crawled Confluence content.

Default URL: `http://127.0.0.1:56403/mcp`

MCP tools read the **local catalog** (SQLite). Capture a session and crawl (or
import) spaces before expecting useful results. MCP does **not** start crawls;
use the CLI, browser extensions, or REST API for that.

> **Breaking:** HTTP+SSE (`GET /mcp` → `/mcp/session/<id>`) is no longer
> supported. Use Streamable HTTP (`POST /mcp` with JSON responses). See
> [ADR-017](../adr/017-mcp-transport-streamable-http.md).

## Prerequisites

1. `spacemosquito init` (once)
2. `spacemosquito serve` running
3. At least one space crawled (or `bootstrap import-saved`)

## Cursor

Add a remote MCP server — either:

- **Cursor Settings → Tools & MCP → Add**, or
- Edit `~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (project)

```json
{
  "mcpServers": {
    "spacemosquito": {
      "url": "http://127.0.0.1:56403/mcp"
    }
  }
}
```

## Claude Code

**CLI:**

```sh
claude mcp add --transport http spacemosquito http://127.0.0.1:56403/mcp
```

**Project** `.mcp.json` or user config (`~/.claude.json`):

```json
{
  "mcpServers": {
    "spacemosquito": {
      "type": "http",
      "url": "http://127.0.0.1:56403/mcp"
    }
  }
}
```

(`type: "streamable-http"` is accepted as an alias for `http`.)

Docs: [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp).

## Gemini CLI

Configure under `mcpServers` in `~/.gemini/settings.json` (user) or
`.gemini/settings.json` (project). Prefer **Streamable HTTP** (`httpUrl` /
http transport), not SSE.

**CLI** (flag names vary by Gemini CLI version — prefer http / streamable-http
over `sse`):

```sh
gemini mcp add --transport http spacemosquito http://127.0.0.1:56403/mcp
# optional: -s user  → write to ~/.gemini/settings.json
```

**settings.json** (example — confirm field names in current Gemini docs):

```json
{
  "mcpServers": {
    "spacemosquito": {
      "httpUrl": "http://127.0.0.1:56403/mcp"
    }
  }
}
```

Docs: [MCP servers with the Gemini CLI](https://google-gemini.github.io/gemini-cli/docs/tools/mcp-server.html).

## Other clients

Any client that supports **remote MCP over Streamable HTTP** can use:

```text
http://127.0.0.1:56403/mcp
```

Field names vary (`url`, `httpUrl`, `type: "http"` / `"streamable-http"`). No
API key is required for the default local bind. Do **not** use `--transport sse`.

## Tools

| Tool                     | Purpose                                                                                                                       |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `confluence_search`      | Hybrid search (keyword + meaning, [ADR-019](../adr/019-semantic-search-embeddings.md)): questions in natural language work. Args: `query` (required), optional `space`, `limit` (default 10), `mode` (`hybrid` default, `keyword` = BM25 with all terms required, `semantic`). Hits carry `match` and, for semantic matches, `heading_path`. |
| `confluence_list_spaces` | List crawled spaces.                                                                                                          |
| `confluence_list_space`  | Page summaries in a space (cursor pagination). Args: `space_key`, optional `limit`, `after_confluence_id`, `include_content`. |
| `confluence_get_page`    | Full page by `confluence_id`; optional `space_key` if IDs collide across spaces.                                              |

Typical flow: search → take `confluence_id` → `confluence_get_page`.

Search hits include `history.updated_by` / `history.updated_at` (last Confluence update) and `confluence_get_page` returns `history` with `created_by`, `created_at`, `updated_by` and `updated_at` when known, so agents can judge how current a page is. `updated_at` at the top level of a page is the local save time.

Page `content` is **Markdown** (same as CLI / REST).

## Smoke test without an IDE

```sh
curl -s -X POST "http://127.0.0.1:56403/mcp" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

```sh
curl -s -X POST "http://127.0.0.1:56403/mcp" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
```

Responses are JSON-RPC objects in the HTTP body (`Content-Type: application/json`).
`GET /mcp` returns **405**.

## Troubleshooting

| Symptom                 | Check                                                                                    |
| ----------------------- | ---------------------------------------------------------------------------------------- |
| Client cannot connect   | `spacemosquito serve` running? URL host/port match `mcp.host` / `mcp.port`? Use **http** / Streamable HTTP, not SSE. |
| 405 on GET /mcp         | Expected — clients must **POST** JSON-RPC to `/mcp`.                                     |
| 403 forbidden origin    | Browser sent a non-loopback `Origin`; bind locally or call without that Origin.          |
| Tools empty / no spaces | Crawl or import at least one space; `spacemosquito stats`                                |
| Search returns nothing  | Reindex after upgrade: `spacemosquito reindex` (and `--content` if Markdown looks wrong) |
| Want LAN access         | Set `mcp.host: "0.0.0.0"` and use the machine’s IP in the client URL — **no auth**; Origin checks still reject non-loopback browser Origins |

## Related

- [README](../../README.md) — install, crawl, search
- [ARCHITECTURE.md](../ARCHITECTURE.md) — MCP package overview
- [ADR-017](../adr/017-mcp-transport-streamable-http.md) — Streamable HTTP transport
