<script setup lang="ts">
// Vista «Ingesta»: tabla de documentos con filtros/orden/paginación
// server-side y cajón de detalle con la línea de tiempo de eventos.
// Acciones: las que soporta la arquitectura actual — inspeccionar el fallo y
// eliminar (con confirmación); no hay retry server-side porque el binario no
// conserva el PDF: resubir el mismo archivo reanuda la ingesta fallida.
import { computed, ref, watch } from 'vue'
import { Loader2, RefreshCw, Trash2 } from 'lucide-vue-next'
import Button from '../ui/Button.vue'
import StatusPill from '../StatusPill.vue'
import OpsDrawer from './OpsDrawer.vue'
import HBarChart from './HBarChart.vue'
import { deleteRagDocument } from '@/lib/api'
import {
  fetchOpsDocumentDetail,
  fetchOpsDocuments,
  type OpsDocDetail,
  type OpsDocPage,
  type OpsDocRow,
} from '@/lib/opsApi'
import { fmtBytes, fmtDateTime, fmtDuration, fmtMs, fmtNum } from '@/lib/format'
import { t } from '@/i18n'

const props = defineProps<{ hours: number }>()

const page = ref<OpsDocPage | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

const status = ref('')
const q = ref('')
const onlyErrors = ref(false)
const onlyMissing = ref(false)
const useRange = ref(false)
const sort = ref('uploaded')
const dir = ref<'asc' | 'desc'>('desc')
const pageNum = ref(0)
const size = 25

let searchTimer: number | undefined

async function load() {
  loading.value = true
  error.value = null
  try {
    page.value = await fetchOpsDocuments({
      status: status.value || undefined,
      q: q.value || undefined,
      errors: onlyErrors.value,
      missing: onlyMissing.value,
      hours: useRange.value ? props.hours : undefined,
      sort: sort.value,
      dir: dir.value,
      page: pageNum.value,
      size,
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch([status, onlyErrors, onlyMissing, useRange, sort, dir, () => props.hours], () => {
  pageNum.value = 0
  void load()
})
watch(pageNum, load)
watch(q, () => {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    pageNum.value = 0
    void load()
  }, 350)
})
void load()
defineExpose({ load })

const totalPages = computed(() => (page.value ? Math.max(1, Math.ceil(page.value.total / size)) : 1))

function toggleSort(col: string) {
  if (sort.value === col) dir.value = dir.value === 'asc' ? 'desc' : 'asc'
  else {
    sort.value = col
    dir.value = 'desc'
  }
}

function statusState(s: string): 'ok' | 'fault' | 'wait' {
  if (s === 'EMBEDDED') return 'ok'
  if (s === 'FAILED') return 'fault'
  return 'wait'
}

function progress(d: OpsDocRow): string {
  if (d.chunkCount === 0) return d.status === 'EMBEDDED' ? '100 %' : '—'
  return `${Math.round((d.embeddedChunks / d.chunkCount) * 100)} %`
}

// ── Detalle ────────────────────────────────────────────────────────────
const detail = ref<OpsDocDetail | null>(null)
const detailLoading = ref(false)

async function openDetail(id: string) {
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await fetchOpsDocumentDetail(id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    detailLoading.value = false
  }
}

async function onDelete(doc: OpsDocRow) {
  if (!window.confirm(t.rag.confirmDelete)) return
  try {
    await deleteRagDocument(doc.id)
    detail.value = null
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

const histItems = computed(() =>
  (detail.value?.tokenHist ?? []).map((b) => ({
    label: `${b.from}–`,
    value: b.count,
    hint: `${b.count} chunks de ${b.from}+ tokens (estimados)`,
  })),
)
</script>

<template>
  <div class="space-y-3">
    <!-- Filtros -->
    <div class="flex flex-wrap items-center gap-2">
      <input
        v-model="q"
        type="search"
        placeholder="Buscar por nombre…"
        class="h-8 w-56 rounded-sm border border-border bg-card px-2 text-xs outline-none focus:ring-2 focus:ring-ring"
      />
      <select v-model="status" class="h-8 rounded-sm border border-border bg-card px-2 text-xs">
        <option value="">Todos los estados</option>
        <option value="PROCESSING">En proceso</option>
        <option value="EMBEDDED">Entrenados</option>
        <option value="FAILED">Fallidos</option>
      </select>
      <label class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <input v-model="onlyErrors" type="checkbox" class="accent-[color:var(--epm-bosque)]" /> Con errores
      </label>
      <label class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <input v-model="onlyMissing" type="checkbox" class="accent-[color:var(--epm-bosque)]" /> Vectores incompletos
      </label>
      <label class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <input v-model="useRange" type="checkbox" class="accent-[color:var(--epm-bosque)]" /> Solo últimas {{ hours }} h
      </label>
      <Button variant="outline" size="sm" class="ml-auto h-8" @click="load">
        <RefreshCw class="h-3.5 w-3.5" /> {{ t.ops.refresh }}
      </Button>
    </div>

    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>

    <!-- Tabla -->
    <div class="overflow-x-auto rounded-md border border-border">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-border bg-muted/40 text-left text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
            <th class="cursor-pointer px-3 py-2" @click="toggleSort('name')">Documento</th>
            <th class="cursor-pointer px-3 py-2 text-right" @click="toggleSort('pages')">Págs.</th>
            <th class="cursor-pointer px-3 py-2 text-right" @click="toggleSort('chunks')">Chunks</th>
            <th class="px-3 py-2 text-right" title="Chunks con embedding / totales">Vectores</th>
            <th class="px-3 py-2 text-right">Progreso</th>
            <th class="cursor-pointer px-3 py-2 text-right" title="Tokens estimados (~4 chars/token)" @click="toggleSort('tokens')">Tokens est.</th>
            <th class="cursor-pointer px-3 py-2 text-right" @click="toggleSort('size')">Tamaño</th>
            <th class="cursor-pointer px-3 py-2" @click="toggleSort('status')">Estado</th>
            <th class="cursor-pointer px-3 py-2" @click="toggleSort('uploaded')">Subido</th>
            <th class="px-3 py-2 text-right">Duración</th>
            <th class="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="11" class="px-3 py-6 text-center text-xs text-muted-foreground">
              <Loader2 class="mr-1 inline h-4 w-4 animate-spin" /> Cargando…
            </td>
          </tr>
          <tr v-else-if="!page || page.rows.length === 0">
            <td colspan="11" class="px-3 py-6 text-center text-xs text-muted-foreground">
              {{ t.ops.empty }}
            </td>
          </tr>
          <tr
            v-for="d in page?.rows ?? []"
            v-else
            :key="d.id"
            class="cursor-pointer border-b border-border/60 transition last:border-0 hover:bg-muted/30"
            @click="openDetail(d.id)"
          >
            <td class="max-w-[240px] px-3 py-2">
              <div class="truncate font-medium" :title="d.fileName">{{ d.fileName }}</div>
              <div v-if="d.error" class="mt-0.5 truncate text-xs text-[color:var(--signal-fault)]" :title="d.error">
                {{ d.error }}
              </div>
            </td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ d.pageCount || '—' }}</td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ d.chunkCount || '—' }}</td>
            <td class="px-3 py-2 text-right font-mono text-xs" :class="d.missingChunks > 0 ? 'text-[color:var(--signal-warn)]' : ''">
              {{ d.embeddedChunks }}/{{ d.chunkCount }}
            </td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ progress(d) }}</td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtNum(d.estTokens) }}</td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtBytes(d.sizeBytes) }}</td>
            <td class="px-3 py-2">
              <StatusPill :state="statusState(d.status)" :value="t.rag.status[d.status] ?? d.status" :pulse="false" />
            </td>
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs text-muted-foreground">
              {{ fmtDateTime(d.uploadedAt) }}
            </td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtDuration(d.durationSec) }}</td>
            <td class="px-3 py-2 text-right" @click.stop>
              <Button
                variant="ghost"
                size="icon"
                class="h-7 w-7 text-muted-foreground hover:text-[color:var(--signal-fault)]"
                :title="t.rag.delete"
                @click="onDelete(d)"
              >
                <Trash2 />
              </Button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Paginación -->
    <div v-if="page && page.total > size" class="flex items-center justify-between text-xs text-muted-foreground">
      <span>{{ fmtNum(page.total) }} documentos</span>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" class="h-7" :disabled="pageNum === 0" @click="pageNum--">←</Button>
        <span class="font-mono">{{ pageNum + 1 }} / {{ totalPages }}</span>
        <Button variant="outline" size="sm" class="h-7" :disabled="pageNum + 1 >= totalPages" @click="pageNum++">→</Button>
      </div>
    </div>

    <!-- Cajón de detalle -->
    <OpsDrawer
      v-if="detail || detailLoading"
      :title="detail?.fileName ?? 'Cargando…'"
      :subtitle="detail ? `doc ${detail.id}` : ''"
      @close="detail = null; detailLoading = false"
    >
      <div v-if="detailLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 class="h-4 w-4 animate-spin" /> Cargando detalle…
      </div>
      <div v-else-if="detail" class="space-y-4 text-sm">
        <!-- Metadatos -->
        <div class="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-md border border-border bg-card p-3 text-xs md:grid-cols-3">
          <div><span class="text-muted-foreground">Estado:</span> {{ t.rag.status[detail.status] ?? detail.status }}</div>
          <div><span class="text-muted-foreground">Tamaño:</span> {{ fmtBytes(detail.sizeBytes) }}</div>
          <div><span class="text-muted-foreground">Páginas:</span> {{ detail.pageCount || '—' }} ({{ detail.pagesStored }} con texto)</div>
          <div><span class="text-muted-foreground">Chunks:</span> {{ detail.chunkCount }} ({{ detail.embeddedChunks }} con vector)</div>
          <div>
            <span class="text-muted-foreground">Tokens est.:</span> {{ fmtNum(detail.estTokens) }}
            <span class="rounded-sm bg-muted px-1 font-mono text-[9px] uppercase" :title="t.ops.tokenSource.estimated">{{ t.ops.tokenSource.badgeEstimated }}</span>
          </div>
          <div><span class="text-muted-foreground">Tokens/chunk:</span> {{ detail.minChunkTokens }}–{{ detail.maxChunkTokens }} (media {{ detail.avgChunkTokens }})</div>
          <div><span class="text-muted-foreground">Modelo embed:</span> {{ detail.models || '—' }}</div>
          <div><span class="text-muted-foreground">Subido:</span> {{ fmtDateTime(detail.uploadedAt) }} {{ detail.uploadedBy ? `· ${detail.uploadedBy}` : '' }}</div>
          <div><span class="text-muted-foreground">Completado:</span> {{ fmtDateTime(detail.processedAt) }} ({{ fmtDuration(detail.durationSec) }})</div>
          <div><span class="text-muted-foreground">Citado en respuestas:</span> {{ detail.timesCited }}</div>
          <div class="col-span-2 md:col-span-3 truncate"><span class="text-muted-foreground">SHA-256:</span> <span class="font-mono text-[10px]">{{ detail.hash }}</span></div>
        </div>

        <div
          v-if="detail.error"
          class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] p-3 text-xs"
        >
          <div class="font-bold text-[color:var(--signal-fault)]">Último error</div>
          <div class="mt-1 break-words">{{ detail.error }}</div>
          <div class="mt-2 text-muted-foreground">
            Para reintentar: vuelve a subir el mismo PDF — la ingesta se reanuda desde el primer chunk pendiente
            (los ya vectorizados se conservan).
          </div>
        </div>

        <!-- Distribución de tamaño de chunk -->
        <div v-if="histItems.length > 0">
          <h4 class="mb-2 text-xs font-bold">
            Distribución de tokens por chunk
            <span class="rounded-sm bg-muted px-1 font-mono text-[9px] uppercase text-muted-foreground" :title="t.ops.tokenSource.estimated">{{ t.ops.tokenSource.badgeEstimated }}</span>
          </h4>
          <HBarChart label="Distribución de tokens por chunk" :items="histItems" :max-rows="16" />
        </div>

        <!-- Muestras de chunks -->
        <div v-if="(detail.chunks ?? []).length > 0">
          <h4 class="mb-2 text-xs font-bold">Muestras de chunks</h4>
          <div v-for="c in detail.chunks" :key="c.index" class="mb-2 rounded-sm border border-border bg-card p-2 text-xs">
            <div class="mb-1 font-mono text-[10px] text-muted-foreground">
              chunk #{{ c.index }} · pág. {{ c.page }} · ~{{ c.tokens }} tokens
            </div>
            <div class="break-words text-muted-foreground">{{ c.snippet }}…</div>
          </div>
        </div>

        <!-- Línea de tiempo de eventos -->
        <div>
          <h4 class="mb-2 text-xs font-bold">Eventos de procesamiento</h4>
          <p v-if="detail.events.length === 0" class="text-xs text-muted-foreground">
            Sin eventos registrados (el documento se ingirió antes de activar la instrumentación).
          </p>
          <ol v-else class="space-y-1">
            <li
              v-for="ev in detail.events"
              :key="ev.id"
              class="flex flex-wrap items-baseline gap-x-2 rounded-sm border border-border/60 px-2 py-1 text-xs"
            >
              <span class="font-mono text-[10px] text-muted-foreground">{{ fmtDateTime(ev.createdAt) }}</span>
              <span class="font-semibold">{{ ev.kind }}</span>
              <span v-if="ev.detail" class="text-muted-foreground">{{ ev.detail }}</span>
              <span
                class="font-mono text-[10px]"
                :class="ev.status === 'OK' ? 'text-[color:var(--signal-ok)]' : 'text-[color:var(--signal-fault)]'"
              >
                {{ ev.status }}
              </span>
              <span v-if="ev.latencyMs" class="font-mono text-[10px] text-muted-foreground">{{ fmtMs(ev.latencyMs) }}</span>
              <span v-if="ev.batchSize" class="font-mono text-[10px] text-muted-foreground">lote {{ ev.batchSize }}</span>
              <span v-if="ev.tokensIn" class="font-mono text-[10px] text-muted-foreground">{{ fmtNum(ev.tokensIn) }} tok</span>
              <span v-if="ev.attempts && ev.attempts > 1" class="font-mono text-[10px] text-[color:var(--signal-warn)]">{{ ev.attempts }} intentos</span>
              <span v-if="ev.errorDetail" class="w-full break-words text-[color:var(--signal-fault)]">{{ ev.errorDetail }}</span>
            </li>
          </ol>
        </div>
      </div>
    </OpsDrawer>
  </div>
</template>
