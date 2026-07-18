// Formateadores compartidos del dashboard de operaciones.

/** Número compacto: 1 284 → «1,3 k», 2 500 000 → «2,5 M». */
export function fmtNum(n: number): string {
  if (!Number.isFinite(n)) return '—'
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toLocaleString('es', { maximumFractionDigits: 1 })} M`
  if (Math.abs(n) >= 10_000) return `${(n / 1_000).toLocaleString('es', { maximumFractionDigits: 1 })} k`
  return n.toLocaleString('es')
}

/** Milisegundos legibles: 950 → «950 ms», 12 400 → «12,4 s». */
export function fmtMs(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  if (ms >= 60_000) return `${(ms / 60_000).toLocaleString('es', { maximumFractionDigits: 1 })} min`
  if (ms >= 1_000) return `${(ms / 1_000).toLocaleString('es', { maximumFractionDigits: 1 })} s`
  return `${Math.round(ms)} ms`
}

/** Duración en segundos: 95 → «1 min 35 s». */
export function fmtDuration(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return '—'
  if (sec < 60) return `${Math.round(sec)} s`
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  if (m < 60) return s > 0 ? `${m} min ${s} s` : `${m} min`
  return `${Math.floor(m / 60)} h ${m % 60} min`
}

/** Bytes legibles. */
export function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '—'
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(0)} KB`
  return `${bytes} B`
}

/** Fecha-hora local corta. */
export function fmtDateTime(iso: string | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  return d.toLocaleString('es', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
}

/** Hora local corta (ejes de charts). */
export function fmtTime(iso: string, longRange: boolean): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  if (longRange) return d.toLocaleString('es', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
  return d.toLocaleTimeString('es', { hour: '2-digit', minute: '2-digit' })
}

/** Porcentaje con 1 decimal. */
export function fmtPct(ratio: number): string {
  if (!Number.isFinite(ratio)) return '—'
  return `${(ratio * 100).toLocaleString('es', { maximumFractionDigits: 1 })} %`
}

/** Score de similitud (0–1) con 3 decimales. */
export function fmtScore(s: number): string {
  if (!Number.isFinite(s) || s === 0) return '—'
  return s.toFixed(3)
}
