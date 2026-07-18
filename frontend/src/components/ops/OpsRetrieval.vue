<script setup lang="ts">
// Vista «Calidad del retrieval»: métricas reales (similitudes, sin
// resultados, feedback) + explorador de consultas con retrieval débil.
// Las métricas de evaluación con dataset dorado (Recall@K, MRR, NDCG,
// groundedness…) NO están instrumentadas y se declaran como tales.
import { ref, watch } from 'vue'
import MetricCard from './MetricCard.vue'
import QueryTable from './QueryTable.vue'
import { fetchOpsOverview, type OpsOverview } from '@/lib/opsApi'
import { fmtNum, fmtPct, fmtScore } from '@/lib/format'
import { t } from '@/i18n'

const props = defineProps<{ hours: number }>()

const overview = ref<OpsOverview | null>(null)
const error = ref<string | null>(null)
const threshold = ref(0.5)
const mode = ref<'weak' | 'noResults' | 'down'>('weak')

async function load() {
  error.value = null
  try {
    overview.value = await fetchOpsOverview(props.hours, threshold.value)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
watch([() => props.hours, threshold], load, { immediate: true })
defineExpose({ load })
</script>

<template>
  <div class="space-y-5">
    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>

    <section class="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-6">
      <MetricCard
        label="Consultas con retrieval"
        :value="fmtNum((overview?.queries.total ?? 0) - (overview?.queries.noResults ?? 0))"
        hint="Consultas del rango que recuperaron al menos un candidato"
        :empty="!overview"
      />
      <MetricCard
        label="Sin resultados"
        :value="fmtNum(overview?.queries.noResults ?? 0)"
        hint="Consultas que no recuperaron ningún chunk: la respuesta se genera sin evidencia"
        goodDirection="down"
        :empty="!overview"
      />
      <MetricCard
        :label="`Score máx. < ${fmtScore(threshold)}`"
        :value="fmtNum(overview?.queries.weakRetrieval ?? 0)"
        hint="Consultas cuyo mejor candidato queda bajo el umbral (umbral exploratorio, ajustable abajo; no es configuración del sistema)"
        goodDirection="down"
        :empty="!overview"
      />
      <MetricCard
        label="👍 útiles"
        :value="fmtNum(overview?.queries.feedbackUp ?? 0)"
        hint="Valoraciones positivas registradas en el rango"
        :empty="!overview"
      />
      <MetricCard
        label="👎 incorrectas"
        :value="fmtNum(overview?.queries.feedbackDown ?? 0)"
        hint="Valoraciones negativas registradas en el rango"
        goodDirection="down"
        :empty="!overview"
      />
      <MetricCard
        label="Tasa de respuesta"
        :value="overview && overview.queries.total > 0 ? fmtPct(overview.queries.answered / overview.queries.total) : '—'"
        hint="Consultas respondidas / consultas totales del rango"
        :empty="!overview || overview.queries.total === 0"
      />
    </section>

    <!-- Métricas de evaluación: sin instrumentar -->
    <section class="rounded-md border border-dashed border-border p-3">
      <h4 class="text-xs font-bold text-muted-foreground">
        Recall@K · Precision@K · Hit Rate@K · MRR · NDCG · Groundedness · Corrección de citas
      </h4>
      <p class="mt-1 text-xs text-muted-foreground">{{ t.ops.notInstrumented }}</p>
    </section>

    <!-- Explorador -->
    <section class="space-y-3">
      <div class="flex flex-wrap items-center gap-3">
        <h3 class="text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          Explorador de consultas problemáticas
        </h3>
        <select v-model="mode" class="h-8 rounded-sm border border-border bg-card px-2 text-xs">
          <option value="weak">Retrieval débil (score bajo umbral)</option>
          <option value="noResults">Sin resultados</option>
          <option value="down">Con feedback 👎</option>
        </select>
        <label v-if="mode === 'weak'" class="inline-flex items-center gap-2 text-xs text-muted-foreground">
          Umbral
          <input v-model.number="threshold" type="range" min="0.2" max="0.9" step="0.05" class="accent-[color:var(--epm-bosque)]" />
          <span class="font-mono">{{ fmtScore(threshold) }}</span>
        </label>
      </div>
      <QueryTable
        :hours="hours"
        :weak="mode === 'weak' ? threshold : undefined"
        :no-results="mode === 'noResults'"
        :feedback="mode === 'down' ? 'down' : undefined"
      />
    </section>
  </div>
</template>
