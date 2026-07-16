import { computed, ref } from 'vue'
import {
  createConversation as createServerConversation,
  deleteServerConversation,
  sendRagFeedback,
  streamChat,
  streamRagAsk,
} from '@/lib/api'
import { uid } from '@/lib/utils'
import type { AttachedDoc, ChatMessage, Conversation } from '@/types'

// Identifica la sesión del navegador para la trazabilidad en rag_queries.
const SESSION_KEY = 'gogioia:session'

function sessionId(): string {
  let id = localStorage.getItem(SESSION_KEY)
  if (!id) {
    id = uid()
    localStorage.setItem(SESSION_KEY, id)
  }
  return id
}

// Conversations live in the browser: this is a local, single-user tool with no
// server-side store. Cap how many we keep so localStorage can't grow unbounded.
const STORAGE_KEY = 'gogioia:conversations'
const MAX_CONVERSATIONS = 40
const TITLE_MAX = 48

/** Read saved conversations, newest first. Tolerant of missing/old/bad data. */
function loadConversations(): Conversation[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw) as Conversation[]
    if (!Array.isArray(parsed)) return []
    return parsed
      .filter((c) => c && typeof c.id === 'string')
      .map((c) => ({
        id: c.id,
        title: c.title ?? '',
        // A reply interrupted by a reload must not stay stuck "typing".
        messages: (c.messages ?? []).map((m) => ({ ...m, streaming: false })),
        docs: c.docs ?? [],
        createdAt: c.createdAt ?? Date.now(),
        updatedAt: c.updatedAt ?? c.createdAt ?? Date.now(),
      }))
      .sort((a, b) => b.updatedAt - a.updatedAt)
  } catch {
    return []
  }
}

/** First user message, condensed into a short sidebar label. */
function deriveTitle(text: string): string {
  const clean = text.replace(/\s+/g, ' ').trim()
  return clean.length > TITLE_MAX ? `${clean.slice(0, TITLE_MAX).trimEnd()}…` : clean
}

// Cap how much document text we send per request (the backend caps again).
const MAX_DOC_CHARS = 24_000

// Ventana del historial de respaldo cuando no hay conversación server-side.
const LOCAL_HISTORY_WINDOW = 12

// Tras fallar la creación de la conversación server-side (Oracle caído), no
// se reintenta en cada envío para no añadir latencia.
const SERVER_CONV_BACKOFF_MS = 60_000
let serverConvBackoffUntil = 0

/** Reactive chat state + streaming logic, shared by the app shell. */
export function useChat() {
  // All conversations, newest first. The active one's messages/docs arrays are
  // shared by reference with `messages`/`docs` below, so live edits (including
  // streamed tokens) flow straight into the stored conversation.
  const conversations = ref<Conversation[]>([])
  const activeId = ref('')
  const messages = ref<ChatMessage[]>([])
  const docs = ref<AttachedDoc[]>([])
  const isStreaming = ref(false)
  const error = ref<string | null>(null)
  // Selected Ollama model; empty => backend uses its configured default.
  const model = ref('')
  // When on, questions are answered by the RAG assistant (Oracle 23ai + Mistral).
  const ragMode = ref(localStorage.getItem('gogioia:rag') === '1')

  let controller: AbortController | null = null

  function setModel(name: string) {
    model.value = name
  }

  function setRagMode(on: boolean) {
    ragMode.value = on
    localStorage.setItem('gogioia:rag', on ? '1' : '0')
  }

  const isEmpty = computed(() => messages.value.length === 0)

  /** Adjuntos de la conversación en el formato que consume el backend. */
  function docsPayload() {
    return docs.value.map((d) => ({ name: d.filename, text: d.text.slice(0, MAX_DOC_CHARS) }))
  }

  /**
   * Garantiza (perezosamente) la conversación server-side: da contexto y
   * persistencia en Oracle. Si falla (p.ej. Oracle caído) se sigue en modo
   * local y se reintenta pasado el backoff.
   */
  async function ensureServerConversation(conv: Conversation | undefined, firstMessage: string, rag: boolean) {
    if (!conv || conv.serverId || Date.now() < serverConvBackoffUntil) return
    try {
      conv.serverId = await createServerConversation(
        sessionId(),
        conv.title || deriveTitle(firstMessage),
        rag ? 'rag' : 'chat',
        model.value,
      )
      persist()
    } catch {
      serverConvBackoffUntil = Date.now() + SERVER_CONV_BACKOFF_MS
    }
  }

  async function send(text: string) {
    const trimmed = text.trim()
    if (!trimmed || isStreaming.value) return
    error.value = null

    // Pin the conversation this reply belongs to, so switching chats mid-stream
    // can't misdirect tokens or leave the wrong message stuck "typing".
    const conv = activeConv()
    const convMessages = messages.value

    const userMsg: ChatMessage = {
      id: uid(),
      role: 'user',
      content: trimmed,
      createdAt: Date.now(),
    }
    const rag = ragMode.value
    const assistantMsg: ChatMessage = {
      id: uid(),
      role: 'assistant',
      content: '',
      streaming: true,
      rag,
      createdAt: Date.now(),
    }
    convMessages.push(userMsg, assistantMsg)
    if (conv && !conv.title) conv.title = deriveTitle(trimmed)
    touch(conv)

    isStreaming.value = true
    controller = new AbortController()

    const onToken = (token: string) => {
      const target = convMessages.find((m) => m.id === assistantMsg.id)
      if (target) target.content += token
    }

    try {
      // Conversación persistida en Oracle: contexto server-side + historial.
      await ensureServerConversation(conv, trimmed, rag)
      const serverId = conv?.serverId

      if (rag) {
        // Asistente RAG: el backend recupera contexto de Oracle 23ai y
        // genera con Mistral; los adjuntos viajan como contexto adicional y
        // la conversación server-side aporta memoria de seguimiento.
        await streamRagAsk(
          trimmed,
          sessionId(),
          docsPayload(),
          serverId,
          {
            onSources: (queryId, sources) => {
              const target = convMessages.find((m) => m.id === assistantMsg.id)
              if (target) {
                target.queryId = queryId
                target.sources = sources
              }
            },
            onToken,
          },
          controller.signal,
        )
      } else {
        // Con conversación server-side solo viaja el mensaje nuevo; si no la
        // hay (Oracle caído), se envía el historial local como respaldo.
        const history = serverId
          ? undefined
          : convMessages
              .filter((m) => m.id !== userMsg.id && m.id !== assistantMsg.id && !m.error && m.content)
              .slice(-LOCAL_HISTORY_WINDOW)
              .map((m) => ({ role: m.role, content: m.content }))
        await streamChat(
          {
            conversationId: serverId,
            message: trimmed,
            history,
            documents: docsPayload(),
            model: model.value,
          },
          { onToken },
          controller.signal,
        )
      }
    } catch (e) {
      // A user-initiated abort is not an error — keep whatever streamed so far.
      if (!(e instanceof DOMException && e.name === 'AbortError')) {
        const message = e instanceof Error ? e.message : 'Error desconocido'
        const target = convMessages.find((m) => m.id === assistantMsg.id)
        if (target) {
          target.error = true
          if (!target.content) target.content = `⚠️ ${message}`
        }
        error.value = message
      }
    } finally {
      const target = convMessages.find((m) => m.id === assistantMsg.id)
      if (target) target.streaming = false
      isStreaming.value = false
      controller = null
      touch(conv)
    }
  }

  function stop() {
    controller?.abort()
  }

  /** Valora una respuesta del asistente RAG y persiste la marca en la UI. */
  async function rateMessage(messageId: string, rating: number) {
    const msg = messages.value.find((m) => m.id === messageId)
    if (!msg?.queryId || msg.feedback === rating) return
    await sendRagFeedback(msg.queryId, rating)
    msg.feedback = rating
    touch(activeConv())
  }

  function addDoc(doc: AttachedDoc) {
    docs.value.push(doc)
    touch(activeConv())
  }

  function removeDoc(id: string) {
    const conv = activeConv()
    docs.value = docs.value.filter((d) => d.id !== id)
    if (conv) conv.docs = docs.value
    touch(conv)
  }

  // ── Conversation history ───────────────────────────────────────────────────

  function activeConv(): Conversation | undefined {
    return conversations.value.find((c) => c.id === activeId.value)
  }

  /** Write the whole list to localStorage (best-effort; storage may be full). */
  function persist() {
    if (conversations.value.length > MAX_CONVERSATIONS) {
      conversations.value = conversations.value.slice(0, MAX_CONVERSATIONS)
    }
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(conversations.value))
    } catch {
      /* quota exceeded or storage disabled — keep working in memory */
    }
  }

  /** Bump a conversation's timestamp, resort newest-first, and persist. */
  function touch(conv: Conversation | undefined) {
    if (conv) conv.updatedAt = Date.now()
    conversations.value.sort((a, b) => b.updatedAt - a.updatedAt)
    persist()
  }

  /** Point the live editor at a stored conversation. */
  function activate(id: string) {
    const conv = conversations.value.find((c) => c.id === id)
    if (!conv || conv.id === activeId.value) return
    stop()
    error.value = null
    activeId.value = conv.id
    messages.value = conv.messages
    docs.value = conv.docs
  }

  function createConversation(): Conversation {
    const conv: Conversation = {
      id: uid(),
      title: '',
      messages: [],
      docs: [],
      createdAt: Date.now(),
      updatedAt: Date.now(),
    }
    conversations.value.unshift(conv)
    stop()
    error.value = null
    activeId.value = conv.id
    messages.value = conv.messages
    docs.value = conv.docs
    persist()
    return conv
  }

  /** "Nueva conversación": archive the current chat and start a blank one —
   *  never destroy history. Reuses an already-empty chat to avoid stacking blanks. */
  function newConversation() {
    const conv = activeConv()
    if (conv && conv.messages.length === 0) return
    createConversation()
  }

  function selectConversation(id: string) {
    activate(id)
  }

  function deleteConversation(id: string) {
    const idx = conversations.value.findIndex((c) => c.id === id)
    if (idx === -1) return
    const serverId = conversations.value[idx].serverId
    if (serverId) void deleteServerConversation(serverId).catch(() => {})
    conversations.value.splice(idx, 1)
    if (activeId.value === id) {
      // Fall back to the next chat, or a fresh blank if none remain.
      if (conversations.value.length > 0) {
        activeId.value = ''
        activate(conversations.value[0].id)
      } else {
        createConversation()
      }
    }
    persist()
  }

  // Restore saved history on startup, or open with a blank conversation.
  conversations.value = loadConversations()
  if (conversations.value.length > 0) {
    activate(conversations.value[0].id)
  } else {
    createConversation()
  }

  return {
    conversations,
    activeId,
    messages,
    docs,
    isStreaming,
    error,
    isEmpty,
    model,
    setModel,
    ragMode,
    setRagMode,
    send,
    stop,
    rateMessage,
    newConversation,
    selectConversation,
    deleteConversation,
    addDoc,
    removeDoc,
  }
}
