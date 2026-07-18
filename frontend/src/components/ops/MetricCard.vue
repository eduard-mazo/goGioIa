<script setup lang="ts">
// Tarjeta de métrica: etiqueta + valor + definición (tooltip) + delta real
// contra el período anterior (solo si hay datos históricos; nunca inventado).
import { computed } from 'vue'
import { Info } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    label: string
    value: string
    /** Definición de la métrica (tooltip con el icono de info). */
    hint?: string
    /** Línea secundaria (p.ej. «fuente: estimado»). */
    sub?: string
    /** Delta vs período anterior; se oculta si es undefined. */
    delta?: number
    /** Dirección buena del delta («up» = subir es bueno). */
    goodDirection?: 'up' | 'down'
    /** Sin datos disponibles. */
    empty?: boolean
    /** La tarjeta navega a una vista al pulsarla. */
    clickable?: boolean
  }>(),
  { goodDirection: 'up', empty: false, clickable: false },
)

defineEmits<{ open: [] }>()

const deltaText = computed(() => {
  if (props.delta === undefined || !Number.isFinite(props.delta)) return ''
  const sign = props.delta > 0 ? '+' : ''
  return `${sign}${props.delta.toLocaleString('es')}`
})

const deltaGood = computed(() => {
  if (props.delta === undefined || props.delta === 0) return null
  const up = props.delta > 0
  return props.goodDirection === 'up' ? up : !up
})
</script>

<template>
  <component
    :is="clickable ? 'button' : 'div'"
    class="flex min-w-0 flex-col gap-1 rounded-md border border-border bg-card p-3 text-left"
    :class="clickable ? 'transition hover:border-[color:var(--epm-citrico)] hover:bg-muted/40' : ''"
    @click="clickable && $emit('open')"
  >
    <div class="flex items-center gap-1 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">
      <span class="truncate" :title="label">{{ label }}</span>
      <Info v-if="hint" class="h-3 w-3 shrink-0 opacity-60" :aria-label="hint" />
    </div>
    <div v-if="empty" class="text-lg text-muted-foreground">— <span class="text-xs">sin datos</span></div>
    <div v-else class="flex items-baseline gap-2">
      <span class="truncate text-xl font-extrabold tracking-tight" :title="hint">{{ value }}</span>
      <span
        v-if="deltaText"
        class="inline-flex items-center rounded-sm px-1 font-mono text-[10px]"
        :class="
          deltaGood === null
            ? 'text-muted-foreground'
            : deltaGood
              ? 'bg-[color:color-mix(in_srgb,var(--signal-ok)_14%,transparent)] text-[color:var(--signal-ok)]'
              : 'bg-[color:color-mix(in_srgb,var(--signal-fault)_12%,transparent)] text-[color:var(--signal-fault)]'
        "
        :title="`Cambio vs período anterior equivalente`"
      >
        {{ deltaText }}
      </span>
    </div>
    <div v-if="sub && !empty" class="truncate text-[11px] text-muted-foreground" :title="sub">{{ sub }}</div>
  </component>
</template>
