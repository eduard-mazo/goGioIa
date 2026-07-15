import type {
  ApiMessage,
  HealthStatus,
  ModelsResponse,
  PdfResult,
  RagDocument,
  RagHealth,
  RagSource,
  ServerConfig,
} from '@/types'

/** Fetch non-secret server config (model name, etc.). */
export async function fetchConfig(): Promise<ServerConfig> {
  const res = await fetch('/api/config')
  if (!res.ok) throw new Error('failed to load config')
  return res.json()
}

/** List the models installed on the Ollama host (empty when unreachable). */
export async function fetchModels(): Promise<ModelsResponse> {
  const res = await fetch('/api/models')
  if (!res.ok) throw new Error('failed to load models')
  return res.json()
}

/** Check whether the backend can reach Ollama. */
export async function fetchHealth(): Promise<HealthStatus> {
  const res = await fetch('/api/health')
  if (!res.ok) throw new Error('health check failed')
  return res.json()
}

/** Upload a PDF and return its extracted text. */
export async function uploadPdf(file: File): Promise<PdfResult> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch('/api/pdf', { method: 'POST', body: form })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `upload failed (${res.status})`)
  return data as PdfResult
}

interface StreamHandlers {
  onToken: (token: string) => void
  onDone?: () => void
}

/** Per-request chat options forwarded to Ollama. */
export interface ChatOptions {
  model?: string
  /** Structured-output constraint: "json" or a JSON schema object. */
  format?: unknown
  /** Model params, e.g. { num_ctx: 8192, temperature: 0.1 }. */
  options?: Record<string, unknown>
}

/**
 * Stream a chat completion from the backend (Server-Sent Events over fetch).
 * Resolves when the stream ends; rejects on transport or model errors.
 */
export async function streamChat(
  messages: ApiMessage[],
  handlers: StreamHandlers,
  opts: ChatOptions = {},
  signal?: AbortSignal,
): Promise<void> {
  await streamSSE(
    '/api/chat',
    { messages, model: opts.model, format: opts.format, options: opts.options },
    (event, data) => {
      switch (event) {
        case 'message':
          handlers.onToken((data as { content: string }).content ?? '')
          return true
        case 'error':
          throw new Error((data as { error: string }).error || 'model error')
        case 'done':
          handlers.onDone?.()
          return false
      }
      return true
    },
    signal,
  )
  handlers.onDone?.()
}

// ── RAG (base de conocimiento en Oracle 23ai) ──────────────────────────────

/** Estado del vector store y del asistente RAG. */
export async function fetchRagHealth(): Promise<RagHealth> {
  const res = await fetch('/api/rag/health')
  if (!res.ok) throw new Error('rag health check failed')
  return res.json()
}

/** Lista los documentos de la base de conocimiento. */
export async function listRagDocuments(): Promise<RagDocument[]> {
  const res = await fetch('/api/rag/documents')
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `request failed (${res.status})`)
  return (data.documents ?? []) as RagDocument[]
}

/**
 * Sube un PDF a la base de conocimiento; el backend lo procesa en segundo
 * plano (extracción → chunks → embeddings en Oracle). Devuelve el id asignado.
 */
export async function uploadRagDocument(file: File): Promise<{ id: string; fileName: string }> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch('/api/rag/documents', { method: 'POST', body: form })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `upload failed (${res.status})`)
  return data as { id: string; fileName: string }
}

/** Elimina un documento (y sus chunks) del vector store. */
export async function deleteRagDocument(id: string): Promise<void> {
  const res = await fetch(`/api/rag/documents/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error(data.error || `delete failed (${res.status})`)
  }
}

/** Valora una respuesta del asistente RAG (-1 | 0 | 1). */
export async function sendRagFeedback(queryId: string, rating: number, comment = ''): Promise<void> {
  const res = await fetch('/api/rag/feedback', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ queryId, rating, comment }),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error(data.error || `feedback failed (${res.status})`)
  }
}

interface RagStreamHandlers {
  /** Fuentes recuperadas de Oracle; llegan antes que el primer token. */
  onSources: (queryId: string, sources: RagSource[]) => void
  onToken: (token: string) => void
  onDone?: (queryId: string) => void
}

/**
 * Pregunta al asistente RAG. El backend embebe la consulta con
 * nomic-embed-text, recupera contexto de Oracle 23ai y genera con Mistral;
 * la respuesta llega en streaming SSE (sources → message* → done).
 */
export async function streamRagAsk(
  question: string,
  sessionId: string,
  handlers: RagStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let queryId = ''
  await streamSSE(
    '/api/rag/ask',
    { question, sessionId },
    (event, data) => {
      switch (event) {
        case 'sources': {
          const d = data as { queryId: string; sources: RagSource[] }
          queryId = d.queryId
          handlers.onSources(d.queryId, d.sources ?? [])
          return true
        }
        case 'message':
          handlers.onToken((data as { content: string }).content ?? '')
          return true
        case 'error':
          throw new Error((data as { error: string }).error || 'model error')
        case 'done':
          handlers.onDone?.((data as { queryId?: string }).queryId || queryId)
          return false
      }
      return true
    },
    signal,
  )
  handlers.onDone?.(queryId)
}

/**
 * POST JSON y consume la respuesta como Server-Sent Events. `onEvent`
 * devuelve false para terminar (evento "done" ya despachado).
 */
async function streamSSE(
  url: string,
  body: unknown,
  onEvent: (event: string, data: unknown) => boolean,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  })

  if (!res.ok || !res.body) {
    const detail = await res.text().catch(() => '')
    throw new Error(detail || `request failed (${res.status})`)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // SSE events are separated by a blank line.
    const chunks = buffer.split('\n\n')
    buffer = chunks.pop() ?? ''

    for (const chunk of chunks) {
      const evt = parseEvent(chunk)
      if (!evt) continue
      if (!onEvent(evt.event, evt.data)) return
    }
  }
}

function parseEvent(raw: string): { event: string; data: unknown } | null {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
  }
  if (dataLines.length === 0) return null
  try {
    return { event, data: JSON.parse(dataLines.join('\n')) }
  } catch {
    return null
  }
}
