# goGioIa

A self-contained, **offline** desktop-grade chat UI for a local
[Ollama](https://ollama.com) instance. The Go binary embeds the entire compiled
frontend (Vue 3 + TypeScript + Tailwind), so it ships as a **single executable**
with no external assets, CDN calls, or runtime dependencies.

## Features

- **Streaming chat** — tokens are streamed from Ollama to the browser over SSE.
- **Rich Markdown rendering** — headings, tables, lists, blockquotes, and
  syntax-highlighted code blocks with one-click copy.
- **PDF context** — attach a PDF; text is extracted server-side (pure Go) and
  injected into the conversation for the model to reason over.
- **Gateway-console theme** — professional dark/light palette via CSS variables.
- **Health indicator** — live status of the configured Ollama endpoint.

## Architecture

```
goGioIa/
├── cmd/server/          # main entrypoint
├── internal/
│   ├── config/          # env-driven configuration
│   ├── ollama/          # streaming Ollama /api/chat client
│   ├── pdf/             # pure-Go PDF text extraction
│   └── server/          # HTTP routing, SSE, static SPA serving
├── web/
│   ├── embed.go         # //go:embed of the built frontend
│   └── dist/            # Vite build output (embedded)
└── frontend/            # Vue 3 + TS + Vite + Tailwind v4 source
    └── src/
        ├── components/  # UI + chat components
        ├── composables/ # useChat (state + streaming)
        └── lib/         # api client, markdown, utils
```

### API

| Method | Path          | Purpose                                        |
| ------ | ------------- | ---------------------------------------------- |
| GET    | `/api/config` | Model name and app metadata                    |
| GET    | `/api/health` | Ollama reachability                            |
| POST   | `/api/chat`   | Chat completion, streamed back as SSE          |
| POST   | `/api/pdf`    | Multipart PDF upload → extracted text (JSON)   |
| GET    | `/*`          | Embedded SPA (with client-side route fallback) |

## Contract mode (structured analysis)

Toggle **Modo contrato** in the composer to analyse an attached contract. The app
sends a JSON-schema `format` plus a wide `num_ctx` and low temperature to Ollama,
and renders the reply as a structured report (parties, dates, obligations,
termination, governing law, and colour-coded risks) instead of prose. It works
with any model; for a dedicated, preconfigured model create one from the bundled
Modelfile — it then appears in the model picker:

```bash
ollama create contract-analyst -f deploy/Modelfile.contract
```

> Automated extraction only — not legal advice. Keep a human in the loop.

## Configuration

Defaults live in `internal/config/config.go` and can be overridden by env vars:

| Variable     | Default                             | Description               |
| ------------ | ----------------------------------- | ------------------------- |
| `OLLAMA_API` | `http://10.14.16.193:9091/api/chat` | Ollama chat endpoint URL  |
| `WEB_PORT`   | `:8080`                             | HTTP listen address       |
| `MODEL_NAME` | `llama3`                            | Default model (see below) |

> Set `MODEL_NAME` to a model you actually have pulled in Ollama, e.g.
> `mistral`, `llama3`, `qwen2.5`.

## Build & run (production)

Requires Go 1.26+ and Node 18+.

```bash
make build      # builds the frontend + embeds it + compiles the binary
make run        # or: ./bin/gogioia
```

Then open <http://localhost:8080>.

The resulting `bin/gogioia` is fully self-contained — copy it anywhere and run
it; all assets are baked in.

## Develop

Run the backend and the Vite dev server (with hot-reload) side by side:

```bash
make server     # terminal 1 — Go API on :8080
make dev        # terminal 2 — Vite on :5173, proxies /api to :8080
```

Open <http://localhost:5173> for live-reloading development.

## Deploy to ppc64le (offline / air-gapped)

goGioIa is a pure-Go, CGO-free binary with the frontend embedded, so it
cross-compiles to a fully static ppc64le ELF and ships as a `FROM scratch`
container — no base OS, no shell, no package manager.

```bash
make build-ppc64le     # static ppc64le binary  -> bin/gogioia-linux-ppc64le
make image-ppc64le     # FROM-scratch OCI image  (needs docker + binfmt/qemu)
make export-ppc64le    # -> gogioia-ppc64le.tar  (transfer this to the target)
```

On the air-gapped ppc64le host (rootless — runs as your user, no `sudo`):

```bash
podman load -i gogioia-ppc64le.tar
mkdir -p ~/.config/containers/systemd
cp deploy/gogioia.container ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user enable --now gogioia
loginctl enable-linger "$USER"   # keep it running after logout
```

Verify with `systemctl --user status gogioia` and
`journalctl --user -u gogioia -f`.

The Podman **quadlet** unit (`deploy/gogioia.container`) runs the image
**rootless** with `Pull=never`, `ReadOnly=true`, `NoNewPrivileges=true`,
`DropCapability=ALL`, as UID/GID `65534` inside the user namespace, publishing
only `:8080`. Configure it via the `WEB_PORT`, `OLLAMA_API`, and `MODEL_NAME`
environment variables.

### Stateless — including PDFs

There is **no writable volume**. The app keeps no server-side state:

- The chat is stateless — the browser sends the full message history each turn.
- **PDF uploads are processed entirely in memory.** The request body is capped
  (32 MiB) and `multipart` parsing uses the same value as its in-memory limit,
  so a file part never spills to a temp file on disk. The extracted text is
  returned to the browser and held only in the frontend; nothing touches the
  container filesystem. That is why the root filesystem can be mounted
  read-only and no `/data` mount (or `tmpfs`) is required.
