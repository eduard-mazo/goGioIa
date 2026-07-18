// Cliente de la API del dashboard de operaciones RAG (/api/rag/ops/*).
// Los tipos reflejan los structs JSON de internal/store/ops.go y
// internal/server/handlers_ops.go.

export type HealthState = 'healthy' | 'degraded' | 'unavailable' | 'unknown'

export interface OpsHealth {
  app: HealthState
  oracle: HealthState
  oracleDetail?: string
  vectorIndex: HealthState
  vectorDetail?: string
  ollama: HealthState
  ollamaDetail?: string
  embedModel: HealthState
  embedDetail?: string
  genModel: HealthState
  genDetail?: string
  ingestion: HealthState
  queueDepth: number
  stuckDocs: number
  checkedAt: string
  ttlSeconds: number
  cached: boolean
}

export interface OpAgg {
  requests: number
  failures: number
  retries: number
  tokensIn: number
  tokensOut: number
  p50Ms: number
  p95Ms: number
  maxMs: number
}

export interface OpsOverview {
  docs: { total: number; embedded: number; processing: number; failed: number }
  chunks: { total: number; embedded: number; missing: number; estTokens: number }
  hours: number
  queries: {
    total: number
    answered: number
    noResults: number
    weakRetrieval: number
    feedbackUp: number
    feedbackDown: number
  }
  embeds: OpAgg
  generation: OpAgg
  errorRate: number
  tokens: { up: number; down: number; upAll: number; downAll: number }
  prev: {
    hasData: boolean
    queries: number
    embedRequests: number
    failures: number
    tokensIn: number
    tokensOut: number
  }
}

export interface SeriesPoint {
  t: string
  v: number
}

export interface KindCount {
  kind: string
  count: number
}

export interface OpsTimeseries {
  hours: number
  bucketSeconds: number
  docsCompleted: SeriesPoint[]
  embedOk: SeriesPoint[]
  embedFail: SeriesPoint[]
  embedTokens: SeriesPoint[]
  embedAvgMs: SeriesPoint[]
  genOk: SeriesPoint[]
  genFail: SeriesPoint[]
  genTokens: SeriesPoint[]
  genAvgMs: SeriesPoint[]
  queries: SeriesPoint[]
  errorsByKind: KindCount[]
}

export interface OpsDocRow {
  id: string
  fileName: string
  sizeBytes: number
  pageCount: number
  chunkCount: number
  status: string
  error?: string
  uploadedBy?: string
  uploadedAt: string
  processedAt?: string
  embeddedChunks: number
  missingChunks: number
  estTokens: number
  minChunkTokens: number
  maxChunkTokens: number
  models: string
  durationSec: number
}

export interface OpsDocPage {
  total: number
  offset: number
  limit: number
  rows: OpsDocRow[]
}

export interface OpsEventRow {
  id: string
  kind: string
  model?: string
  status: 'OK' | 'ERROR'
  httpStatus?: number
  errorKind?: string
  errorDetail?: string
  latencyMs: number
  queueMs?: number
  attempts?: number
  batchSize?: number
  payloadBytes?: number
  tokensIn?: number
  tokensOut?: number
  tokenSource?: string
  loadMs?: number
  detail?: string
  createdAt: string
}

export interface HistBin {
  from: number
  count: number
}

export interface OpsDocDetail extends OpsDocRow {
  hash: string
  pagesStored: number
  avgChunkTokens: number
  timesCited: number
  tokenHist: HistBin[] | null
  chunks: { index: number; page: number; tokens: number; snippet: string }[] | null
  events: OpsEventRow[]
}

export interface OpsQueryRow {
  id: string
  createdAt: string
  model: string
  preview: string
  answered: boolean
  status: 'answered' | 'pending' | 'error'
  candidates: number
  usedInPrompt: number
  topScore: number
  avgScore: number
  ctxTokensEst: number
  promptTokens: number
  completionTokens: number
  genLatencyMs: number
  errorKind?: string
  feedback: number
}

export interface OpsQueryPage {
  total: number
  offset: number
  limit: number
  rows: OpsQueryRow[]
}

export interface OpsTraceChunk {
  rank: number
  score: number
  used: boolean
  chunkId: string
  chunkIndex: number
  page: number
  tokens: number
  snippet: string
  fileName: string
  documentId: string
  model: string
}

export interface OpsQueryTrace {
  id: string
  createdAt: string
  question: string
  response?: string
  model: string
  sessionId?: string
  userId?: string
  template?: string
  chunks: OpsTraceChunk[]
  events: OpsEventRow[]
  feedback: { rating: number; comment?: string; createdBy?: string; createdAt: string }[]
}

export interface OpsTokensReport {
  hours: number
  ingest: {
    extractedEst: number
    attemptedEst: number
    successful: number
    avgChunk: number
    minChunk: number
    maxChunk: number
    nearLimit: number
    overLimit: number
    rejectedInputs: number
    inputTooLarge400: number
  }
  query: { embedTokens: number; retrievedEst: number; selectedEst: number }
  generation: {
    promptTokens: number
    completionTokens: number
    answers: number
    avgPerAnswer: number
    maxPerAnswer: number
    stoppedByLimit: number
  }
  byModel: { model: string; kind: string; requests: number; tokensIn: number; tokensOut: number }[]
  byDoc: { fileName: string; id: string; tokens: number; chunks: number }[]
}

export interface OpsModelAgg {
  model: string
  kind: string
  requests: number
  failures: number
  retries: number
  avgMs: number
  p95Ms: number
}

export interface OpsModelStats {
  hours: number
  perModel: OpsModelAgg[]
  httpStatuses: KindCount[]
  errorKinds: KindCount[]
  loadEvents: number
  avgLoadMs: number
  maxLoadMs: number
  queueAvgMs: number
  queueMaxMs: number
  retryExhausted: number
  lastEmbedOk?: string
  lastEmbedFail?: string
  lastEmbedError?: string
}

export interface OpsModelsResponse {
  endpoint: string
  embedModel: string
  generationModel: string
  chatModel: string
  vectorDimension: number
  embedMaxTokens: number
  ragNumCtx: number
  embedConcurrency: number
  embedKeepAlive: string
  maxAttempts: number
  reachable: boolean
  installed?: { name: string; digest: string; sizeBytes: number; parameterSize: string; quantization: string }[]
  running?: { name: string; digest: string; sizeVram: number; sizeBytes: number; expiresAt: string }[]
  stats: OpsModelStats
}

export interface OpsIntegrityCheck {
  id: string
  count: number
  samples: string[]
  error?: string
}

export interface OpsConfigEntry {
  key: string
  value: string
  source: string
  secret?: boolean
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as { error?: string }).error || `request failed (${res.status})`)
  return data as T
}

const qs = (params: Record<string, string | number | boolean | undefined>) => {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '' && v !== false) p.set(k, String(v))
  }
  const s = p.toString()
  return s ? `?${s}` : ''
}

export const fetchOpsHealth = () => getJSON<OpsHealth>('/api/rag/ops/health')

export const fetchOpsOverview = (hours: number, weak = 0.5) =>
  getJSON<OpsOverview>(`/api/rag/ops/overview${qs({ hours, weak })}`)

export const fetchOpsTimeseries = (hours: number) =>
  getJSON<OpsTimeseries>(`/api/rag/ops/timeseries${qs({ hours })}`)

export interface DocListParams {
  status?: string
  q?: string
  errors?: boolean
  missing?: boolean
  hours?: number
  sort?: string
  dir?: 'asc' | 'desc'
  page?: number
  size?: number
}

export const fetchOpsDocuments = (p: DocListParams) =>
  getJSON<OpsDocPage>(`/api/rag/ops/documents${qs({ ...p })}`)

export const fetchOpsDocumentDetail = (id: string) =>
  getJSON<OpsDocDetail>(`/api/rag/ops/documents/${id}`)

export interface QueryListParams {
  hours?: number
  status?: string
  feedback?: string
  noResults?: boolean
  weak?: number
  model?: string
  sort?: string
  dir?: 'asc' | 'desc'
  page?: number
  size?: number
}

export const fetchOpsQueries = (p: QueryListParams) =>
  getJSON<OpsQueryPage>(`/api/rag/ops/queries${qs({ ...p })}`)

export const fetchOpsQueryTrace = (id: string) =>
  getJSON<OpsQueryTrace>(`/api/rag/ops/queries/${id}`)

export const fetchOpsTokens = (hours: number) =>
  getJSON<OpsTokensReport>(`/api/rag/ops/tokens${qs({ hours })}`)

export const fetchOpsModels = (hours: number) =>
  getJSON<OpsModelsResponse>(`/api/rag/ops/models${qs({ hours })}`)

export const fetchOpsIntegrity = () =>
  getJSON<{ checks: OpsIntegrityCheck[] }>('/api/rag/ops/integrity')

export const fetchOpsConfig = () =>
  getJSON<{ entries: OpsConfigEntry[] }>('/api/rag/ops/config')
