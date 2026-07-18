<script setup lang="ts">
// Vista «Resumen»: tarjetas de estado global, semáforo de salud por
// componente y charts del rango seleccionado.
import { computed, ref, watch } from 'vue'
import StatusPill from '../StatusPill.vue'
import MetricCard from './MetricCard.vue'
import TokenTransit from './TokenTransit.vue'
import LineChart from './LineChart.vue'
import HBarChart from './HBarChart.vue'
import {
  fetchOpsHealth,
  fetchOpsOverview,
  fetchOpsTimeseries,
  type OpsHealth,
  type OpsOverview,
  type OpsTimeseries,
  type HealthState,
} from '@/lib/opsApi'
import { fmtDateTime, fmtMs, fmtNum, fmtPct } from '@/lib/format'
import { t } from '@/i18n'

const props = defineProps<{ hours: number }>()
const emit = defineEmits<{ navigate: [view: string] }>()

const overview = ref<OpsOverview | null>(null)
const health = ref<OpsHealth | null>(null)
const series = ref<OpsTimeseries | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const [ov, ts] = await Promise.all([fetchOpsOverview(props.hours), fetchOpsTimeseries(props.hours)])
    overview.value = ov
    series.value = ts
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
  // La salud tiene su propia caché en el servidor; fallo no bloquea la vista.
  try {
    health.value = await fetchOpsHealth()
  } catch {
    health.value = null
  }
}

watch(() => props.hours, load, { immediate: true })
defineExpose({ load })

const chartMain = 'var(--chart-main)'
const chartFail = 'var(--chart-fail)'
const longRange = computed(() => props.hours > 24)

function pillState(s: HealthState | undefined): 'ok' | 'warn' | 'fault' | 'idle' {
  switch (s) {
    case 'healthy':
      return 'ok'
    case 'degraded':
      return 'warn'
    case 'unavailable':
      return 'fault'
    default:
      return 'idle'
  }
}

const healthRows = computed(() => {
  const h = health.value
  if (!h) return []
  return [
    { key: t.ops.health.app, state: h.app, detail: '' },
    { key: t.ops.health.oracle, state: h.oracle, detail: h.oracleDetail ?? '' },
    { key: t.ops.health.vectorIndex, state: h.vectorIndex, detail: h.vectorDetail ?? '' },
    { key: t.ops.health.ollama, state: h.ollama, detail: h.ollamaDetail ?? '' },
    { key: t.ops.health.embedModel, state: h.embedModel, detail: h.embedDetail ?? '' },
    { key: t.ops.health.genModel, state: h.genModel, detail: h.genDetail ?? '' },
    { key: t.ops.health.ingestion, state: h.ingestion, detail: h.stuckDocs > 0 ? `${h.stuckDocs} documento(s) atascados >30 min` : '' },
  ]
})

// Deltas honestos: solo si el período anterior tiene datos.
const dQueries = computed(() => {
  const o = overview.value
  if (!o || !o.prev.hasData) return undefined
  return o.queries.total - o.prev.queries
})
const dErrors = computed(() => {
  const o = overview.value
  if (!o || !o.prev.hasData) return undefined
  return o.embeds.failures + o.generation.failures - o.prev.failures
})
</script>

<template>
  <div class="space-y-5">
    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>

    <!-- Salud -->
    <section>
      <div class="mb-2 flex items-baseline justify-between">
        <h3 class="text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          {{ t.ops.health.title }}
        </h3>
        <span v-if="health" class="font-mono text-[10px] text-muted-foreground">
          {{ fmtDateTime(health.checkedAt) }} · TTL {{ health.ttlSeconds }}s
          <template v-if="health.cached"> · {{ t.ops.health.cached }}</template>
        </span>
      </div>
      <div class="flex flex-wrap gap-2">
        <div v-for="row in healthRows" :key="row.key" :title="row.detail || row.key">
          <StatusPill
            :label="row.key"
            :state="pillState(row.state)"
            :value="t.ops.health.states[row.state] ?? row.state"
            :pulse="false"
            always-label
          />
        </div>
        <StatusPill
          v-if="health"
          :label="t.ops.health.queue"
          :state="health.queueDepth > 0 ? 'wait' : 'ok'"
          :value="String(health.queueDepth)"
          :pulse="false"
          always-label
        />
        <div v-if="!health" class="text-xs text-muted-foreground">Salud no disponible.</div>
      </div>
    </section>

    <!-- Tarjetas de estado global -->
    <section class="grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-7">
      <MetricCard
        label="Documentos"
        :value="fmtNum(overview?.docs.total ?? 0)"
        hint="Documentos totales en la base de conocimiento (estado actual, no depende del rango)"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'ingestion')"
      />
      <MetricCard
        label="Indexados"
        :value="fmtNum(overview?.docs.embedded ?? 0)"
        hint="Documentos con ingesta completada (estado EMBEDDED)"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'ingestion')"
      />
      <MetricCard
        label="Procesando"
        :value="fmtNum(overview?.docs.processing ?? 0)"
        hint="Documentos en cola o en proceso (UPLOADED / EXTRACTING / CHUNKED)"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'ingestion')"
      />
      <MetricCard
        label="Fallidos"
        :value="fmtNum(overview?.docs.failed ?? 0)"
        hint="Documentos cuya ingesta terminó en error; se reanudan al resubir el mismo PDF"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'ingestion')"
      />
      <MetricCard
        label="Chunks"
        :value="fmtNum(overview?.chunks.total ?? 0)"
        hint="Fragmentos de texto persistidos en document_chunks"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'integrity')"
      />
      <MetricCard
        label="Vectores"
        :value="fmtNum(overview?.chunks.embedded ?? 0)"
        hint="Chunks con embedding persistido (VECTOR 768)"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'integrity')"
      />
      <MetricCard
        label="Chunks sin vector"
        :value="fmtNum(overview?.chunks.missing ?? 0)"
        hint="Chunks sin embedding: deberían ser 0; revisa Integridad si no lo son"
        :empty="!overview"
        clickable
        @open="emit('navigate', 'integrity')"
      />
    </section>

    <!-- Tarjetas del rango -->
    <section>
      <h3 class="mb-2 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        Últimas {{ hours }} h
      </h3>
      <div class="grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-7">
        <MetricCard
          label="Consultas RAG"
          :value="fmtNum(overview?.queries.total ?? 0)"
          hint="Preguntas al asistente RAG en el rango"
          :delta="dQueries"
          :empty="!overview"
          clickable
          @open="emit('navigate', 'queries')"
        />
        <MetricCard
          label="Respondidas"
          :value="fmtNum(overview?.queries.answered ?? 0)"
          hint="Consultas con respuesta generada y guardada"
          :empty="!overview"
          clickable
          @open="emit('navigate', 'queries')"
        />
        <MetricCard
          label="Evidencia débil"
          :value="fmtNum((overview?.queries.noResults ?? 0) + (overview?.queries.weakRetrieval ?? 0))"
          hint="Consultas sin resultados o con score máximo < 0,5 (umbral exploratorio)"
          goodDirection="down"
          :empty="!overview"
          clickable
          @open="emit('navigate', 'retrieval')"
        />
        <TokenTransit
          class="col-span-2"
          :up="overview?.tokens.up ?? 0"
          :down="overview?.tokens.down ?? 0"
          :up-all="overview?.tokens.upAll ?? 0"
          :down-all="overview?.tokens.downAll ?? 0"
          :hours="hours"
          :empty="!overview"
          @open="emit('navigate', 'tokens')"
        />
        <MetricCard
          label="Tasa de error"
          :value="overview ? fmtPct(overview.errorRate) : '—'"
          hint="Errores / llamadas (embeddings, retrieval y generación) en el rango"
          :delta="dErrors"
          goodDirection="down"
          :empty="!overview"
          clickable
          @open="emit('navigate', 'models')"
        />
        <MetricCard
          label="Embeddings p50 / p95"
          :value="overview ? `${fmtMs(overview.embeds.p50Ms)} / ${fmtMs(overview.embeds.p95Ms)}` : '—'"
          hint="Latencia de las llamadas /api/embed (mediana y percentil 95) en el rango"
          :empty="!overview || overview.embeds.requests === 0"
          clickable
          @open="emit('navigate', 'models')"
      />
        <MetricCard
          label="Generación p50 / p95"
          :value="overview ? `${fmtMs(overview.generation.p50Ms)} / ${fmtMs(overview.generation.p95Ms)}` : '—'"
          hint="Latencia total de generación de respuestas (mediana y percentil 95) en el rango"
          :empty="!overview || overview.generation.requests === 0"
          clickable
          @open="emit('navigate', 'models')"
        />
      </div>
    </section>

    <!-- Charts -->
    <section class="grid gap-4 lg:grid-cols-2">
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Consultas RAG</h4>
        <LineChart
          label="Consultas RAG por intervalo"
          :series="[{ name: 'Consultas', color: chartMain, points: series?.queries ?? [] }]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Embeddings: correctos vs fallidos</h4>
        <LineChart
          label="Llamadas de embeddings correctas y fallidas por intervalo"
          :series="[
            { name: 'Correctos', color: chartMain, points: series?.embedOk ?? [] },
            { name: 'Fallidos', color: chartFail, points: series?.embedFail ?? [] },
          ]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">
          Tokens de embeddings
          <span class="ml-1 rounded-sm bg-muted px-1 font-mono text-[9px] uppercase text-muted-foreground" :title="t.ops.tokenSource.ollama">{{ t.ops.tokenSource.badgeOllama }}</span>
        </h4>
        <LineChart
          label="Tokens de embeddings por intervalo"
          :series="[{ name: 'Tokens', color: chartMain, points: series?.embedTokens ?? [] }]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">
          Tokens de generación
          <span class="ml-1 rounded-sm bg-muted px-1 font-mono text-[9px] uppercase text-muted-foreground" :title="t.ops.tokenSource.ollama">{{ t.ops.tokenSource.badgeOllama }}</span>
        </h4>
        <LineChart
          label="Tokens de generación por intervalo"
          :series="[{ name: 'Tokens', color: chartMain, points: series?.genTokens ?? [] }]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Latencia media de embeddings (ms)</h4>
        <LineChart
          label="Latencia media de embeddings por intervalo"
          :series="[{ name: 'Latencia', color: chartMain, points: series?.embedAvgMs ?? [] }]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
          :format="fmtMs"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Latencia media de generación (ms)</h4>
        <LineChart
          label="Latencia media de generación por intervalo"
          :series="[{ name: 'Latencia', color: chartMain, points: series?.genAvgMs ?? [] }]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
          :format="fmtMs"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Documentos ingeridos</h4>
        <LineChart
          label="Documentos completados por intervalo"
          :series="[{ name: 'Completados', color: chartMain, points: series?.docsCompleted ?? [] }]"
          :loading="loading"
          :error="error"
          :long-range="longRange"
        />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Errores por categoría</h4>
        <HBarChart
          label="Errores por categoría en el rango"
          :items="(series?.errorsByKind ?? []).map((e) => ({ label: e.kind, value: e.count, color: 'var(--chart-fail)' }))"
          :loading="loading"
          :error="error"
        />
      </div>
    </section>
  </div>
</template>
