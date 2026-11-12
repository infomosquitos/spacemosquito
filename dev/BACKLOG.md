# Backlog

Flat task list. Status values: `backlog` | `ready` | `in_progress` | `blocked` | `done` | `cancelled`.

On cleanup, move shipped work to [`CHANGELOG.md`](../CHANGELOG.md) and clean the backlog — see [`CLEANUP.md`](CLEANUP.md). On version cut, see [`RELEASE.md`](RELEASE.md).

Reorder freely. Cancel by setting status to `cancelled`. Unexpected work becomes a new task with its own ID.

## Format

Each task:

```markdown
## <task-id>
- status: backlog
- parent: <optional-related-task-id>
- blocked_by: <optional-task-id>
- goal: One sentence
- done_when: Observable acceptance criteria
- notes: Optional one-liner
```

Detail lives in `dev/tasks/<task-id>.md` when needed (no `task-` filename prefix).

---

## cme-markdown-gap-analysis
- status: ready
- goal: Extract confluence-markdown-exporter conversion behaviors (tests + Converter) and gap-analyze vs our CSF→Markdown rules; recommend follow-ups only.
- done_when: CME capability inventory, ours crosswalk, severity-rated gap matrix, and ≤10 ranked implement/defer/ignore recommendations; no port in this task.
- notes: Detail in [`tasks/cme-markdown-gap-analysis.md`](tasks/cme-markdown-gap-analysis.md).

## semantic-search
- status: in_progress
- goal: Semantic + hybrid search with bge-m3 embedded via llama.cpp (yzma helper process), shipped pre-packaged; issue #9.
- done_when: Fresh brew install backfills embeddings and hybrid search finds paraphrased questions; `provider: none` = v0.8.10 behavior; MCP/REST `mode`; released as v0.9.0.
- notes: Detail in [`tasks/semantic-search.md`](tasks/semantic-search.md); decision [ADR-019](../docs/adr/019-semantic-search-embeddings.md).
