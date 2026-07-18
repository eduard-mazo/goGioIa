<script setup lang="ts">
// Tile «Tránsito de tokens»: total del rango con el desglose subida (tokens
// enviados al modelo: embeddings de ingesta + pregunta + prompt) y bajada
// (tokens generados). Medidor de un solo carril con dos pasos de la misma
// rampa azul (ordinal validada) y hueco de superficie de 2px; la identidad
// la llevan las flechas y las etiquetas, nunca solo el color. Conteos
// exactos de Ollama (solo llamadas correctas).
import { computed } from 'vue'
import { ArrowDown, ArrowUp, Info } from 'lucide-vue-next'
import { fmtNum } from '@/lib/format'
import { t } from '@/i18n'

const props = defineProps<{
  up: number
  down: number
  upAll: number
  downAll: number
  hours: number
  empty?: boolean
}>()

defineEmits<{ open: [] }>()

const total = computed(() => props.up + props.down)

// Anchos del medidor: proporcionales, con un mínimo visible si hay valor.
const upPct = computed(() => {
  if (total.value === 0) return 0
  return Math.max(2, Math.min(98, (props.up / total.value) * 100))
})

const hint =
  'Tokens contados por Ollama en llamadas correctas del rango. Subida = enviados al modelo ' +
  '(embeddings de documentos + embedding de la pregunta + prompt de generación); ' +
  'bajada = generados por el modelo (RAG y chat).'
</script>

<template>
  <button
    class="flex min-w-0 flex-col gap-2 rounded-md border border-border bg-card p-3 text-left transition hover:border-[color:var(--epm-citrico)] hover:bg-muted/40"
    :title="hint"
    @click="$emit('open')"
  >
    <div class="flex items-center gap-1 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">
      <span class="truncate">Tránsito de tokens</span>
      <Info class="h-3 w-3 shrink-0 opacity-60" :aria-label="hint" />
      <span
        class="ml-auto rounded-sm bg-muted px-1 font-mono text-[9px] uppercase"
        :title="t.ops.tokenSource.ollama"
      >
        {{ t.ops.tokenSource.badgeOllama }}
      </span>
    </div>

    <div v-if="empty" class="text-lg text-muted-foreground">— <span class="text-xs">sin datos</span></div>
    <template v-else>
      <div class="flex items-baseline gap-2">
        <span class="text-xl font-extrabold tracking-tight">{{ fmtNum(total) }}</span>
        <span class="text-[11px] text-muted-foreground">en {{ hours }} h</span>
      </div>

      <!-- Medidor subida/bajada: un carril, dos pasos de la rampa azul,
           hueco de 2px en color de superficie entre segmentos. -->
      <div
        v-if="total > 0"
        class="flex h-2.5 w-full overflow-hidden rounded-[4px]"
        role="img"
        :aria-label="`Subida ${fmtNum(up)} tokens, bajada ${fmtNum(down)} tokens`"
      >
        <div :style="{ width: `${upPct}%`, background: 'var(--chart-main)' }" />
        <div class="h-full w-[2px] shrink-0 bg-card" />
        <div class="flex-1" :style="{ background: 'var(--chart-main-soft)' }" />
      </div>

      <div class="grid grid-cols-2 gap-2 text-xs">
        <div class="flex min-w-0 items-center gap-1.5">
          <ArrowUp class="h-3.5 w-3.5 shrink-0" :style="{ color: 'var(--chart-main)' }" aria-hidden="true" />
          <span class="truncate text-muted-foreground">Subida</span>
          <span class="ml-auto font-mono font-semibold" style="font-variant-numeric: tabular-nums">
            {{ fmtNum(up) }}
          </span>
        </div>
        <div class="flex min-w-0 items-center gap-1.5">
          <ArrowDown class="h-3.5 w-3.5 shrink-0" :style="{ color: 'var(--chart-main-soft)' }" aria-hidden="true" />
          <span class="truncate text-muted-foreground">Bajada</span>
          <span class="ml-auto font-mono font-semibold" style="font-variant-numeric: tabular-nums">
            {{ fmtNum(down) }}
          </span>
        </div>
      </div>

      <div class="truncate text-[11px] text-muted-foreground" :title="`Histórico completo: ${fmtNum(upAll)} de subida y ${fmtNum(downAll)} de bajada`">
        Histórico: {{ fmtNum(upAll + downAll) }} ({{ fmtNum(upAll) }} ↑ · {{ fmtNum(downAll) }} ↓)
      </div>
    </template>
  </button>
</template>
