<script setup lang="ts">
// Vista «Tokens y uso»: consolida los conteos por categoría. Cada bloque
// lleva su fuente: «exacto» (reportado por Ollama) o «estimado» (~4 chars por
// token). Nunca se suman fuentes distintas sin etiquetar. Son unidades
// operativas, no costes: no existe configuración de precios en la app.
import { computed, ref, watch } from 'vue'
import MetricCard from './MetricCard.vue'
import HBarChart from './HBarChart.vue'
import { fetchOpsTokens, type OpsTokensReport } from '@/lib/opsApi'
import { fmtNum } from '@/lib/format'
import { t } from '@/i18n'

const props = defineProps<{ hours: number }>()

const report = ref<OpsTokensReport | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    report.value = await fetchOpsTokens(props.hours)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
watch(() => props.hours, load, { immediate: true })
defineExpose({ load })

const est = computed(() => ` (${t.ops.tokenSource.badgeEstimated})`)
const exact = computed(() => ` (${t.ops.tokenSource.badgeOllama})`)

const byModelItems = computed(() =>
  (report.value?.byModel ?? []).map((m) => ({
    label: `${m.model} · ${m.kind}`,
    value: m.tokensIn + m.tokensOut,
    hint: `${m.requests} llamadas · ${fmtNum(m.tokensIn)} entrada + ${fmtNum(m.tokensOut)} salida (exacto)`,
  })),
)
const byDocItems = computed(() =>
  (report.value?.byDoc ?? []).map((d) => ({
    label: d.fileName,
    value: d.tokens,
    hint: `${d.chunks} chunks · ${fmtNum(d.tokens)} tokens estimados`,
  })),
)
</script>

<template>
  <div class="space-y-5">
    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>

    <p class="text-xs text-muted-foreground">
      «{{ t.ops.tokenSource.badgeOllama }}» = {{ t.ops.tokenSource.ollama.toLowerCase() }} ·
      «{{ t.ops.tokenSource.badgeEstimated }}» = {{ t.ops.tokenSource.estimated.toLowerCase() }}.
      Los totales de reintentos cuentan por llamada real: los tokens «logrados» excluyen los intentos fallidos.
    </p>

    <!-- Ingesta -->
    <section>
      <h3 class="mb-2 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        Tokens de ingesta
      </h3>
      <div class="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-5">
        <MetricCard
          :label="`Extraídos del corpus${est}`"
          :value="fmtNum(report?.ingest.extractedEst ?? 0)"
          hint="Σ token_count de todos los chunks persistidos (global, no depende del rango)"
          :sub="t.ops.tokenSource.badgeEstimated"
          :empty="!report"
        />
        <MetricCard
          :label="`Enviados a embeddings${est}`"
          :value="fmtNum(report?.ingest.attemptedEst ?? 0)"
          hint="Tokens intentados en llamadas de embeddings de ingesta en el rango (bytes/4, incluye reintentos y fallos)"
          :sub="t.ops.tokenSource.badgeEstimated"
          :empty="!report"
        />
        <MetricCard
          :label="`Vectorizados con éxito${exact}`"
          :value="fmtNum(report?.ingest.successful ?? 0)"
          hint="prompt_eval_count sumado de las llamadas de ingesta correctas en el rango"
          :sub="t.ops.tokenSource.badgeOllama"
          :empty="!report"
        />
        <MetricCard
          :label="`Media por chunk${est}`"
          :value="report ? `${report.ingest.avgChunk} (${report.ingest.minChunk}–${report.ingest.maxChunk})` : '—'"
          hint="Tokens estimados por chunk: media (mín–máx), corpus completo"
          :empty="!report"
        />
        <MetricCard
          label="Chunks cerca del límite"
          :value="report ? `${fmtNum(report.ingest.nearLimit)} / ${fmtNum(report.ingest.overLimit)}` : '—'"
          hint="≥90 % del contexto del modelo (2048) / por encima del límite (deberían ser 0)"
          goodDirection="down"
          :empty="!report"
        />
        <MetricCard
          label="Entradas rechazadas"
          :value="fmtNum(report?.ingest.rejectedInputs ?? 0)"
          hint="Entradas rechazadas por la validación local antes de llamar a Ollama (rango)"
          goodDirection="down"
          :empty="!report"
        />
        <MetricCard
          label="HTTP 400 por tamaño"
          :value="fmtNum(report?.ingest.inputTooLarge400 ?? 0)"
          hint="Llamadas de ingesta rechazadas por Ollama con 400 de tamaño en el rango; el chunk culpable se parte y se promedia"
          goodDirection="down"
          :empty="!report"
        />
      </div>
    </section>

    <!-- Consulta y retrieval -->
    <section>
      <h3 class="mb-2 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        Tokens de consulta y contexto (rango)
      </h3>
      <div class="grid grid-cols-2 gap-2 md:grid-cols-3">
        <MetricCard
          :label="`Embedding de preguntas${exact}`"
          :value="fmtNum(report?.query.embedTokens ?? 0)"
          hint="Tokens de vectorizar las preguntas (prefijo search_query incluido)"
          :sub="t.ops.tokenSource.badgeOllama"
          :empty="!report"
        />
        <MetricCard
          :label="`Contexto recuperado${est}`"
          :value="fmtNum(report?.query.retrievedEst ?? 0)"
          hint="Σ tokens estimados de todos los chunks candidatos recuperados"
          :sub="t.ops.tokenSource.badgeEstimated"
          :empty="!report"
        />
        <MetricCard
          :label="`Contexto usado en el prompt${est}`"
          :value="fmtNum(report?.query.selectedEst ?? 0)"
          hint="Σ tokens estimados de los chunks que entraron al prompt final"
          :sub="t.ops.tokenSource.badgeEstimated"
          :empty="!report"
        />
      </div>
    </section>

    <!-- Generación -->
    <section>
      <h3 class="mb-2 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        Tokens de generación (rango)
      </h3>
      <div class="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-5">
        <MetricCard
          :label="`Prompt${exact}`"
          :value="fmtNum(report?.generation.promptTokens ?? 0)"
          hint="prompt_eval_count sumado de las generaciones correctas"
          :sub="t.ops.tokenSource.badgeOllama"
          :empty="!report"
        />
        <MetricCard
          :label="`Respuesta${exact}`"
          :value="fmtNum(report?.generation.completionTokens ?? 0)"
          hint="eval_count sumado de las generaciones correctas"
          :sub="t.ops.tokenSource.badgeOllama"
          :empty="!report"
        />
        <MetricCard
          label="Respuestas"
          :value="fmtNum(report?.generation.answers ?? 0)"
          hint="Generaciones completadas con éxito en el rango"
          :empty="!report"
        />
        <MetricCard
          label="Media / máx por respuesta"
          :value="report ? `${fmtNum(report.generation.avgPerAnswer)} / ${fmtNum(report.generation.maxPerAnswer)}` : '—'"
          hint="Tokens de salida por respuesta (media y máximo)"
          :empty="!report || report.generation.answers === 0"
        />
        <MetricCard
          label="Cortadas por límite"
          :value="fmtNum(report?.generation.stoppedByLimit ?? 0)"
          hint="Respuestas con done_reason ≠ stop (p.ej. length): el modelo se quedó sin presupuesto de tokens"
          goodDirection="down"
          :empty="!report"
        />
      </div>
    </section>

    <!-- Desgloses -->
    <section class="grid gap-4 lg:grid-cols-2">
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">
          Tokens por modelo y operación
          <span class="ml-1 rounded-sm bg-muted px-1 font-mono text-[9px] uppercase text-muted-foreground" :title="t.ops.tokenSource.ollama">{{ t.ops.tokenSource.badgeOllama }}</span>
        </h4>
        <HBarChart label="Tokens por modelo y operación" :items="byModelItems" :loading="loading" :error="error" />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">
          Tokens por documento (top 12, corpus completo)
          <span class="ml-1 rounded-sm bg-muted px-1 font-mono text-[9px] uppercase text-muted-foreground" :title="t.ops.tokenSource.estimated">{{ t.ops.tokenSource.badgeEstimated }}</span>
        </h4>
        <HBarChart label="Tokens por documento" :items="byDocItems" :loading="loading" :error="error" />
      </div>
    </section>
  </div>
</template>
