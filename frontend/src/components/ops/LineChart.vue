<script setup lang="ts">
// Chart de líneas SVG propio (sin dependencias: la app se despliega offline).
// Sigue las especificaciones dataviz: líneas de 2px, lavado de área al 10 %
// solo con una serie, rejilla hairline recesiva, leyenda solo con ≥2 series,
// crosshair+tooltip al pasar el ratón, y textos siempre en tokens de texto.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { SeriesPoint } from '@/lib/opsApi'
import { fmtNum, fmtTime } from '@/lib/format'

const props = withDefaults(
  defineProps<{
    series: { name: string; color: string; points: SeriesPoint[] }[]
    height?: number
    loading?: boolean
    error?: string | null
    /** Eje temporal con fecha (rangos > 24 h). */
    longRange?: boolean
    /** Formateador del valor (tooltip y eje Y). */
    format?: (v: number) => string
    /** Etiqueta accesible del gráfico. */
    label: string
  }>(),
  { height: 170, loading: false, error: null, longRange: false },
)

const wrap = ref<HTMLElement | null>(null)
const width = ref(600)
let ro: ResizeObserver | undefined

onMounted(() => {
  ro = new ResizeObserver((entries) => {
    const w = entries[0]?.contentRect.width
    if (w) width.value = Math.max(240, Math.round(w))
  })
  if (wrap.value) ro.observe(wrap.value)
})
onBeforeUnmount(() => ro?.disconnect())

const M = { l: 44, r: 12, t: 10, b: 22 }

// Eje X unificado: unión ordenada de los timestamps de todas las series.
const axis = computed(() => {
  const set = new Set<string>()
  for (const s of props.series) for (const p of s.points) set.add(p.t)
  return [...set].sort()
})

const byTime = computed(() =>
  props.series.map((s) => {
    const m = new Map<string, number>()
    for (const p of s.points) m.set(p.t, p.v)
    return m
  }),
)

const maxV = computed(() => {
  let max = 0
  for (const s of props.series) for (const p of s.points) if (p.v > max) max = p.v
  return niceMax(max)
})

function niceMax(v: number): number {
  if (v <= 0) return 1
  const pow = 10 ** Math.floor(Math.log10(v))
  for (const m of [1, 2, 5, 10]) {
    if (v <= m * pow) return m * pow
  }
  return 10 * pow
}

const innerW = computed(() => width.value - M.l - M.r)
const innerH = computed(() => props.height - M.t - M.b)

const x = (i: number) =>
  M.l + (axis.value.length <= 1 ? innerW.value / 2 : (i / (axis.value.length - 1)) * innerW.value)
const y = (v: number) => M.t + innerH.value * (1 - v / maxV.value)

// Segmentos por serie: los huecos (buckets sin dato, p.ej. latencias
// dispersas) parten la línea en lugar de dibujarse como cero.
const paths = computed(() =>
  props.series.map((s, si) => {
    const m = byTime.value[si]
    const segs: string[] = []
    let cur: string[] = []
    axis.value.forEach((t, i) => {
      const v = m.get(t)
      if (v === undefined) {
        if (cur.length > 1) segs.push('M' + cur.join(' L'))
        cur = []
        return
      }
      cur.push(`${x(i).toFixed(1)},${y(v).toFixed(1)}`)
    })
    if (cur.length > 1) segs.push('M' + cur.join(' L'))
    // Un único punto aislado se dibuja como marcador.
    const dots = axis.value
      .map((t, i) => ({ v: m.get(t), i }))
      .filter((p): p is { v: number; i: number } => p.v !== undefined)
    return { d: segs.join(' '), color: s.color, dots }
  }),
)

// Lavado de área: solo cuando hay una única serie (el solape de dos lavados
// enturbia la lectura).
const areaPath = computed(() => {
  if (props.series.length !== 1) return ''
  const m = byTime.value[0]
  const pts = axis.value
    .map((t, i) => ({ v: m.get(t), i }))
    .filter((p): p is { v: number; i: number } => p.v !== undefined)
  if (pts.length < 2) return ''
  const line = pts.map((p) => `${x(p.i).toFixed(1)},${y(p.v).toFixed(1)}`).join(' L')
  const x0 = x(pts[0].i).toFixed(1)
  const x1 = x(pts[pts.length - 1].i).toFixed(1)
  const yb = (M.t + innerH.value).toFixed(1)
  return `M${x0},${yb} L${line.replace(/^/, '')} L${x1},${yb} Z`
})

const yTicks = computed(() => [0, maxV.value / 2, maxV.value])
const xTicks = computed(() => {
  const n = axis.value.length
  if (n === 0) return []
  const count = Math.min(4, n)
  const out: { i: number; label: string }[] = []
  for (let k = 0; k < count; k++) {
    const i = Math.round((k / Math.max(1, count - 1)) * (n - 1))
    out.push({ i, label: fmtTime(axis.value[i], props.longRange) })
  }
  return out
})

const fmt = (v: number) => (props.format ? props.format(v) : fmtNum(v))

const isEmpty = computed(() => {
  if (axis.value.length === 0) return true
  return props.series.every((s) => s.points.every((p) => p.v === 0))
})

// ── Hover: crosshair + tooltip ─────────────────────────────────────────
const hoverIdx = ref<number | null>(null)
const tipStyle = ref<Record<string, string>>({})

function onMove(e: MouseEvent) {
  const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
  const px = e.clientX - rect.left
  const n = axis.value.length
  if (n === 0) return
  const rel = Math.min(1, Math.max(0, (px - M.l) / Math.max(1, innerW.value)))
  const i = Math.round(rel * (n - 1))
  hoverIdx.value = i
  const cx = x(i)
  const flip = cx > width.value * 0.62
  tipStyle.value = flip
    ? { right: `${width.value - cx + 10}px`, top: '8px' }
    : { left: `${cx + 10}px`, top: '8px' }
}

const tipRows = computed(() => {
  const i = hoverIdx.value
  if (i === null) return []
  return props.series
    .map((s, si) => ({ name: s.name, color: s.color, v: byTime.value[si].get(axis.value[i]) }))
    .filter((r): r is { name: string; color: string; v: number } => r.v !== undefined)
})
</script>

<template>
  <div ref="wrap" class="relative w-full">
    <!-- Estados de carga / error / vacío -->
    <div
      v-if="loading"
      class="flex animate-pulse items-center justify-center rounded-md bg-muted/50 text-xs text-muted-foreground"
      :style="{ height: `${height}px` }"
    >
      Cargando…
    </div>
    <div
      v-else-if="error"
      class="flex items-center justify-center rounded-md border border-[color:color-mix(in_srgb,var(--signal-fault)_35%,transparent)] px-3 text-center text-xs text-[color:var(--signal-fault)]"
      :style="{ height: `${height}px` }"
    >
      {{ error }}
    </div>
    <div
      v-else-if="isEmpty"
      class="flex items-center justify-center rounded-md border border-dashed border-border text-xs text-muted-foreground"
      :style="{ height: `${height}px` }"
    >
      Sin actividad en el rango
    </div>

    <template v-else>
      <svg
        :width="width"
        :height="height"
        role="img"
        :aria-label="label"
        class="block"
        @mousemove="onMove"
        @mouseleave="hoverIdx = null"
      >
        <!-- Rejilla recesiva -->
        <g>
          <line
            v-for="t in yTicks"
            :key="`g${t}`"
            :x1="M.l"
            :x2="width - M.r"
            :y1="y(t)"
            :y2="y(t)"
            stroke="var(--border)"
            stroke-width="1"
          />
        </g>
        <!-- Etiquetas de ejes (tokens de texto, nunca color de serie) -->
        <g class="fill-[color:var(--muted-foreground)]" font-size="10" style="font-variant-numeric: tabular-nums">
          <text v-for="t in yTicks" :key="`yl${t}`" :x="M.l - 6" :y="y(t) + 3" text-anchor="end">
            {{ t === 0 ? '0' : fmt(t) }}
          </text>
          <text
            v-for="tk in xTicks"
            :key="`xl${tk.i}`"
            :x="x(tk.i)"
            :y="height - 6"
            text-anchor="middle"
          >
            {{ tk.label }}
          </text>
        </g>
        <!-- Lavado de área (una sola serie) -->
        <path v-if="areaPath" :d="areaPath" :fill="series[0].color" opacity="0.1" />
        <!-- Series -->
        <g v-for="(p, i) in paths" :key="`s${i}`">
          <path
            v-if="p.d"
            :d="p.d"
            fill="none"
            :stroke="p.color"
            stroke-width="2"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
          <!-- Puntos aislados (series dispersas) con anillo de superficie -->
          <template v-if="p.dots.length <= 2">
            <circle
              v-for="d in p.dots"
              :key="`d${d.i}`"
              :cx="x(d.i)"
              :cy="y(d.v)"
              r="4"
              :fill="p.color"
              stroke="var(--background)"
              stroke-width="2"
            />
          </template>
        </g>
        <!-- Crosshair -->
        <line
          v-if="hoverIdx !== null"
          :x1="x(hoverIdx)"
          :x2="x(hoverIdx)"
          :y1="M.t"
          :y2="height - M.b"
          stroke="var(--muted-foreground)"
          stroke-width="1"
          opacity="0.5"
        />
        <g v-if="hoverIdx !== null">
          <circle
            v-for="r in tipRows"
            :key="r.name"
            :cx="x(hoverIdx)"
            :cy="y(r.v)"
            r="4"
            :fill="r.color"
            stroke="var(--background)"
            stroke-width="2"
          />
        </g>
      </svg>

      <!-- Tooltip -->
      <div
        v-if="hoverIdx !== null && tipRows.length > 0"
        class="pointer-events-none absolute z-10 rounded-sm border border-border bg-popover px-2.5 py-1.5 text-xs shadow-md"
        :style="tipStyle"
      >
        <div class="mb-0.5 font-mono text-[10px] text-muted-foreground">
          {{ fmtTime(axis[hoverIdx], true) }}
        </div>
        <div v-for="r in tipRows" :key="r.name" class="flex items-center gap-1.5">
          <span class="inline-block h-2 w-2 rounded-full" :style="{ background: r.color }" />
          <span class="text-muted-foreground">{{ r.name }}:</span>
          <span class="font-mono" style="font-variant-numeric: tabular-nums">{{ fmt(r.v) }}</span>
        </div>
      </div>

      <!-- Leyenda: solo con dos o más series -->
      <div v-if="series.length >= 2" class="mt-1 flex flex-wrap gap-x-4 gap-y-1 px-1">
        <span v-for="s in series" :key="s.name" class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          <span class="inline-block h-2 w-2 rounded-full" :style="{ background: s.color }" />
          {{ s.name }}
        </span>
      </div>
    </template>
  </div>
</template>
