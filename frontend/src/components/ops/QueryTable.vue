<script setup lang="ts">
// Tabla de consultas RAG (paginada server-side) + cajón con la traza
// completa: pregunta → embedding → retrieval → contexto → generación →
// citas/fuentes → feedback. Reutilizada por «Consultas» y «Calidad del
// retrieval». No hay reescritura de consulta ni reranking en la app: esos
// pasos no aparecen porque no existen, no porque se oculten.
import { computed, ref, watch } from 'vue'
import { Loader2, ThumbsDown, ThumbsUp } from 'lucide-vue-next'
import Button from '../ui/Button.vue'
import OpsDrawer from './OpsDrawer.vue'
import {
  fetchOpsQueries,
  fetchOpsQueryTrace,
  type OpsQueryPage,
  type OpsQueryTrace,
  type QueryListParams,
} from '@/lib/opsApi'
import { fmtDateTime, fmtMs, fmtNum, fmtScore } from '@/lib/format'

const props = defineProps<{
  hours: number
  status?: string
  feedback?: string
  noResults?: boolean
  weak?: number
  model?: string
}>()

const page = ref<OpsQueryPage | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const sort = ref('created')
const dir = ref<'asc' | 'desc'>('desc')
const pageNum = ref(0)
const size = 25

async function load() {
  loading.value = true
  error.value = null
  try {
    const params: QueryListParams = {
      hours: props.hours,
      status: props.status || undefined,
      feedback: props.feedback || undefined,
      noResults: props.noResults,
      weak: props.weak,
      model: props.model || undefined,
      sort: sort.value,
      dir: dir.value,
      page: pageNum.value,
      size,
    }
    page.value = await fetchOpsQueries(params)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(
  [() => props.hours, () => props.status, () => props.feedback, () => props.noResults, () => props.weak, () => props.model, sort, dir],
  () => {
    pageNum.value = 0
    void load()
  },
  { immediate: true },
)
watch(pageNum, load)
defineExpose({ load })

const totalPages = computed(() => (page.value ? Math.max(1, Math.ceil(page.value.total / size)) : 1))

function toggleSort(col: string) {
  if (sort.value === col) dir.value = dir.value === 'asc' ? 'desc' : 'asc'
  else {
    sort.value = col
    dir.value = 'desc'
  }
}

const statusLabel: Record<string, string> = {
  answered: 'Respondida',
  pending: 'Incompleta',
  error: 'Error',
}

// ── Traza ──────────────────────────────────────────────────────────────
const trace = ref<OpsQueryTrace | null>(null)
const traceLoading = ref(false)

async function openTrace(id: string) {
  traceLoading.value = true
  trace.value = null
  try {
    trace.value = await fetchOpsQueryTrace(id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    traceLoading.value = false
  }
}

const traceEvent = (kind: string) => trace.value?.events.find((e) => e.kind === kind)

// Pasos de la traza en orden de ejecución, solo los que existen en la app.
const traceSteps = computed(() => {
  const t = trace.value
  if (!t) return []
  const qe = traceEvent('query_embed')
  const rt = traceEvent('retrieval')
  const gen = traceEvent('generation')
  return [
    { name: '1 · Pregunta recibida', body: t.question, meta: fmtDateTime(t.createdAt) },
    {
      name: '2 · Embedding de la consulta',
      body: qe
        ? `modelo ${qe.model} · ${fmtNum(qe.tokensIn ?? 0)} tokens (${qe.tokenSource || '—'}) · ${fmtMs(qe.latencyMs)}${(qe.loadMs ?? 0) >= 1000 ? ` · el modelo se (re)cargó (${fmtMs(qe.loadMs ?? 0)})` : ''}`
        : 'Sin evento registrado (consulta anterior a la instrumentación). Dimensión del vector: 768.',
      meta: 'prefijo search_query · VECTOR(768)',
    },
    {
      name: '3 · Retrieval vectorial (coseno)',
      body: rt
        ? `${rt.batchSize ?? 0} candidatos · ${fmtMs(rt.latencyMs)} · ${rt.detail || ''}`
        : `${t.chunks.length} candidatos registrados`,
      meta: 'sin retrieval léxico ni reranking en esta app',
    },
    {
      name: '4 · Contexto seleccionado',
      body: `${t.chunks.filter((c) => c.used).length} de ${t.chunks.length} chunks entraron al prompt`,
      meta: t.template ? `plantilla ${t.template}` : '',
    },
    {
      name: '5 · Generación',
      body: gen
        ? gen.status === 'OK'
          ? `modelo ${gen.model} · ${fmtNum(gen.tokensIn ?? 0)} prompt + ${fmtNum(gen.tokensOut ?? 0)} respuesta (${gen.tokenSource || '—'}) · ${fmtMs(gen.latencyMs)}`
          : `ERROR (${gen.errorKind}): ${gen.errorDetail}`
        : t.response
          ? 'Respuesta guardada (sin métricas: anterior a la instrumentación)'
          : 'Sin respuesta registrada',
      meta: '',
    },
  ]
})
</script>

<template>
  <div class="space-y-3">
    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>

    <div class="overflow-x-auto rounded-md border border-border">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-border bg-muted/40 text-left text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
            <th class="cursor-pointer px-3 py-2" @click="toggleSort('created')">Fecha</th>
            <th class="px-3 py-2">Pregunta</th>
            <th class="px-3 py-2">Estado</th>
            <th class="px-3 py-2 text-right" title="Chunks candidatos / usados en el prompt">Cand./usados</th>
            <th class="cursor-pointer px-3 py-2 text-right" title="Mejor similitud coseno" @click="toggleSort('score')">Score máx.</th>
            <th class="px-3 py-2 text-right" title="Tokens estimados del contexto usado">Ctx est.</th>
            <th class="cursor-pointer px-3 py-2 text-right" title="Tokens prompt + respuesta (exactos de Ollama)" @click="toggleSort('tokens')">Tokens</th>
            <th class="cursor-pointer px-3 py-2 text-right" @click="toggleSort('latency')">Latencia gen.</th>
            <th class="px-3 py-2 text-center">Feedback</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="9" class="px-3 py-6 text-center text-xs text-muted-foreground">
              <Loader2 class="mr-1 inline h-4 w-4 animate-spin" /> Cargando…
            </td>
          </tr>
          <tr v-else-if="!page || page.rows.length === 0">
            <td colspan="9" class="px-3 py-6 text-center text-xs text-muted-foreground">Sin consultas con estos filtros.</td>
          </tr>
          <tr
            v-for="row in page?.rows ?? []"
            v-else
            :key="row.id"
            class="cursor-pointer border-b border-border/60 transition last:border-0 hover:bg-muted/30"
            @click="openTrace(row.id)"
          >
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs text-muted-foreground">{{ fmtDateTime(row.createdAt) }}</td>
            <td class="max-w-[280px] px-3 py-2">
              <div class="truncate" :title="row.preview">{{ row.preview }}</div>
              <div v-if="row.errorKind" class="font-mono text-[10px] text-[color:var(--signal-fault)]">{{ row.errorKind }}</div>
            </td>
            <td class="px-3 py-2">
              <span
                class="rounded-sm px-1.5 py-0.5 text-[10px] font-bold uppercase"
                :class="
                  row.status === 'answered'
                    ? 'bg-[color:color-mix(in_srgb,var(--signal-ok)_14%,transparent)] text-[color:var(--signal-ok)]'
                    : row.status === 'error'
                      ? 'bg-[color:color-mix(in_srgb,var(--signal-fault)_12%,transparent)] text-[color:var(--signal-fault)]'
                      : 'bg-muted text-muted-foreground'
                "
              >
                {{ statusLabel[row.status] }}
              </span>
            </td>
            <td class="px-3 py-2 text-right font-mono text-xs" :class="row.candidates === 0 ? 'text-[color:var(--signal-warn)]' : ''">
              {{ row.candidates }}/{{ row.usedInPrompt }}
            </td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtScore(row.topScore) }}</td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtNum(row.ctxTokensEst) }}</td>
            <td class="px-3 py-2 text-right font-mono text-xs">
              {{ row.promptTokens + row.completionTokens > 0 ? fmtNum(row.promptTokens + row.completionTokens) : '—' }}
            </td>
            <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtMs(row.genLatencyMs) }}</td>
            <td class="px-3 py-2 text-center">
              <ThumbsUp v-if="row.feedback > 0" class="inline h-3.5 w-3.5 text-[color:var(--signal-ok)]" />
              <ThumbsDown v-else-if="row.feedback < 0" class="inline h-3.5 w-3.5 text-[color:var(--signal-fault)]" />
              <span v-else class="text-xs text-muted-foreground">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="page && page.total > size" class="flex items-center justify-between text-xs text-muted-foreground">
      <span>{{ fmtNum(page.total) }} consultas</span>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" class="h-7" :disabled="pageNum === 0" @click="pageNum--">←</Button>
        <span class="font-mono">{{ pageNum + 1 }} / {{ totalPages }}</span>
        <Button variant="outline" size="sm" class="h-7" :disabled="pageNum + 1 >= totalPages" @click="pageNum++">→</Button>
      </div>
    </div>

    <!-- Traza -->
    <OpsDrawer
      v-if="trace || traceLoading"
      title="Traza de la consulta"
      :subtitle="trace ? `query ${trace.id}` : ''"
      @close="trace = null; traceLoading = false"
    >
      <div v-if="traceLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 class="h-4 w-4 animate-spin" /> Cargando traza…
      </div>
      <div v-else-if="trace" class="space-y-4 text-sm">
        <!-- Pasos -->
        <ol class="space-y-2">
          <li v-for="step in traceSteps" :key="step.name" class="rounded-md border border-border bg-card p-3">
            <div class="flex items-baseline justify-between gap-2">
              <div class="text-xs font-bold">{{ step.name }}</div>
              <div v-if="step.meta" class="truncate font-mono text-[10px] text-muted-foreground">{{ step.meta }}</div>
            </div>
            <div class="mt-1 break-words text-xs text-muted-foreground">{{ step.body }}</div>
          </li>
        </ol>

        <!-- Candidatos -->
        <div>
          <h4 class="mb-2 text-xs font-bold">Chunks candidatos ({{ trace.chunks.length }})</h4>
          <p v-if="trace.chunks.length === 0" class="text-xs text-[color:var(--signal-warn)]">
            El retrieval no devolvió candidatos: la respuesta se generó sin evidencia de la base de conocimiento.
          </p>
          <div
            v-for="c in trace.chunks"
            :key="c.chunkId"
            class="mb-2 rounded-sm border p-2 text-xs"
            :class="c.used ? 'border-border bg-card' : 'border-dashed border-border/70 opacity-70'"
          >
            <div class="flex flex-wrap items-baseline gap-x-2 font-mono text-[10px] text-muted-foreground">
              <span class="font-bold text-foreground">#{{ c.rank }}</span>
              <span>score {{ fmtScore(c.score) }}</span>
              <span>{{ c.fileName }} · pág. {{ c.page }} · chunk {{ c.chunkIndex }}</span>
              <span>~{{ c.tokens }} tok (est.)</span>
              <span :class="c.used ? 'text-[color:var(--signal-ok)]' : ''">
                {{ c.used ? 'en el prompt' : 'excluido' }}
              </span>
            </div>
            <div class="mt-1 break-words text-muted-foreground">{{ c.snippet }}…</div>
          </div>
        </div>

        <!-- Respuesta -->
        <div>
          <h4 class="mb-2 text-xs font-bold">Respuesta</h4>
          <div v-if="trace.response" class="whitespace-pre-wrap break-words rounded-md border border-border bg-card p-3 text-xs">
            {{ trace.response }}
          </div>
          <p v-else class="text-xs text-muted-foreground">Sin respuesta guardada.</p>
        </div>

        <!-- Feedback -->
        <div v-if="trace.feedback.length > 0">
          <h4 class="mb-2 text-xs font-bold">Feedback del usuario</h4>
          <div v-for="(f, i) in trace.feedback" :key="i" class="mb-1 flex items-center gap-2 text-xs">
            <ThumbsUp v-if="f.rating > 0" class="h-3.5 w-3.5 text-[color:var(--signal-ok)]" />
            <ThumbsDown v-else-if="f.rating < 0" class="h-3.5 w-3.5 text-[color:var(--signal-fault)]" />
            <span class="font-mono text-[10px] text-muted-foreground">{{ fmtDateTime(f.createdAt) }}</span>
            <span v-if="f.comment" class="text-muted-foreground">{{ f.comment }}</span>
          </div>
        </div>

        <!-- Evaluación: no instrumentada -->
        <div class="rounded-md border border-dashed border-border p-3 text-xs text-muted-foreground">
          Evaluación automática (groundedness, corrección de citas): sin instrumentar en esta aplicación.
        </div>
      </div>
    </OpsDrawer>
  </div>
</template>
