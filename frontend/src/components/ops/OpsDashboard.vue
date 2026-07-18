<script setup lang="ts">
// Área «Operaciones RAG»: contenedor a pantalla completa con navegación
// interna de 8 vistas. La app no usa vue-router (SPA de una vista); la
// navegación usa rutas reales (/ops/<vista>) vía History API — el fallback
// SPA del servidor Go sirve index.html para cualquier ruta, así que los
// enlaces profundos y el botón atrás funcionan sin hash.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  Activity,
  ArrowLeft,
  Boxes,
  Cpu,
  Database,
  FileSearch,
  Gauge,
  MessagesSquare,
  Settings2,
  ShieldCheck,
} from 'lucide-vue-next'
import OpsOverview from './OpsOverview.vue'
import OpsIngestion from './OpsIngestion.vue'
import OpsTokens from './OpsTokens.vue'
import OpsRetrieval from './OpsRetrieval.vue'
import OpsQueries from './OpsQueries.vue'
import OpsModels from './OpsModels.vue'
import OpsIntegrity from './OpsIntegrity.vue'
import OpsConfig from './OpsConfig.vue'
import { t } from '@/i18n'

const emit = defineEmits<{ close: [] }>()

type ViewId =
  | 'overview'
  | 'ingestion'
  | 'tokens'
  | 'retrieval'
  | 'queries'
  | 'models'
  | 'integrity'
  | 'config'

const views: { id: ViewId; label: string; icon: unknown }[] = [
  { id: 'overview', label: t.ops.nav.overview, icon: Gauge },
  { id: 'ingestion', label: t.ops.nav.ingestion, icon: Database },
  { id: 'tokens', label: t.ops.nav.tokens, icon: Boxes },
  { id: 'retrieval', label: t.ops.nav.retrieval, icon: FileSearch },
  { id: 'queries', label: t.ops.nav.queries, icon: MessagesSquare },
  { id: 'models', label: t.ops.nav.models, icon: Cpu },
  { id: 'integrity', label: t.ops.nav.integrity, icon: ShieldCheck },
  { id: 'config', label: t.ops.nav.config, icon: Settings2 },
]

const current = ref<ViewId>('overview')
const hours = ref(24)

const ranges = [
  { value: 6, label: t.ops.range.h6 },
  { value: 24, label: t.ops.range.h24 },
  { value: 72, label: t.ops.range.h72 },
  { value: 168, label: t.ops.range.h168 },
]

function readPath() {
  const m = window.location.pathname.match(/^\/ops\/?([a-z]*)/)
  if (!m) return
  const v = views.find((x) => x.id === m[1])
  current.value = v ? v.id : 'overview'
}

function go(view: ViewId) {
  current.value = view
  window.history.pushState(null, '', `/ops/${view}`)
}

function close() {
  window.history.pushState(null, '', '/')
  emit('close')
}

function onPopstate() {
  // Si el usuario retrocede fuera de /ops, App.vue cierra el área.
  if (window.location.pathname.startsWith('/ops')) readPath()
}

onMounted(() => {
  if (window.location.pathname.startsWith('/ops')) {
    readPath()
  } else {
    // Abierta desde la cabecera: entrada nueva para que «atrás» vuelva al chat.
    window.history.pushState(null, '', '/ops/overview')
  }
  window.addEventListener('popstate', onPopstate)
})
onBeforeUnmount(() => window.removeEventListener('popstate', onPopstate))

const currentView = computed(() => {
  switch (current.value) {
    case 'ingestion':
      return OpsIngestion
    case 'tokens':
      return OpsTokens
    case 'retrieval':
      return OpsRetrieval
    case 'queries':
      return OpsQueries
    case 'models':
      return OpsModels
    case 'integrity':
      return OpsIntegrity
    case 'config':
      return OpsConfig
    default:
      return OpsOverview
  }
})

// Solo Resumen/Tokens/Retrieval/Modelos dependen del rango temporal.
const rangeApplies = computed(() =>
  ['overview', 'tokens', 'retrieval', 'models', 'queries', 'ingestion'].includes(current.value),
)
</script>

<template>
  <div class="fixed inset-0 z-[55] flex flex-col bg-background text-foreground">
    <!-- Cabecera -->
    <header class="flex h-14 shrink-0 items-center gap-3 border-b border-border px-4">
      <button
        class="grid h-9 w-9 shrink-0 place-items-center rounded-sm border border-border transition hover:bg-muted"
        :title="t.ops.backToChat"
        @click="close"
      >
        <ArrowLeft class="h-4 w-4" />
      </button>
      <div class="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-primary text-primary-foreground">
        <Activity class="h-4 w-4" />
      </div>
      <div class="min-w-0 flex-1 leading-none">
        <h2 class="truncate text-lg font-extrabold tracking-tight">{{ t.ops.title }}</h2>
        <div class="mt-1 hidden text-[10px] font-medium uppercase tracking-[0.2em] text-muted-foreground sm:block">
          {{ t.ops.subtitle }}
        </div>
      </div>
      <label v-if="rangeApplies" class="flex items-center gap-2 text-xs text-muted-foreground">
        <span class="hidden sm:inline">{{ t.ops.range.label }}</span>
        <select v-model.number="hours" class="h-8 rounded-sm border border-border bg-card px-2 text-xs">
          <option v-for="r in ranges" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </label>
    </header>

    <div class="flex min-h-0 flex-1">
      <!-- Navegación lateral (≥md) -->
      <nav class="hidden w-52 shrink-0 flex-col gap-1 overflow-y-auto border-r border-border p-2 md:flex" aria-label="Vistas de operaciones">
        <button
          v-for="v in views"
          :key="v.id"
          class="flex items-center gap-2.5 rounded-sm px-3 py-2 text-left text-sm transition"
          :class="
            current === v.id
              ? 'bg-primary text-primary-foreground font-bold'
              : 'text-muted-foreground hover:bg-muted hover:text-foreground'
          "
          :aria-current="current === v.id ? 'page' : undefined"
          @click="go(v.id)"
        >
          <component :is="v.icon" class="h-4 w-4 shrink-0" />
          <span class="truncate">{{ v.label }}</span>
        </button>
      </nav>

      <!-- Navegación superior (móvil) -->
      <div class="flex min-w-0 flex-1 flex-col">
        <nav class="flex shrink-0 gap-1 overflow-x-auto border-b border-border p-2 md:hidden" aria-label="Vistas de operaciones">
          <button
            v-for="v in views"
            :key="v.id"
            class="shrink-0 rounded-sm px-3 py-1.5 text-xs transition"
            :class="current === v.id ? 'bg-primary text-primary-foreground font-bold' : 'text-muted-foreground hover:bg-muted'"
            @click="go(v.id)"
          >
            {{ v.label }}
          </button>
        </nav>

        <main class="min-h-0 flex-1 overflow-y-auto p-4 sm:p-5">
          <component :is="currentView" :hours="hours" @navigate="go($event as ViewId)" />
        </main>
      </div>
    </div>
  </div>
</template>
