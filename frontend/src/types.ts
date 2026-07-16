export type Role = 'system' | 'user' | 'assistant'

/** A message as sent to / received from the backend API. */
export interface ApiMessage {
  role: Role
  content: string
}

/** A message tracked in the UI, with rendering metadata. */
export interface ChatMessage extends ApiMessage {
  id: string
  streaming?: boolean
  error?: boolean
  createdAt: number
  /** Assistant reply produced by the RAG assistant (knowledge base). */
  rag?: boolean
  /** Chunks retrieved from Oracle 23ai that grounded this reply. */
  sources?: RagSource[]
  /** rag_queries id — enables feedback on this reply. */
  queryId?: string
  /** User rating already sent for this reply (-1 | 1). */
  feedback?: number
  /** Respuesta servida desde la cache semántica (sin pasar por el LLM). */
  cached?: boolean
}

/** A PDF attached to the conversation as extra context. */
export interface AttachedDoc {
  id: string
  filename: string
  chars: number
  /** Texto inline (respaldo sin Oracle); '' cuando vive en el servidor. */
  text: string
  /** Id del anexo persistido en Oracle (se referencia en vez de reenviar). */
  attachmentId?: string
}

/** A saved conversation: its messages, attached docs, and metadata. */
export interface Conversation {
  id: string
  /** Id de la conversación persistida en Oracle (contexto server-side). */
  serverId?: string
  /** Derived from the first user message; '' until the user writes something. */
  title: string
  messages: ChatMessage[]
  docs: AttachedDoc[]
  createdAt: number
  updatedAt: number
}

export interface ServerConfig {
  model: string
  name: string
  ollama: string
}

export interface ModelsResponse {
  models: string[]
  default: string
}

export interface HealthStatus {
  ollama: 'online' | 'offline'
  detail: string
  model: string
}

export interface PdfResult {
  filename: string
  chars: number
  text: string
}

// ── RAG (base de conocimiento en Oracle 23ai) ──────────────────────────────

export type RagDocStatus = 'UPLOADED' | 'EXTRACTING' | 'CHUNKED' | 'EMBEDDED' | 'FAILED'

/** A PDF ingested into the vector store (documents table). */
export interface RagDocument {
  id: string
  fileName: string
  sizeBytes: number
  pageCount: number
  chunkCount: number
  status: RagDocStatus
  error?: string
  uploadedBy?: string
  uploadedAt: string
  processedAt?: string
}

/** A retrieved chunk cited as the source of a RAG answer. */
export interface RagSource {
  chunkId: string
  fileName: string
  page: number
  score: number
  snippet: string
}

export interface RagHealth {
  oracle: 'online' | 'offline'
  detail: string
  documents: number
  chunks: number
  embedModel: string
  ragModel: string
}
