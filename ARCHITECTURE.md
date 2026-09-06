# goGioIa — Architecture

This document explains how the application is put together and how data
flows through it, end to end. For the API table, config reference and deploy
steps see [README.md](README.md); this file focuses on *why* the code is
shaped the way it is.

## 1. What this is

goGioIa is a single self-contained Go binary that serves:

1. A **chat UI** (Vue 3 SPA, embedded) that streams completions from a local
   **Ollama** instance.
2. A **RAG assistant** that answers questions grounded in PDFs ingested into
   an **Oracle 23ai** vector store (embeddings via `nomic-embed-text`,
   generation via `mistral`).
3. An **operations dashboard** (read-only observability UI) over the RAG
   pipeline: health, ingestion, tokens, retrieval quality, query traces,
   models, and data-integrity checks.

It is designed to run **offline / air-gapped** (ppc64le target, `FROM
scratch` container, no CGO) and to be **stateless** on the host filesystem —
all persistent state lives in Oracle; the container itself needs no writable
volume.

## 2. Process topology

```
┌─────────────────────────────────────────────────────────────────┐
│                      goGioIa binary (:8080)                     │
│                                                                   │
│  cmd/server/main.go                                               │
│    └─ internal/server.Server  (net/http, stdlib ServeMux)        │
│         ├─ serves embedded SPA (web/dist, //go:embed)            │
│         ├─ /api/*        chat, pdf, health, models                │
│         ├─ /api/rag/*    ingestion, ask, feedback                 │
│         └─ /api/rag/ops/* dashboard aggregates (read-only)        │
│                                                                   │
│  internal/rag.Service   — orchestrates ingest + retrieval          │
│  internal/store.Store   — Oracle access (go-ora, no CGO)           │
│  internal/ollama.Client — /api/chat, /api/embed, /api/tags,/ps     │
└─────────────────────────────────────────────────────────────────┘
              │                                   │
              ▼                                   ▼
     ┌──────────────────┐              ┌───────────────────────┐
     │  Ollama (remote)  │              │  Oracle 23ai (remote)  │
     │  chat + embeddings│              │  documents, chunks,    │
     │  (<ollama-host>)  │              │  vectors, rag_events   │
     └──────────────────┘              └───────────────────────┘
```

There is no other process: no queue, no cache service, no auth layer. Ollama
and Oracle are the only two external dependencies, and both are only reachable
on the target network (see `internal/config` defaults). The app tolerates
either being down at boot — `Store.EnsureReady` retries lazily on each
request instead of crashing the process.

## 3. Source layout

```
cmd/server/        main() — flags > env > defaults, graceful shutdown
internal/
  config/           env-driven Config{}, records the *source* of each value
                     (environment/flag/default) for the ops "config" tab
  ollama/           client.go  — chat streaming, /api/tags, /api/ps
                     embed.go   — batched embeddings, retries, size-splitting,
                                  concurrency semaphore, event recording
  pdf/              pure-Go text extraction (whole doc + per-page)
  rag/              chunk.go   — paragraph-aware text splitter
                     service.go — ingestion pipeline + retrieval/prompt
  store/            oracle.go  — connection, schema bootstrap, CRUD, vector search
                     schema.go  — DDL (idempotent CREATE TABLE/INDEX)
                     events.go  — rag_events writer (best-effort, async)
                     ops.go     — all dashboard aggregate queries (~1600 lines)
  server/           server.go       — router wiring, SPA static handler
                     handlers.go     — chat, pdf, health, models
                     handlers_rag.go — ingest, ask (SSE), feedback
                     handlers_ops.go — dashboard endpoints, health cache
web/                embed.go — //go:embed of web/dist (Vite build output)
frontend/           Vue 3 + TS + Vite + Tailwind v4 source (see §6)
deploy/             oracle_schema.sql (reference DDL), Dockerfile.ppc64le,
                    Podman quadlet unit, dev env file
```

Dependency direction is strictly `server → rag → {store, ollama}`; `store`
and `ollama` never import each other or `rag`. `rag` doesn't know about HTTP;
`server` doesn't know about SQL or the Ollama wire format.

## 4. Request flow: plain chat

`ChatInput.vue` → `POST /api/chat` → `handleChat` (`handlers.go`):

1. Decode `{model, messages, options}` from the browser (the browser holds
   the *entire* conversation history — the server is stateless per request).
2. Open an SSE response (`text/event-stream`, `X-Accel-Buffering: no`).
3. `ollama.Client.Stream` POSTs to Ollama's `/api/chat` with `stream: true`
   and returns the raw NDJSON body.
4. Each NDJSON line is decoded into `ollama.ChatChunk` and re-emitted as an
   SSE `message` event with just the token; the final line (`done: true`)
   carries authoritative `prompt_eval_count`/`eval_count`/`load_duration`,
   which `recordGeneration` writes to `rag_events` (kind=`generation`,
   detail=`chat`) for the dashboard — even outside RAG mode, every generation
   is observable.
5. On abort or transport error, an `error` SSE event is sent and the event is
   still recorded (`OK: false`, `ErrorKind` from `ollama.ClassifyError`).

PDF attachments (`/api/pdf`) are extracted **in memory only** (`pdf.ExtractText`,
pure Go, no CGO) and the text is held client-side; nothing touches disk. When
a doc is attached, the frontend raises `num_ctx` to 8192 so Ollama doesn't
silently truncate it.

## 5. Request flow: RAG ("asistente" mode)

### 5.1 Ingestion ("training")

`KnowledgeBase.vue` → `POST /api/rag/documents` (multipart) → `handleRagUpload`
→ `rag.Service.IngestAsync`:

```
UPLOADED → EXTRACTING → CHUNKED → EMBEDDED   (or FAILED, with a resumable retry)
```

1. **Dedup**: SHA-256 of the raw bytes is checked against
   `documents.file_hash` (`uq_documents_hash`). An existing non-`FAILED`
   document short-circuits with `409 Conflict`. A `FAILED` document is
   *resumed in place* — same `document_id`, chunks already embedded are kept.
2. `IngestAsync` returns immediately (`202 Accepted`); the pipeline runs in a
   detached goroutine (`go s.process(...)`, 30-minute budget, independent of
   the HTTP request lifetime) so the upload request doesn't block on
   minutes-long embedding work.
3. **Extract** (`internal/pdf.ExtractPages`): per-page plain text, stored in
   `document_pages` (needed to cite "page 3" in answers).
4. **Chunk** (`internal/rag/chunk.go`): ~1800 chars with 250 overlap,
   preferring to cut on paragraph → line → sentence → word boundaries
   (`findBreak`). A hard cap (`capChunks`) re-splits anything that would
   exceed the embedding model's context window; re-chunking is **deterministic**
   (same input/config → same chunk indexes), which matters for resuming.
5. **Embed** (`internal/ollama/embed.go` + `rag.embedAndStore`): batched calls
   to `/api/embed` (`EMBED_BATCH`, default 8), each row upserted by
   `(document_id, chunk_index)` with a deterministic id
   (`derivedID("chunk", docID, index)`) — so retrying a batch after a
   transient failure never duplicates vectors. If Ollama rejects a batch with
   HTTP 400 (input too large), the batch is retried chunk-by-chunk, and any
   chunk still too large is recursively halved and its children's vectors
   **averaged** (`embedSplitting`/`meanVec`) — cosine-distance-equivalent —
   so the original chunk keeps its index/text but never fails outright.
6. Resuming skips chunk indexes already embedded
   (`EmbeddedChunkIndexes`/`pendingChunks`); a warm-up call
   (`ollama.WarmEmbed`) ensures the embeddings model is loaded before the
   batch loop starts.
7. **Finish**: once every chunk is embedded, `FinishDocument` sets
   `EMBEDDED` + `page_count`. Every stage transition and every embed call is
   recorded to `rag_events` (best-effort, see §7).

### 5.2 Query ("ask")

`ChatInput.vue` (RAG toggle on) → `POST /api/rag/ask` → `handleRagAsk` →
`rag.Service.PrepareAsk`, then streamed like plain chat:

1. **Embed the question**: `nomic-embed-text` with the `search_query:`
   prefix (documents were embedded with `search_document:` — the model's
   asymmetric convention).
2. **Retrieve**: `store.SearchChunks` — `VECTOR_DISTANCE(embedding, :q, COSINE)`
   over `document_chunks`, `FETCH FIRST :topK ROWS ONLY` (`RAG_TOP_K`,
   default 5). Uses the `ORGANIZATION INMEMORY NEIGHBOR GRAPH` vector index
   when present; falls back to exact search otherwise (index creation
   requires `vector_memory_size` on the instance and is tolerated as absent).
3. **Prompt**: the active row in `prompt_templates` (versioned, seeded on
   first boot) has its `{context}`/`{question}` placeholders filled
   (`renderPrompt`) with the retrieved chunks (each capped to 2000 chars,
   labeled `[Fuente N] file.pdf (pág. P)`).
4. **Trace**: a `rag_queries` row is inserted (with the question's own
   embedding, for future analysis) and `rag_retrieved_chunks` records which
   chunks were used, at what rank and similarity — this is what backs the
   "query trace" view in the ops dashboard.
5. The server emits SSE `sources` (queryId + compact source list) **before**
   generation starts, so the UI can show citations while the model is still
   writing. Then `message` events stream the answer tokens (via
   `ollama.Client.Stream`, same code path as plain chat). On `done`,
   `FinishAsk` persists the full answer text back onto the `rag_queries` row.
6. **Feedback**: 👍/👎 in the UI → `POST /api/rag/feedback` →
   `rag_feedback (query_id, rating, comment)`.

### 5.3 Data model (Oracle 23ai)

```
documents ──< document_pages
   │
   └──< document_chunks (embedding VECTOR(768,FLOAT32)) ──< rag_retrieved_chunks >── rag_queries ──< rag_feedback
                                                                                          │
                                                                                     prompt_templates (FK)

rag_events   — one row per finished pipeline operation (embed | query_embed |
               retrieval | generation | ingest), correlated by ref_id to a
               document_id or query_id. This is the whole observability layer.
```

Schema is created idempotently on first successful `Store.EnsureReady` call
(`internal/store/schema.go`, tolerates `ORA-00955 object already exists`);
see `deploy/oracle_schema.sql` for the reference DDL. IDs are `RAW(16)`
(`SYS_GUID()` or app-derived), exposed to the frontend as lowercase hex.

## 6. Observability: the ops dashboard

Every operation in the RAG pipeline — an embedding call, a retrieval, a
generation, an ingestion milestone — is written to `rag_events`
(`internal/store/events.go`) via `Store.RecordEvent`, fired in a detached
goroutine with its own 10s timeout: **recording is best-effort and never
blocks or fails the pipeline it observes.** If Oracle isn't ready yet, the
event is silently dropped (the pipeline still logs to stdout).

`internal/store/ops.go` is the query layer that turns raw `rag_events` +
domain tables into the aggregates the dashboard needs (overview cards,
bucketed time series, paginated document/query tables with filters, token
usage broken down by category/model/document, per-model stats, integrity
checks). None of this is precomputed — every ops endpoint aggregates in SQL
on read.

Frontend: `OpsDashboard.vue` is a tabbed shell over
`frontend/src/components/ops/*`:

| Tab | Component | Backed by |
|---|---|---|
| Resumen | `OpsOverview.vue` | `GET /ops/overview`, `/ops/timeseries` |
| Ingesta | `OpsIngestion.vue` | `GET /ops/documents[/:id]` |
| Tokens | `OpsTokens.vue` | `GET /ops/tokens` |
| Retrieval | `OpsRetrieval.vue` | `/ops/overview` (weak-match slice) |
| Consultas | `OpsQueries.vue` | `GET /ops/queries[/:id]` |
| Modelos | `OpsModels.vue` | `GET /ops/models` (+ live `/api/tags`,`/api/ps`) |
| Integridad | `OpsIntegrity.vue` | `GET /ops/integrity` |
| Config | `OpsConfig.vue` | `GET /ops/config` |

Two design rules run through this layer:

- **Token counts are never mixed untagged.** Ollama's own
  `prompt_eval_count`/`eval_count` (`token_source: "ollama"`) and a ~4-chars/token
  estimate (`token_source: "estimated"`, used when Ollama didn't report — e.g.
  during chunking, before any API call happens) are always kept distinguishable
  in the UI and the aggregates.
- **Health is derived, not always probed.** `/api/rag/ops/health` is cached
  for `OPS_HEALTH_TTL` seconds. The embeddings model's readiness is inferred
  from recent event activity and only actively probed (1-token warm call) if
  there's no recent signal; the *generation* model is never actively probed,
  because on a small (2 GiB) GPU that would evict the embeddings model. This
  trade-off — and the resulting "unknown vs. healthy" distinction in
  `handleOpsHealth`/`embedModelHealth`/`genModelHealth` — is deliberate, not
  an oversight.

Deep links work: the SPA route `/ops/overview` etc. is served by the same
`index.html` fallback as every other unknown path (`staticHandler` in
`server.go`), so a bookmark or refresh on `/ops/queries` doesn't 404.

## 7. Resilience patterns worth knowing about

- **Resumable ingestion.** Every write in the pipeline (`InsertPage`,
  `InsertChunk`) is an `MERGE`-based upsert keyed by a *deterministic* derived
  id (`derivedID("chunk", docID, index)`), not `SYS_GUID()`. Combined with
  deterministic chunking, this means re-running a failed ingest on the same
  bytes reproduces the same chunk indexes and never creates duplicate rows or
  vectors — it just fills in what's missing.
- **Oversized-input splitting.** Ollama's `/api/embed` will 400 on inputs
  that exceed the model's context. Rather than fail the chunk, the ingester
  detects `IsInputTooLarge()` and recursively halves the text (`halveText`,
  breaking on whitespace) down to `maxSplitDepth` (16 leaves max), embedding
  each half and averaging the resulting vectors. The chunk's persisted text
  and index are untouched — only how its embedding was computed changes.
- **Embedding concurrency + retries.** `internal/ollama/embed.go` gates
  concurrent `/api/embed` calls behind a semaphore (`EMBED_CONCURRENCY`,
  default 1 — sized for a 2 GiB GPU) and retries transient failures up to
  `maxAttempts` (default 4). Every attempt, wait, retry and outcome is what
  feeds the "Modelos" ops tab's GPU-eviction diagnostics (reloads, queue
  waits, connection resets, HTTP 400s).
- **Stateless container.** No volume is mounted; PDF bytes for the *plain
  chat* attach flow never leave memory (`MaxBytesReader` + matching
  `ParseMultipartForm` memory cap so multipart never spills to a temp file).
  RAG-ingested PDFs are persisted, but only as extracted text/vectors in
  Oracle — the original PDF bytes are discarded after ingestion.
- **Lazy Oracle readiness.** `Store.EnsureReady` is called at the top of
  every RAG/ops handler; if Oracle was down at boot, the first successful
  request bootstraps the schema. There's no separate migration step.

## 8. Frontend structure

Vue 3 + TypeScript + Vite + Tailwind v4, no server-side rendering, no
router library — view switching is plain `v-if` state in `App.vue` plus a
`popstate` listener for the `/ops/*` deep-link case.

- `composables/useChat.ts` owns all chat state: conversations are **entirely
  client-side** (`localStorage`, capped at 40), since this is a single-user
  local tool with no server-side session store. It also owns the
  RAG-mode toggle and dispatches to `streamChat` vs. `streamRagAsk`
  (`lib/api.ts`) depending on it.
- `lib/api.ts` / `lib/opsApi.ts` are thin `fetch` wrappers; `streamSSE` is a
  small hand-rolled SSE-over-fetch reader (buffers on `\n\n`, dispatches
  `event:`/`data:` pairs) shared by chat and RAG-ask streaming.
- `components/KnowledgeBase.vue` — document upload/list/delete UI for the
  knowledge base (polls `/api/rag/documents` while anything is
  extracting/chunking/embedding).
- `components/ops/*` — the dashboard tabs described in §6.
- A **session id** (`localStorage`, `gogioia:session`) is hashed
  (SHA-256 → 16 bytes) into `rag_queries.session_id` server-side
  (`store.SessionID`) so queries can be grouped per browser without any
  account system.

## 9. Build & deploy shape

`make build` = `make build-web` (Vite build → `web/dist`) → `go build`
(embeds `web/dist` via `//go:embed all:dist` in `web/embed.go`) → single
binary. `make build-ppc64le` cross-compiles the same source, CGO-disabled,
for the air-gapped ppc64le target; `make image-ppc64le` wraps it in a
`FROM scratch` OCI image with no shell/package manager, run rootless via a
Podman quadlet unit (`deploy/gogioia.container`) with `ReadOnly=true`,
`DropCapability=ALL`, publishing only the app port. See README.md for the
full command sequence and environment variable reference.
