<script setup lang="ts">
// Vista «Modelos y Ollama»: estado en vivo del host (/api/tags, /api/ps) y
// métricas por modelo derivadas de rag_events. Diagnóstico directo del
// incidente típico de esta GPU (Quadro P620, 2 GiB): recargas repetidas del
// modelo, resets de conexión, HTTP 400 por tamaño y esperas de cola.
import { computed, ref, watch } from 'vue'
import MetricCard from './MetricCard.vue'
import HBarChart from './HBarChart.vue'
import StatusPill from '../StatusPill.vue'
import { fetchOpsModels, type OpsModelsResponse } from '@/lib/opsApi'
import { fmtBytes, fmtDateTime, fmtMs, fmtNum } from '@/lib/format'

const props = defineProps<{ hours: number }>()

const data = ref<OpsModelsResponse | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchOpsModels(props.hours)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
watch(() => props.hours, load, { immediate: true })
defineExpose({ load })

// El endpoint se muestra sin la ruta (host:puerto): igual criterio que la
// cabecera del chat. No hay credenciales en la URL.
const endpointHost = computed(() => (data.value?.endpoint ?? '').replace(/\/api\/.*$/, ''))

const httpItems = computed(() =>
  (data.value?.stats.httpStatuses ?? []).map((s) => ({
    label: `HTTP ${s.kind}`,
    value: s.count,
    color: 'var(--chart-fail)',
  })),
)
const errorItems = computed(() =>
  (data.value?.stats.errorKinds ?? []).map((s) => ({ label: s.kind, value: s.count, color: 'var(--chart-fail)' })),
)

const kindLabel: Record<string, string> = {
  embed: 'embeddings (lote)',
  query_embed: 'embedding de pregunta',
  generation: 'generación',
}
</script>

<template>
  <div class="space-y-5">
    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>

    <!-- Config efectiva del cliente -->
    <section class="flex flex-wrap items-center gap-2">
      <StatusPill
        label="Ollama"
        :state="data?.reachable ? 'ok' : 'fault'"
        :value="endpointHost || '—'"
        :pulse="false"
      />
      <StatusPill label="Embeddings" state="idle" :value="data?.embedModel ?? '—'" :pulse="false" />
      <StatusPill label="Generación" state="idle" :value="data?.generationModel ?? '—'" :pulse="false" />
      <span class="font-mono text-[11px] text-muted-foreground">
        VECTOR({{ data?.vectorDimension ?? 768 }}) · num_ctx embed {{ data?.embedMaxTokens }} · num_ctx gen
        {{ data?.ragNumCtx }} · concurrencia {{ data?.embedConcurrency }} · keep_alive {{ data?.embedKeepAlive }} ·
        {{ data?.maxAttempts }} intentos máx.
      </span>
    </section>

    <!-- Señales del incidente típico -->
    <section>
      <h3 class="mb-2 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        Señales de estrés (últimas {{ hours }} h)
      </h3>
      <div class="grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-6">
        <MetricCard
          label="(Re)cargas del modelo"
          :value="fmtNum(data?.stats.loadEvents ?? 0)"
          :sub="data && data.stats.loadEvents > 0 ? `media ${fmtMs(data.stats.avgLoadMs)} · máx ${fmtMs(data.stats.maxLoadMs)}` : ''"
          hint="Llamadas con load_duration ≥ 1 s: con keep_alive activo deberían ser raras; muchas recargas = el modelo está siendo desalojado de la GPU"
          goodDirection="down"
          :empty="!data"
        />
        <MetricCard
          label="Espera de cola"
          :value="data ? `${fmtMs(data.stats.queueAvgMs)} / ${fmtMs(data.stats.queueMaxMs)}` : '—'"
          hint="Espera media/máxima en el semáforo de concurrencia (EMBED_CONCURRENCY=1): esperas altas = peticiones simultáneas por encima de la concurrencia configurada"
          :empty="!data || (data.stats.queueAvgMs === 0 && data.stats.queueMaxMs === 0)"
        />
        <MetricCard
          label="Reintentos"
          :value="fmtNum((data?.stats.perModel ?? []).reduce((n, m) => n + m.retries, 0))"
          hint="Reintentos consumidos por fallos transitorios (reset/EOF/timeout/429/5xx) en el rango"
          goodDirection="down"
          :empty="!data"
        />
        <MetricCard
          label="Reintentos agotados"
          :value="fmtNum(data?.stats.retryExhausted ?? 0)"
          hint="Llamadas que fallaron tras gastar todos los intentos"
          goodDirection="down"
          :empty="!data"
        />
        <MetricCard
          label="Último embedding OK"
          :value="fmtDateTime(data?.stats.lastEmbedOk)"
          hint="Momento de la última llamada de embeddings correcta (histórico completo)"
          :empty="!data?.stats.lastEmbedOk"
        />
        <MetricCard
          label="Último embedding fallido"
          :value="fmtDateTime(data?.stats.lastEmbedFail)"
          :sub="data?.stats.lastEmbedError"
          hint="Momento y detalle del último fallo de embeddings (histórico completo)"
          :empty="!data?.stats.lastEmbedFail"
        />
      </div>
    </section>

    <!-- Actividad por modelo -->
    <section>
      <h3 class="mb-2 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        Actividad por modelo (últimas {{ hours }} h)
      </h3>
      <div class="overflow-x-auto rounded-md border border-border">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-border bg-muted/40 text-left text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
              <th class="px-3 py-2">Modelo</th>
              <th class="px-3 py-2">Operación</th>
              <th class="px-3 py-2 text-right">Llamadas</th>
              <th class="px-3 py-2 text-right">Fallos</th>
              <th class="px-3 py-2 text-right">Reintentos</th>
              <th class="px-3 py-2 text-right">Latencia media</th>
              <th class="px-3 py-2 text-right">p95</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="(data?.stats.perModel ?? []).length === 0">
              <td colspan="7" class="px-3 py-5 text-center text-xs text-muted-foreground">Sin actividad en el rango.</td>
            </tr>
            <tr v-for="m in data?.stats.perModel ?? []" :key="`${m.model}-${m.kind}`" class="border-b border-border/60 last:border-0">
              <td class="px-3 py-2 font-medium">{{ m.model }}</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">{{ kindLabel[m.kind] ?? m.kind }}</td>
              <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtNum(m.requests) }}</td>
              <td class="px-3 py-2 text-right font-mono text-xs" :class="m.failures > 0 ? 'text-[color:var(--signal-fault)]' : ''">
                {{ fmtNum(m.failures) }}
              </td>
              <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtNum(m.retries) }}</td>
              <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtMs(m.avgMs) }}</td>
              <td class="px-3 py-2 text-right font-mono text-xs">{{ fmtMs(m.p95Ms) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Errores -->
    <section class="grid gap-4 lg:grid-cols-2">
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Distribución de estados HTTP (errores)</h4>
        <HBarChart label="Errores por estado HTTP" :items="httpItems" :loading="loading" :error="error" />
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Errores por categoría</h4>
        <HBarChart label="Errores por categoría" :items="errorItems" :loading="loading" :error="error" />
      </div>
    </section>

    <!-- Modelos del host -->
    <section class="grid gap-4 lg:grid-cols-2">
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Cargados ahora (/api/ps)</h4>
        <p v-if="!data?.reachable" class="text-xs text-muted-foreground">Host no alcanzable.</p>
        <p v-else-if="(data?.running ?? []).length === 0" class="text-xs text-muted-foreground">
          Ningún modelo cargado en este momento (o el host no expone /api/ps).
        </p>
        <table v-else class="w-full text-xs">
          <thead>
            <tr class="text-left text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
              <th class="py-1">Modelo</th>
              <th class="py-1 text-right">VRAM</th>
              <th class="py-1 text-right">Expira</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in data?.running ?? []" :key="m.digest" class="border-t border-border/60">
              <td class="py-1.5 font-medium">{{ m.name }}</td>
              <td class="py-1.5 text-right font-mono">{{ fmtBytes(m.sizeVram) }}</td>
              <td class="py-1.5 text-right font-mono">{{ fmtDateTime(m.expiresAt) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="rounded-md border border-border bg-card p-3">
        <h4 class="mb-2 text-xs font-bold">Instalados (/api/tags)</h4>
        <p v-if="!data?.reachable" class="text-xs text-muted-foreground">Host no alcanzable.</p>
        <table v-else-if="(data?.installed ?? []).length > 0" class="w-full text-xs">
          <thead>
            <tr class="text-left text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
              <th class="py-1">Modelo</th>
              <th class="py-1">Digest</th>
              <th class="py-1 text-right">Tamaño</th>
              <th class="py-1 text-right">Cuantización</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in data?.installed ?? []" :key="m.digest" class="border-t border-border/60">
              <td class="py-1.5 font-medium">{{ m.name }}</td>
              <td class="py-1.5 font-mono text-[10px] text-muted-foreground">{{ m.digest.slice(0, 12) }}</td>
              <td class="py-1.5 text-right font-mono">{{ fmtBytes(m.sizeBytes) }}</td>
              <td class="py-1.5 text-right font-mono">{{ m.quantization || '—' }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="text-xs text-muted-foreground">Sin datos.</p>
      </div>
    </section>
  </div>
</template>
