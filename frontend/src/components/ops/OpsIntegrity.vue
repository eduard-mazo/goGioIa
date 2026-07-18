<script setup lang="ts">
// Vista «Integridad de datos»: chequeos de consistencia del corpus, solo
// lectura. No hay reparación automática: la app no tiene semántica segura de
// reparación; el remedio documentado por chequeo es manual (resubir/borrar).
import { ref } from 'vue'
import { CircleCheck, CircleX, TriangleAlert } from 'lucide-vue-next'
import Button from '../ui/Button.vue'
import { fetchOpsIntegrity, type OpsIntegrityCheck } from '@/lib/opsApi'
import { fmtNum } from '@/lib/format'
import { t } from '@/i18n'

const checks = ref<OpsIntegrityCheck[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    checks.value = (await fetchOpsIntegrity()).checks
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
void load()
defineExpose({ load })

const meta: Record<string, { label: string; hint: string }> = {
  docs_embedded_sin_chunks: {
    label: 'Documentos completados sin chunks',
    hint: 'Marcados EMBEDDED pero sin fragmentos: resubir el PDF los regenera.',
  },
  chunks_sin_embedding: {
    label: 'Chunks sin embedding',
    hint: 'Fragmentos sin vector: no participan en el retrieval. Resubir el documento reanuda la vectorización.',
  },
  chunks_vacios: {
    label: 'Chunks vacíos',
    hint: 'Fragmentos con texto de longitud cero: ruido en el índice.',
  },
  chunks_sobre_limite: {
    label: 'Chunks sobre el límite de contexto',
    hint: 'Tokens estimados > contexto del modelo de embeddings (2048): no deberían existir tras capChunks.',
  },
  docs_modelos_mixtos: {
    label: 'Documentos con varios modelos de embedding',
    hint: 'Chunks del mismo documento vectorizados con modelos distintos: espacios vectoriales incompatibles.',
  },
  chunks_modelo_inactivo: {
    label: 'Embeddings de un modelo distinto al activo',
    hint: 'Vectores generados con un modelo diferente a EMBED_MODEL: no son comparables con las consultas actuales; resubir para re-vectorizar.',
  },
  docs_failed_con_avance: {
    label: 'Documentos fallidos con avance parcial',
    hint: 'Ingesta FAILED con chunks ya vectorizados: al resubir el mismo PDF se reanuda sin repetir trabajo.',
  },
  docs_atascados: {
    label: 'Documentos atascados en proceso',
    hint: 'En estado intermedio más de 30 minutos: el proceso pudo morir; resubir reanuda.',
  },
  consultas_sin_respuesta: {
    label: 'Consultas sin respuesta guardada',
    hint: 'La generación se cortó o falló después del retrieval (más de 10 minutos sin respuesta).',
  },
  evidencia_de_docs_no_indexados: {
    label: 'Evidencia de documentos no indexados',
    hint: 'Respuestas antiguas citan chunks de documentos que ya no están completos.',
  },
  vectores_norma_cero: {
    label: 'Vectores con norma ~0',
    hint: 'Embeddings sospechosos (todo ceros): rompen la similitud coseno.',
  },
}
</script>

<template>
  <div class="space-y-3">
    <div class="flex items-center justify-between">
      <p class="text-xs text-muted-foreground">
        Chequeos de solo lectura sobre el corpus. No hay reparación automática: el remedio indicado en cada
        chequeo es manual y no destructivo.
      </p>
      <Button variant="outline" size="sm" class="h-8" @click="load">{{ t.ops.refresh }}</Button>
    </div>

    <div
      v-if="error"
      class="rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>
    <div v-if="loading" class="grid gap-2 md:grid-cols-2">
      <div v-for="i in 6" :key="i" class="h-20 animate-pulse rounded-md bg-muted/50" />
    </div>

    <div v-else class="grid gap-2 md:grid-cols-2">
      <div
        v-for="c in checks"
        :key="c.id"
        class="rounded-md border p-3"
        :class="
          c.error
            ? 'border-dashed border-border'
            : c.count > 0
              ? 'border-[color:color-mix(in_srgb,var(--signal-warn)_45%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-warn)_7%,transparent)]'
              : 'border-border bg-card'
        "
      >
        <div class="flex items-center gap-2">
          <TriangleAlert v-if="!c.error && c.count > 0" class="h-4 w-4 shrink-0 text-[color:var(--signal-warn)]" />
          <CircleCheck v-else-if="!c.error" class="h-4 w-4 shrink-0 text-[color:var(--signal-ok)]" />
          <CircleX v-else class="h-4 w-4 shrink-0 text-muted-foreground" />
          <div class="min-w-0 flex-1 text-sm font-bold">{{ meta[c.id]?.label ?? c.id }}</div>
          <div class="font-mono text-sm font-bold" style="font-variant-numeric: tabular-nums">
            {{ c.error ? '—' : fmtNum(c.count) }}
          </div>
        </div>
        <p class="mt-1 text-xs text-muted-foreground">{{ meta[c.id]?.hint }}</p>
        <p v-if="c.error" class="mt-1 break-words font-mono text-[10px] text-muted-foreground">
          Chequeo no disponible: {{ c.error }}
        </p>
        <ul v-else-if="c.count > 0" class="mt-2 space-y-0.5">
          <li v-for="s in c.samples" :key="s" class="truncate font-mono text-[11px] text-muted-foreground" :title="s">
            · {{ s }}
          </li>
          <li v-if="c.count > c.samples.length" class="font-mono text-[11px] text-muted-foreground">
            … y {{ fmtNum(c.count - c.samples.length) }} más
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>
