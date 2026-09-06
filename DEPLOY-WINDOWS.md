# Deploying goGioIa on Windows

Review of the deployment surface for a new Windows device: build tooling,
`internal/config`, `internal/store` (Oracle), `internal/ollama`,
`internal/server`, the `Makefile` and `deploy/`. All claims below were verified
by cross-compiling and running the test suite.

- [1. Code review — Windows readiness](#1-code-review--windows-readiness)
- [2. Database requirements](#2-database-requirements)
- [3. Ollama requirements and models](#3-ollama-requirements-and-models)
- [4. Deployment steps](#4-deployment-steps--new-windows-device)
- [5. How to modify the environment](#5-how-to-modify-the-environment)
- [6. Summary of what to fix first](#6-summary-of-what-to-fix-before-deploying)

---

## 1. Code review — Windows readiness

### ✅ What works in your favour

**No native dependencies at all.** Both third-party deps are pure Go:

```
github.com/sijms/go-ora/v2 v2.9.0        ← Oracle driver, no OCI
github.com/ledongthuc/pdf v0.0.0-2025... ← PDF extraction, no poppler
```

This is the big one: **you do not need Oracle Instant Client, OCI DLLs,
`tnsnames.ora`, or `ORACLE_HOME` on the Windows box.** Verified cross-compile:

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/server
→ gogioia.exe, 27 MB, WINDOWS BUILD OK
```

`go build ./... && go test ./...` also passes clean on the current working tree
(`ollama`, `pdf`, `rag` all ok).

**Nothing touches the filesystem.** `internal/server/handlers.go:18` caps
uploads at 32 MiB and `ParseMultipartForm` gets the same value as its in-memory
limit, so PDF parts never spill to a temp file. No path joins, no `/tmp`, no
writable volume. Fully portable.

### 🔴 Blocker 1 — a fresh clone cannot build

`web/dist` is gitignored and the `.gitkeep` escape hatch is **not actually
tracked**:

```
$ git ls-files web/
web/embed.go          ← that's all
```

So `web/embed.go:10` (`//go:embed all:dist`) fails on any new machine.
Simulated:

```
$ git clone <repo> fresh && cd fresh && go build ./cmd/server
web/embed.go:10:12: pattern all:dist: no matching files found
```

**The frontend build must run before the Go build**, which means Node +
`npm install` (≈250 MB of `node_modules`, needs internet) on the new device — or
you ship a prebuilt `gogioia.exe` / a prebuilt `web/dist/` folder.

### 🔴 Blocker 2 — no SERVICE_NAME support (only SID)

`internal/store/oracle.go:35`:

```go
url := go_ora.BuildUrl(cfg.OracleHost, cfg.OraclePort, "", cfg.OracleUser, ...
                                                       ↑ service name, hardcoded empty
    map[string]string{"SID": cfg.OracleSID, "lob fetch": "pre"})
```

go-ora's 3rd parameter *is* the service name; the code always passes `""` and
connects by SID. **If the new environment runs Oracle 23ai Free (service
`FREEPDB1`) or any PDB, this will not connect** — SID `FREE` lands you in the
CDB root, where you can't create a normal (non-`C##`) user. Minimal patch:

```go
// config.go
OracleService string   // + env("ORACLE_SERVICE", "")

// oracle.go
opts := map[string]string{"lob fetch": "pre"}
if cfg.OracleService == "" {
    opts["SID"] = cfg.OracleSID
}
url := go_ora.BuildUrl(cfg.OracleHost, cfg.OraclePort, cfg.OracleService,
    cfg.OracleUser, cfg.OraclePassword, opts)
```

### 🟠 Windows-only behavioural regression: retries on connection reset

`internal/ollama/embed.go` classifies and retries transport errors with:

```go
errors.Is(err, syscall.ECONNRESET) || strings.Contains(s, "connection reset")
```

On Windows, `syscall.ECONNRESET` is a **synthetic `APPLICATION_ERROR`
constant**, not `WSAECONNRESET` (10054) — confirmed in `zerrors_windows.go` and
`Errno.Is()`, which maps only `ErrPermission` / `ErrExist` / `ErrNotExist` /
`ErrUnsupported`. The string fallback also misses, because Windows' message is
*"An existing connection was forcibly closed by the remote host."*

**Effect on Windows:** a mid-ingest reset from Ollama is **not retried**
(`isTransient` → false), and the ops dashboard logs `error_kind=other` instead
of `connection_reset` / `refused`. Fix — broaden the string match (no new import
needed):

```go
func isResetErr(s string) bool {
    return strings.Contains(s, "connection reset") ||
        strings.Contains(s, "forcibly closed") ||   // WSAECONNRESET
        strings.Contains(s, "actively refused")     // WSAECONNREFUSED
}
```

### 🟠 Hardcoded production credentials in source

`internal/config/config.go:74-79` ships real-looking values as compile-time
defaults:

```go
defaultOraclePassword = "<contraseña real>"
defaultOracleHost     = "<host interno>"
defaultOllamaAPI      = "http://<host interno>:9091/api/chat"
```

On a new device these **must** be overridden by env — and they are in git
history. Rotate the password and consider making `ORACLE_PASSWORD` mandatory
(fail fast if unset) rather than defaulted.

### 🟡 Minor Windows notes

| Item | Detail |
| --- | --- |
| `make` | Not available natively on Windows. Build with `make dist-windows` on the Linux box (Path A), or run the two commands by hand on Windows (Path B). |
| SIGTERM | `cmd/server/main.go` handles `SIGINT`/`SIGTERM`. Ctrl-C works; SIGTERM is never delivered on Windows, so a service wrapper must use its own stop mechanism (NSSM graceful stop / Ctrl-C event). |
| Firewall | `:8080` inbound needs a rule if accessed from other machines. |
| Config surface | 4 flags (`--config`, `--port`, `--ollama`, `--model`); everything else comes from `gogioia.env` or the environment. Precedence: flag > env > file > default. |
| Oracle bootstrap | `EnsureReady` runs in the background at boot and is idempotent (tolerates ORA-00955); it retries per-request if Oracle is down at startup. No manual DDL step required. |

---

## 2. Database requirements

**Oracle Database 23ai — hard requirement, non-negotiable.** The schema uses
`VECTOR(768, FLOAT32)`, `TO_VECTOR()`, and `VECTOR_DISTANCE(..., COSINE)`.
**19c and 21c will not work** — the datatype does not exist.

| Requirement | Value |
| --- | --- |
| Version | Oracle Database 23ai (23.4+). **23ai Free** is sufficient and license-free |
| Listener | TCP, default `1521`, reachable from the Windows box |
| Connect identifier | **SID** (see [Blocker 2](#-blocker-2--no-service_name-support-only-sid) if your target is a PDB / service name) |
| Client on Windows | **none** — go-ora is pure Go |
| Character set | AL32UTF8 (Spanish accents in CLOBs) |

**App user privileges** (the app creates its own schema on first boot):

```sql
CREATE USER useria IDENTIFIED BY "<nueva-password>";
GRANT CREATE SESSION, CREATE TABLE, CREATE SEQUENCE TO useria;
ALTER USER useria QUOTA UNLIMITED ON users;
-- USER_INDEXES is readable by default (needed by ops /integrity)
```

**Vector index — instance-level prerequisite.** `CREATE VECTOR INDEX ...
ORGANIZATION INMEMORY NEIGHBOR GRAPH` needs `vector_memory_size` configured in
the SGA:

```sql
ALTER SYSTEM SET vector_memory_size = 512M SCOPE=SPFILE;
-- restart required
```

If it is unset, `internal/store/oracle.go:80` logs a warning and **the app keeps
working with exact (brute-force) search** — correct results, slower at scale.
The ops dashboard reports the index as absent.

**Storage sizing:** each chunk costs 768 × 4 B = **3 KB of vector** plus its
CLOB text (~1800 chars) plus a duplicate of the page text in `document_pages`.
Rough rule: **~10 KB of tablespace per chunk**, ~1 chunk per 1800 characters of
PDF. A 500-page corpus ≈ 1500 chunks ≈ 15 MB. Very modest.

**Tables auto-created:** `documents`, `document_pages`, `document_chunks`,
`rag_queries`, `rag_retrieved_chunks`, `rag_feedback`, `prompt_templates`,
`rag_events` + 4 indexes. Reference DDL in `deploy/oracle_schema.sql`.

---

## 3. Ollama requirements and models

`OLLAMA_API` is the **full chat URL**; the client derives the host root by
trimming at `/api/` to reach `/api/embed`, `/api/embeddings`, `/api/tags`,
`/api/ps`.

### Models to pull

| Model | Role | Env var | Size | Required? |
| --- | --- | --- | --- | --- |
| **`nomic-embed-text`** | Embeddings, **768 dims** | `EMBED_MODEL` | ~274 MB | **Mandatory** |
| **`mistral:latest`** | Generates RAG answers | `RAG_MODEL` | ~4.1 GB | Mandatory for RAG |
| `llama3.1:latest` | Plain chat (non-RAG) | `MODEL_NAME` | ~4.9 GB | Optional |

```powershell
ollama pull nomic-embed-text
ollama pull mistral
ollama pull llama3.1        # only if you use the plain chat tab
```

> ⚠️ **`nomic-embed-text` is not freely swappable.** 768 is baked into the
> `VECTOR(768, FLOAT32)` column, and the ops config page reports
> `VECTOR_DIMENSION=768` as `source: code`. A different embedding model means
> changing the DDL **and re-ingesting every document**. Its `n_ctx_train` is
> 2048 → that is why `EMBED_MAX_TOKENS=2048`. The code also prepends nomic's
> task prefixes (`search_document: ` / `search_query: `), which are
> model-specific.

### Hardware / host tuning

Defaults in `internal/config/config.go` are tuned for a **2 GiB GPU**
(`EMBED_CONCURRENCY=1`, `EMBED_BATCH=8`, `EMBED_KEEP_ALIVE=10m`). Mistral q4 at
`num_ctx=8192` wants ~4.5 GB VRAM. If Ollama runs on the same Windows box with a
small GPU, set **on the Ollama host** (not in goGioIa):

```
OLLAMA_NUM_PARALLEL=1
OLLAMA_MAX_LOADED_MODELS=1
OLLAMA_HOST=0.0.0.0:11434      # only if goGioIa is on a different machine
```

Without these, the two models evict each other mid-ingest — exactly the incident
the ops "models" page was built to diagnose.

---

## 4. Deployment steps — new Windows device

### Path A (recommended): cross-compile on Linux, ship one .exe

This sidesteps the Node / `web/dist` blocker entirely. **On the Linux machine:**

```bash
cd ~/go/src/goGioIa
make dist-windows
```

That rebuilds the frontend, cross-compiles, and leaves a ready-to-copy
`dist/gogioia-windows-amd64.zip` containing:

```
gogioia.exe            23 MB, self-contained (frontend embedded)
gogioia.env.example    config template → rename to gogioia.env
DEPLOY-WINDOWS.md      this guide
README.md
```

Copy the zip to the Windows box, unpack it into `C:\apps\goGioIa`, and jump to
**step 3**. For Windows on ARM use `make build-windows-arm64`; `make cross`
builds every platform at once (windows/linux/darwin amd64+arm64, plus
ppc64le), and `make dist` packages them all.

### Path B: build from source on Windows

**Prerequisites:** Go 1.26+ (`go.mod` requires it — the code uses
`errors.AsType`, a 1.26 feature), Node 18+, Git.

```powershell
winget install GoLang.Go OpenJS.NodeJS.LTS Git.Git
# close and reopen PowerShell so PATH refreshes
go version    # must be >= 1.26
```

**1. Clone and build the frontend first** (mandatory — see Blocker 1):

```powershell
git clone <repo-url> C:\apps\goGioIa
cd C:\apps\goGioIa\frontend
npm install
npm run build          # writes ..\web\dist  → required by //go:embed
```

**2. Build the binary:**

```powershell
cd C:\apps\goGioIa
go build -o bin\gogioia.exe .\cmd\server
```

**3. Prepare Oracle 23ai** (on the DB host):

```sql
ALTER SYSTEM SET vector_memory_size = 512M SCOPE=SPFILE;   -- then restart
CREATE USER useria IDENTIFIED BY "<nueva-password>";
GRANT CREATE SESSION, CREATE TABLE, CREATE SEQUENCE TO useria;
ALTER USER useria QUOTA UNLIMITED ON users;
```

Do **not** run `oracle_schema.sql` — the app creates everything on first boot.

**4. Prepare Ollama:**

```powershell
winget install Ollama.Ollama
ollama pull nomic-embed-text
ollama pull mistral
ollama list                       # verify
```

**5. Create the config file.** Copy `gogioia.env.example` next to the `.exe` as
`gogioia.env` and edit host, SID, user and password — it is loaded
automatically at startup. See [section 5](#5-how-to-modify-the-environment) for
the format and the alternatives (env vars, service environment, flags).

```powershell
copy C:\apps\goGioIa\gogioia.env.example C:\apps\goGioIa\gogioia.env
notepad C:\apps\goGioIa\gogioia.env
```

**6. Open the firewall** (only if reached from other machines):

```powershell
New-NetFirewallRule -DisplayName "goGioIa" -Direction Inbound `
  -Protocol TCP -LocalPort 8080 -Action Allow
```

**7. First run and verification:**

```powershell
cd C:\apps\goGioIa
.\gogioia.exe
```

Expected in the log:

```
🚀 goGioIa listening on http://localhost:8080
   config = C:\apps\goGioIa\gogioia.env    ← the file was found
   model = llama3.1:latest
   ollama = http://localhost:11434/api/chat
oracle: esquema RAG verificado        ← schema bootstrap succeeded
```

`config = (sin archivo; entorno y defaults)` means no config file was found —
check its name and that it sits next to the `.exe`.

If you see `aviso: no se pudo crear el índice vectorial (¿vector_memory_size?)`
the app still works, just with exact search. If you see
`aviso: oracle no disponible aún:` check host/port/SID/credentials — it retries
per-request.

Then verify each subsystem:

```powershell
curl http://localhost:8080/api/health          # Ollama reachability
curl http://localhost:8080/api/rag/health      # Oracle + doc/chunk counts
curl http://localhost:8080/api/rag/ops/health  # per-component health
curl http://localhost:8080/api/rag/ops/config  # effective config + source of each key
```

That last one is the best deployment check — it shows every key with
`source: file | environment | flag | default | code`, plus a `CONFIG_FILE` entry
naming the file in use. **Anything critical still showing `default` means your
config is not being picked up.**

Finally, open <http://localhost:8080>, upload a PDF via **Base de conocimiento**,
and watch it go `EXTRACTING → CHUNKED → EMBEDDED`. Note: **scanned PDFs with no
text layer will fail** — the extractor is pure Go, no OCR.

**8. Install as a Windows service** (optional, for auto-start). There is no
native equivalent to the Podman quadlet, so use NSSM:

```powershell
choco install nssm
nssm install goGioIa C:\apps\goGioIa\bin\gogioia.exe
nssm set goGioIa AppDirectory C:\apps\goGioIa
nssm set goGioIa AppEnvironmentExtra ORACLE_HOST=<oracle-host> ORACLE_PORT=1521 `
  ORACLE_SID=orcl ORACLE_USER=useria ORACLE_PASSWORD=<pwd> `
  OLLAMA_API=http://localhost:11434/api/chat RAG_MODEL=mistral:latest
nssm set goGioIa AppStdout C:\apps\goGioIa\logs\out.log
# with gogioia.env next to the .exe, AppEnvironmentExtra is redundant —
# use it only to override a single key on this particular host
nssm set goGioIa AppStderr C:\apps\goGioIa\logs\err.log
nssm start goGioIa
```

Because SIGTERM never arrives on Windows, set NSSM's shutdown method to
`Console` (Ctrl-C event) so the 5-second graceful HTTP shutdown in `main.go`
actually runs and in-flight SSE streams close cleanly.

---

## 5. How to modify the environment

Precedence, highest first:

```
flag de arranque  >  variable de entorno  >  archivo de configuración  >  default
```

### Option A — config file (simplest, recommended)

Copy `gogioia.env.example` next to the `.exe` as **`gogioia.env`** and edit it.
It is picked up automatically at startup — no flags, no environment variables:

```
C:\apps\goGioIa\
  gogioia.exe
  gogioia.env      ← ORACLE_PASSWORD=…, OLLAMA_API=…, etc.
```

Format is the usual `KEY=value`, `#` comments, `export ` prefix tolerated,
quotes to preserve spaces (`'literal'` raw, `"con \n \t"` with escapes). CRLF
and the Notepad UTF-8 BOM are both handled, so you can edit it with Notepad on
the target machine. Malformed lines are skipped rather than aborting startup.

Search order when `--config` is not given:

1. the path in `GOGIOIA_CONFIG`
2. `gogioia.env`, then `.env`, **next to the executable**
3. `gogioia.env`, then `.env`, in the working directory

Anything absent from the file keeps its default. The file is read **once at
startup** — restart the service to apply changes. Point at a different file
explicitly with `--config`:

```powershell
.\gogioia.exe --config C:\apps\goGioIa\produccion.env
```

An explicit path that cannot be read is fatal (a typo shouldn't silently boot
you onto the built-in defaults); autodiscovery failing is not — it just logs.

Restrict the ACL, it holds the DB password:

```powershell
icacls C:\apps\goGioIa\gogioia.env /inheritance:r /grant:r "%USERNAME%:R" /grant:r "SYSTEM:F"
```

`gogioia.env` and `.env` are in `.gitignore`; only the `.example` is versioned.

### Option B — wrapper script

`C:\apps\goGioIa\start-gogioia.cmd`:

```bat
@echo off
REM ── Ollama ────────────────────────────────────────────────────────────
set OLLAMA_API=http://localhost:11434/api/chat
set MODEL_NAME=llama3.1:latest
set RAG_MODEL=mistral:latest
set EMBED_MODEL=nomic-embed-text

REM ── Oracle 23ai ───────────────────────────────────────────────────────
set ORACLE_HOST=<oracle-host>
set ORACLE_PORT=1521
set ORACLE_SID=orcl
set ORACLE_USER=useria
set ORACLE_PASSWORD=<la-password-real>

REM ── HTTP ──────────────────────────────────────────────────────────────
set WEB_PORT=:8080

REM ── RAG tuning (optional — defaults are safe for a 2 GiB GPU) ─────────
REM set EMBED_MAX_TOKENS=2048
REM set EMBED_BATCH=8
REM set EMBED_CONCURRENCY=1
REM set EMBED_KEEP_ALIVE=10m
REM set RAG_TOP_K=5
REM set RAG_NUM_CTX=8192
REM set RAG_CHUNK_SIZE=1800
REM set RAG_CHUNK_OVERLAP=250
REM set OPS_HEALTH_TTL=60

"%~dp0bin\gogioia.exe"
```

Restrict its ACL — it holds the DB password:

```powershell
icacls C:\apps\goGioIa\start-gogioia.cmd /inheritance:r /grant:r "%USERNAME%:F" "SYSTEM:F"
```

### Option C — service environment

`nssm set goGioIa AppEnvironmentExtra ...` (shown above). Env vars win over the
config file, so this is a clean way to override one or two values per host
while keeping the shared `gogioia.env`; they live in the registry, not a
readable file.

### Option D — machine-wide

Affects every process, use sparingly:

```powershell
[Environment]::SetEnvironmentVariable("ORACLE_HOST","<oracle-host>","Machine")
```

### Flag overrides

Flags win over everything. Four exist:

```powershell
.\bin\gogioia.exe --config C:\apps\goGioIa\otro.env --port 9000 `
                  --ollama http://otro:11434/api/chat --model qwen2.5
```

Check what actually took effect at `/api/rag/ops/config` — every key is listed
with its provenance (`flag` / `environment` / `file` / `default` / `code`), plus
a `CONFIG_FILE` entry naming the file that was loaded.

### Full env var reference

| Variable | Default | Notes |
| --- | --- | --- |
| `OLLAMA_API` | `http://127.0.0.1:11434/api/chat` | **Full chat URL.** Host root is derived by trimming at `/api/` |
| `WEB_PORT` | `:8080` | `:` is added automatically if omitted |
| `MODEL_NAME` | `llama3.1:latest` | Plain-chat model; must exist in `ollama list` |
| `EMBED_MODEL` | `nomic-embed-text` | **Must be 768-dim** — see §3 |
| `EMBED_MAX_TOKENS` | `2048` | nomic's `n_ctx_train`; pins `num_ctx`, overriding host Modelfile |
| `EMBED_BATCH` | `8` | Chunks per `/api/embed` call |
| `EMBED_CONCURRENCY` | `1` | Raise only with a large GPU |
| `EMBED_KEEP_ALIVE` | `10m` | Keeps embed model resident between batches |
| `RAG_MODEL` | `mistral:latest` | Answers RAG questions |
| `RAG_NUM_CTX` | `8192` | Generation context; drives VRAM |
| `RAG_TOP_K` | `5` | Chunks retrieved per question |
| `RAG_CHUNK_SIZE` | `1800` | Characters (~450 tokens) |
| `RAG_CHUNK_OVERLAP` | `250` | Characters |
| `OPS_HEALTH_TTL` | `60` | Health-probe cache, seconds |
| `ORACLE_HOST` | `127.0.0.1` | |
| `ORACLE_PORT` | `1521` | |
| `ORACLE_SID` | `orcl` | **SID only** — see Blocker 2 |
| `ORACLE_USER` | `useria` | |
| `ORACLE_PASSWORD` | *(hardcoded in source)* | **Always override.** Rotate it |

Every one of these keys can be set either in `gogioia.env` or as a real
environment variable — same names, same values, env wins.

> Integer vars are ignored unless they parse as a **positive** integer — a typo
> silently falls back to the default. Check `/api/rag/ops/config` to confirm each
> key reads `source: file` or `source: environment`.

---

## 6. Summary of what to fix before deploying

1. **Ship a prebuilt `.exe` or run `npm run build` first** — otherwise
   `go build` fails on `//go:embed all:dist`.
2. **Add `ORACLE_SERVICE` support** if the target DB is a PDB / 23ai Free
   (`FREEPDB1`) — currently SID-only.
3. **Rotar la contraseña de Oracle y fijar `ORACLE_PASSWORD` en `gogioia.env`** — it is a
   compile-time default sitting in git history.
4. **Broaden the connection-reset string match** so embedding retries actually
   work on Windows.
5. **Set `vector_memory_size`** on the Oracle instance, or accept exact (slower)
   vector search.

Items 1–3 are blockers; 4–5 are degradations you can live with initially.
