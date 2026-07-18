<script setup lang="ts">
// Barras horizontales (categorías nominales: errores por tipo, tokens por
// modelo/documento, histogramas). Barras finas con extremo redondeado en el
// lado del dato, valor al final en tokens de texto, tooltip nativo.
import { computed } from 'vue'
import { fmtNum } from '@/lib/format'

const props = withDefaults(
  defineProps<{
    items: { label: string; value: number; color?: string; hint?: string }[]
    loading?: boolean
    error?: string | null
    format?: (v: number) => string
    /** Etiqueta accesible del gráfico. */
    label: string
    maxRows?: number
  }>(),
  { loading: false, error: null, maxRows: 12 },
)

const rows = computed(() => props.items.slice(0, props.maxRows))
const maxV = computed(() => Math.max(1, ...rows.value.map((r) => r.value)))
const fmt = (v: number) => (props.format ? props.format(v) : fmtNum(v))
const isEmpty = computed(() => rows.value.length === 0 || rows.value.every((r) => r.value === 0))
</script>

<template>
  <div role="img" :aria-label="label">
    <div v-if="loading" class="h-24 animate-pulse rounded-md bg-muted/50" />
    <div
      v-else-if="error"
      class="flex h-24 items-center justify-center rounded-md border border-[color:color-mix(in_srgb,var(--signal-fault)_35%,transparent)] px-3 text-center text-xs text-[color:var(--signal-fault)]"
    >
      {{ error }}
    </div>
    <div
      v-else-if="isEmpty"
      class="flex h-24 items-center justify-center rounded-md border border-dashed border-border text-xs text-muted-foreground"
    >
      Sin datos en el rango
    </div>
    <div v-else class="space-y-1.5">
      <div
        v-for="r in rows"
        :key="r.label"
        class="flex items-center gap-2"
        :title="r.hint || `${r.label}: ${fmt(r.value)}`"
      >
        <div class="w-[38%] min-w-0 truncate text-xs text-muted-foreground" :title="r.label">
          {{ r.label }}
        </div>
        <div class="relative h-3.5 flex-1">
          <div
            class="absolute inset-y-0 left-0 rounded-r-[4px]"
            :style="{
              width: `${Math.max(1.5, (r.value / maxV) * 100)}%`,
              background: r.color || 'var(--chart-main)',
            }"
          />
        </div>
        <div class="w-14 shrink-0 text-right font-mono text-xs" style="font-variant-numeric: tabular-nums">
          {{ fmt(r.value) }}
        </div>
      </div>
    </div>
  </div>
</template>
