import type { ApiMessage, HealthStatus, ModelsResponse, PdfResult, ServerConfig } from '@/types'

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
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ messages, model: opts.model, format: opts.format, options: opts.options }),
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
      switch (evt.event) {
        case 'message':
          handlers.onToken((evt.data as { content: string }).content ?? '')
          break
        case 'error':
          throw new Error((evt.data as { error: string }).error || 'model error')
        case 'done':
          handlers.onDone?.()
          return
      }
    }
  }
  handlers.onDone?.()
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
