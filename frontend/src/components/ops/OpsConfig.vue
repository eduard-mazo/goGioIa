<script setup lang="ts">
// Vista «Configuración»: valores efectivos en solo lectura, con su
// procedencia (environment | file | flag | default | code). Los secretos nunca
// llegan del servidor (solo su procedencia). No hay edición: la app no tiene
// un mecanismo seguro de configuración en caliente.
import { ref } from 'vue'
import { KeyRound } from 'lucide-vue-next'
import { fetchOpsConfig, type OpsConfigEntry } from '@/lib/opsApi'

const entries = ref<OpsConfigEntry[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    entries.value = (await fetchOpsConfig()).entries
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
void load()
defineExpose({ load })

const sourceLabel: Record<string, string> = {
  environment: 'variable de entorno',
  file: 'archivo de configuración',
  flag: 'flag de arranque',
  default: 'valor por defecto',
  code: 'fijado en código',
}

const hints: Record<string, string> = {
  EMBED_MAX_TOKENS:
    'Contexto operativo del modelo de embeddings (n_ctx_train de nomic). num_ctx se clava a este valor en cada llamada para anular Modelfiles del host que pidan más (p.ej. 8192 sobre 2048).',
  EMBED_CONCURRENCY: 'Llamadas de embeddings simultáneas. 1 para la GPU de 2 GiB del despliegue actual.',
  EMBED_KEEP_ALIVE: 'Tiempo que Ollama mantiene el modelo de embeddings cargado entre llamadas.',
  RAG_TOP_K: 'Chunks recuperados por consulta (candidatos = seleccionados: no hay reranking).',
  RAG_CHUNK_SIZE: 'Tamaño de chunk en caracteres (~450 tokens).',
  RAG_CHUNK_OVERLAP: 'Solape entre chunks en caracteres.',
  DOC_PREFIX: 'Prefijo de tarea nomic aplicado a cada documento antes de vectorizar.',
  QUERY_PREFIX: 'Prefijo de tarea nomic aplicado a cada pregunta antes de vectorizar.',
  OPS_HEALTH_TTL: 'Segundos que se cachea la sonda de salud de este dashboard.',
}
</script>

<template>
  <div class="space-y-3">
    <p class="text-xs text-muted-foreground">
      Configuración efectiva en solo lectura. Para cambiarla: el archivo de configuración
      (gogioia.env junto al ejecutable), variables de entorno del contenedor
      (deploy/gogioia.container) o flags de arranque. Los valores secretos no se muestran.
    </p>

    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>
    <div v-if="loading" class="h-64 animate-pulse rounded-md bg-muted/50" />

    <div v-else class="overflow-x-auto rounded-md border border-border">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-border bg-muted/40 text-left text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
            <th class="px-3 py-2">Clave</th>
            <th class="px-3 py-2">Valor</th>
            <th class="px-3 py-2">Procedencia</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in entries" :key="e.key" class="border-b border-border/60 last:border-0" :title="hints[e.key]">
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs font-semibold">
              {{ e.key }}
              <KeyRound v-if="e.secret" class="ml-1 inline h-3 w-3 text-muted-foreground" aria-label="secreto" />
            </td>
            <td class="max-w-[360px] break-words px-3 py-2 font-mono text-xs">{{ e.value }}</td>
            <td class="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">
              {{ sourceLabel[e.source] ?? e.source }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
