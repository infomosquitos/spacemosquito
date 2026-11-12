# ADR-019: Semantic Search with Embedded llama.cpp and bge-m3 (Hybrid with BM25)

- **Status:** Proposed
- **Date:** 2026-11-12
- **Related:** [ADR-016](016-sqlite-only-distribution.md) (SQLite only, static binary), [ADR-017](017-mcp-transport-streamable-http.md) (MCP), [issue #9](https://github.com/infomosquitos/spacemosquito/issues/9), [`dev/tasks/semantic-search.md`](../../dev/tasks/semantic-search.md) (design, spike results)

## Context

Search everywhere (MCP `confluence_search`, REST `/api/search`, browse, CLI) is SQLite FTS5 with BM25: the title is weighted 10× and multi-word queries are AND. It is strong for exact terms, IDs (`CCP-761`) and titles. It is weak for questions phrased differently from the page ("price updates from SAP" vs "ZCONDA -> PriceRow Import"), and those are exactly what agents ask over MCP.

Constraints:

- The main binary is static (`CGO_ENABLED=0`) and SQLite only.
- Everything runs locally, and the content is company-internal.
- A catalog is roughly 1,000 pages per user.
- Installation should need no extra setup.

## Decision

| Area | Choice |
|------|--------|
| Model | **bge-m3** (multilingual, 1024 dims), GGUF Q8_0 from `ggml-org`. |
| Runtime | **llama.cpp loaded at runtime** through [yzma](https://github.com/hybridgroup/yzma) (purego/ffi, no CGO). It uses Metal on Apple Silicon and the CPU elsewhere. |
| Process | yzma runs in a **helper binary, `spacemosquito-embedder`**, that `serve` starts on demand and talks to over stdin/stdout. The main binary never links native-loading code. |
| Shipping | **Pre-packaged, no runtime downloads.** The llama.cpp shared libraries and the helper ship in the release archive and the `spacemosquito` cask. The model ships in its own cask, `spacemosquito-bge-m3-embeddings-model`, which the main cask depends on, so app upgrades do not download the model again. The model file is an asset of this repository's release `model-bge-m3-q8_0-v1` (republished from the pinned upstream file, sha256-checked), so installs depend only on GitHub. |
| Default | **On.** `serve` embeds pages missing vectors in the background, and search defaults to `hybrid`. `embedder.provider: none` turns it off. |
| Unit | **Chunks** of `content.md`, split by headings (~350 tokens, small overlap, tables and code blocks kept whole). Each is embedded with the page title and heading path. |
| Storage | A `page_chunks` table holds the text, heading path, content hash, model, dims and a float32 vector BLOB. Rows are deleted together with their page. |
| Vector search | Brute-force dot product in Go over an in-memory matrix, loaded lazily and reloaded after chunk writes. No SQLite extension. |
| Ranking | **Hybrid:** BM25 and vector results are merged with Reciprocal Rank Fusion. The excerpt is the best chunk with its heading path. `mode` is `keyword`, `semantic` or `hybrid`, and falls back to `keyword` when vectors are unavailable. |
| Indexing | Incremental: after each saved page, only chunks whose hash changed are embedded. A slow or broken embedder never fails a crawl. `reindex --embeddings` re-embeds in the foreground. |

### Why a helper process

The yzma dependency `jupiterrider/ffi` loads libffi in a package `init()` and panics when it is missing (Linux without `libffi.so.8`). Linked into the main binary, it would break every command on such machines. The helper also confines native crashes to a process that `serve` restarts, and it lets `serve` free the model's ~700 MB of memory when idle.

## Alternatives considered

| Option | Why not |
|--------|---------|
| Ollama over HTTP | An extra application to install and run; the user wants everything shipped with spacemosquito. |
| Linking llama.cpp with CGO | Breaks the static, cross-compiled binary (ADR-016) and needs a C++ toolchain for each platform in CI. |
| Pure-Go inference | CPU only, slow, and the inference code would be ours to maintain. |
| yzma inside the main binary | libffi `init()` panic on Linux, and native crashes would take down `serve` (see above). |
| `sqlite-vec` or a vector database | Needs a loadable extension, CGO or a second service; brute force is enough at about 5,000 vectors. |
| Downloading libs and model on first use | The user wants no runtime downloads, and the content may sit behind restrictive networks. |
| One package including the model | Every `brew upgrade` would download about 650 MB. |
| Semantic only | Loses the exact ID and title matches that keyword search gets right today. |
| Whole-page embeddings | Long pages dilute meaning, and agents get no precise excerpt to cite. |

## Consequences

- **Easier:**
  - People and agents can find pages with questions phrased by meaning.
  - Agents get a citable chunk together with its heading path.
  - The same vectors can later power "related pages" and attachment text (#7).
- **Harder:**
  - Installs are larger: about 15–40 MB of libraries per platform plus a 635 MB model cask.
  - The first backfill uses a few minutes of GPU or CPU time (estimated 7 min on Metal and 20 min on CPU for 956 pages).
  - Releases depend on upstream llama.cpp builds pinned by yzma, which must be bumped together.
  - The ranking needs tuning on real questions.
- **Follow-ups:**
  - Run the evaluation set (~30 real questions; recall@5 and MRR for keyword vs hybrid) and record it here before setting Status to Accepted.
  - Optionally add other providers (Ollama, OpenAI-compatible) behind the same `Embedder` interface.
