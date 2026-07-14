<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Bot, Menu, WifiOff } from 'lucide-vue-next'
import Sidebar from './components/Sidebar.vue'
import ChatMessage from './components/ChatMessage.vue'
import ChatInput from './components/ChatInput.vue'
import StatusPill from './components/StatusPill.vue'
import ModelSelect from './components/ModelSelect.vue'
import { useChat } from './composables/useChat'
import { fetchConfig, fetchHealth, fetchModels, uploadPdf } from './lib/api'
import { uid } from './lib/utils'
import { t } from './i18n'
import type { HealthStatus, ModelsResponse } from './types'

const {
  conversations,
  activeId,
  messages,
  docs,
  isStreaming,
  isEmpty,
  setModel,
  contractMode,
  setContractMode,
  send,
  stop,
  newConversation,
  selectConversation,
  deleteConversation,
  addDoc,
  removeDoc,
} = useChat()

const model = ref('')
const models = ref<string[]>([])
const selectedModel = ref('')
const ollamaHost = ref('')
const health = ref<HealthStatus | null>(null)
const uploading = ref(false)
const uploadError = ref<string | null>(null)
const dark = ref(true)
const collapsed = ref(false)
const mobileOpen = ref(false)
const scrollEl = ref<HTMLElement | null>(null)

const isOffline = computed(() => health.value?.ollama === 'offline')
const pillState = computed<'ok' | 'fault' | 'idle'>(() => {
  if (!health.value) return 'idle'
  return health.value.ollama === 'online' ? 'ok' : 'fault'
})
const pillValue = computed(() =>
  health.value ? (health.value.ollama === 'online' ? t.status.online : t.status.offline) : t.status.checking,
)

onMounted(async () => {
  dark.value = readTheme()
  collapsed.value = localStorage.getItem('gogioia:collapsed') === '1'
  applyTheme()
  window.addEventListener('keydown', onKeydown)
  try {
    const cfg = await fetchConfig()
    model.value = cfg.model
    ollamaHost.value = cfg.ollama.replace(/\/api\/.*$/, '')
    selectedModel.value = localStorage.getItem('gogioia:model') || cfg.model
    setModel(selectedModel.value)
  } catch {
    /* config es best-effort */
  }
  await loadModels()
  refreshHealth()
  window.setInterval(refreshHealth, 15_000)
})

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') mobileOpen.value = false
}

async function refreshHealth() {
  try {
    health.value = await fetchHealth()
    // A host that just became reachable may not have been listed yet.
    if (health.value.ollama === 'online' && models.value.length === 0) await loadModels()
  } catch {
    health.value = { ollama: 'offline', detail: '', model: model.value }
  }
}

async function loadModels() {
  try {
    const res = await fetchModels()
    models.value = res.models
    const pick = resolveModel(res, localStorage.getItem('gogioia:model') || '')
    selectedModel.value = pick
    setModel(pick)
  } catch {
    /* best-effort — keep the current selection */
  }
}

// Resolve which model to preselect: a valid saved choice wins, else the server
// default (exact or tag-normalised), else the first listed model.
function resolveModel(res: ModelsResponse, saved: string): string {
  const list = res.models
  if (saved && list.includes(saved)) return saved
  if (list.length === 0) return saved || res.default
  return (
    list.find((m) => m === res.default) ??
    list.find((m) => m.startsWith(res.default + ':')) ??
    list[0]
  )
}

function onSelectModel(name: string) {
  selectedModel.value = name
  setModel(name)
  localStorage.setItem('gogioia:model', name)
}

function readTheme(): boolean {
  const saved = localStorage.getItem('gogioia:theme')
  return saved ? saved === 'dark' : true
}

function applyTheme() {
  document.documentElement.classList.toggle('dark', dark.value)
}

function toggleTheme() {
  dark.value = !dark.value
  localStorage.setItem('gogioia:theme', dark.value ? 'dark' : 'light')
  applyTheme()
}

function toggleCollapse() {
  collapsed.value = !collapsed.value
  localStorage.setItem('gogioia:collapsed', collapsed.value ? '1' : '0')
}

function onNewChat() {
  newConversation()
  mobileOpen.value = false
}

function onSelectChat(id: string) {
  selectConversation(id)
  mobileOpen.value = false
}

async function onAttach(file: File) {
  uploading.value = true
  uploadError.value = null
  try {
    const res = await uploadPdf(file)
    addDoc({ id: uid(), filename: res.filename, chars: res.chars, text: res.text })
  } catch (e) {
    uploadError.value = e instanceof Error ? e.message : t.doc.uploadFailed
  } finally {
    uploading.value = false
  }
}

function scrollToBottom() {
  nextTick(() => {
    const el = scrollEl.value
    if (el) el.scrollTo({ top: el.scrollHeight })
  })
}

watch(() => messages.value.length, scrollToBottom)
watch(
  () => messages.value.reduce((n, m) => n + m.content.length, 0),
  scrollToBottom,
)
// When a reply finishes, its rendered height can jump (e.g. the contract
// report replaces the streamed JSON), so re-anchor to the bottom.
watch(isStreaming, (streaming) => {
  if (!streaming) scrollToBottom()
})
</script>

<template>
  <div class="flex h-dvh w-full overflow-hidden bg-background text-foreground">
    <!-- Fondo oscuro del cajón móvil -->
    <div
      v-show="mobileOpen"
      class="fixed inset-0 z-40 bg-black/50 backdrop-blur-sm md:hidden"
      @click="mobileOpen = false"
    />

    <Sidebar
      :conversations="conversations"
      :active-id="activeId"
      :docs="docs"
      :model="selectedModel || model"
      :health="health"
      :dark="dark"
      :collapsed="collapsed"
      :mobile-open="mobileOpen"
      @new-chat="onNewChat"
      @select-chat="onSelectChat"
      @delete-chat="deleteConversation"
      @remove-doc="removeDoc"
      @toggle-theme="toggleTheme"
      @toggle-collapse="toggleCollapse"
      @close-mobile="mobileOpen = false"
    />

    <div class="flex min-w-0 flex-1 flex-col">
      <!-- Barra superior -->
      <header
        class="flex h-16 shrink-0 items-center justify-between gap-3 border-b border-border bg-background/80 px-4 backdrop-blur-md sm:px-6"
      >
        <div class="flex min-w-0 items-center gap-3">
          <button
            class="grid h-9 w-9 shrink-0 place-items-center rounded-sm border border-border transition hover:bg-muted md:hidden"
            :aria-label="t.sidebar.openMenu"
            @click="mobileOpen = true"
          >
            <Menu class="h-4 w-4" />
          </button>
          <div class="hidden min-w-0 items-baseline gap-3 sm:flex">
            <h2 class="truncate text-xl font-extrabold tracking-tight sm:text-2xl">{{ t.app.title }}</h2>
            <span class="hidden font-mono text-[11px] uppercase tracking-[0.2em] text-muted-foreground lg:inline">
              / {{ t.app.breadcrumb }}
            </span>
          </div>
        </div>
        <div class="flex shrink-0 items-center gap-2 sm:gap-3">
          <ModelSelect
            :models="models"
            :selected="selectedModel"
            :disabled="models.length === 0"
            @update:selected="onSelectModel"
          />
          <StatusPill label="Ollama" :state="pillState" :value="pillValue" />
        </div>
      </header>

      <!-- Aviso de desconexión: la app sigue funcionando sin Ollama. -->
      <div
        v-if="isOffline"
        class="flex items-start gap-3 border-b border-[color:color-mix(in_srgb,var(--signal-fault)_35%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_12%,transparent)] px-4 py-3 sm:px-6"
        role="status"
      >
        <WifiOff class="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--signal-fault)]" />
        <div class="min-w-0 text-sm">
          <div class="font-bold text-[color:var(--signal-fault)]">{{ t.chat.offlineTitle }}</div>
          <div class="text-muted-foreground">{{ t.chat.offlineBody(ollamaHost || '—') }}</div>
        </div>
      </div>

      <div ref="scrollEl" class="flex-1 overflow-y-auto">
        <!-- Estado inicial -->
        <div v-if="isEmpty" class="flex h-full flex-col items-center justify-center px-4 text-center">
          <div class="grid h-12 w-12 place-items-center rounded-sm bg-primary text-primary-foreground">
            <Bot class="h-6 w-6" />
          </div>
          <p class="mt-4 text-sm text-muted-foreground">{{ t.chat.empty }}</p>
        </div>

        <!-- Conversación -->
        <div v-else class="mx-auto max-w-3xl">
          <ChatMessage v-for="m in messages" :key="m.id" :message="m" />
          <div class="h-4" />
        </div>
      </div>

      <div v-if="uploadError" class="mx-auto w-full max-w-3xl px-4">
        <div
          class="mb-2 rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
        >
          {{ uploadError }}
        </div>
      </div>

      <ChatInput
        :streaming="isStreaming"
        :uploading="uploading"
        :contract-mode="contractMode"
        @send="send"
        @stop="stop"
        @attach="onAttach"
        @toggle-contract="setContractMode(!contractMode)"
      />
    </div>
  </div>
</template>
