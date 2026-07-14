<script setup lang="ts">
import { computed } from 'vue'
import { AlertTriangle, FileSignature } from 'lucide-vue-next'
import { t } from '@/i18n'
import type { ContractReport } from '@/types'

const props = defineProps<{ data: ContractReport }>()

/** Display a scalar, or the "no consta" placeholder for null/empty. */
function val(v?: string | null): string {
  return v && v.trim() ? v : t.contract.none
}

const partes = computed(() => props.data.partes?.filter((p) => p?.nombre) ?? [])
const obligaciones = computed(() => props.data.obligaciones?.filter(Boolean) ?? [])
const riesgos = computed(() => props.data.riesgos?.filter((r) => r?.clausula || r?.motivo) ?? [])
const fechas = computed(() => props.data.fechas ?? {})

// Rows rendered as a simple definition list.
const rows = computed(() => [
  { label: t.contract.subject, value: val(props.data.objeto) },
  { label: t.contract.payments, value: val(props.data.pagos) },
  { label: t.contract.termination, value: val(props.data.terminacion) },
  { label: t.contract.liability, value: val(props.data.responsabilidad) },
  { label: t.contract.confidentiality, value: val(props.data.confidencialidad) },
  { label: t.contract.law, value: val(props.data.ley_aplicable) },
  { label: t.contract.jurisdiction, value: val(props.data.jurisdiccion) },
])

function severityClass(sev?: string | null): string {
  switch ((sev ?? '').toLowerCase()) {
    case 'alta':
      return 'bg-[color:color-mix(in_srgb,var(--signal-fault)_16%,transparent)] text-[color:var(--signal-fault)]'
    case 'media':
      return 'bg-[color:color-mix(in_srgb,var(--signal-warn)_18%,transparent)] text-[color:var(--signal-warn)]'
    case 'baja':
      return 'bg-[color:color-mix(in_srgb,var(--signal-ok)_16%,transparent)] text-[color:var(--signal-ok)]'
    default:
      return 'bg-muted text-muted-foreground'
  }
}

function sectionLabel(text: string) {
  return text
}
</script>

<template>
  <div class="overflow-hidden rounded-sm border border-border bg-card/40">
    <!-- Cabecera -->
    <div class="flex items-center gap-2 border-b border-border bg-muted/40 px-3 py-2">
      <FileSignature class="h-4 w-4 text-primary" />
      <span class="text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        {{ t.contract.toggle }}
      </span>
    </div>

    <div class="space-y-4 p-3">
      <!-- Resumen -->
      <div v-if="data.resumen">
        <p class="text-sm leading-relaxed">{{ data.resumen }}</p>
      </div>

      <!-- Partes -->
      <section v-if="partes.length">
        <h4 class="mb-1.5 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          {{ sectionLabel(t.contract.parties) }}
        </h4>
        <ul class="space-y-1">
          <li v-for="(p, i) in partes" :key="i" class="flex items-baseline gap-2 text-sm">
            <span class="font-semibold">{{ p.nombre }}</span>
            <span v-if="p.rol" class="text-xs text-muted-foreground">· {{ p.rol }}</span>
          </li>
        </ul>
      </section>

      <!-- Fechas -->
      <section v-if="fechas.inicio || fechas.fin || fechas.renovacion" class="flex flex-wrap gap-4">
        <div v-for="f in [
          { k: t.contract.start, v: fechas.inicio },
          { k: t.contract.end, v: fechas.fin },
          { k: t.contract.renewal, v: fechas.renovacion },
        ]" :key="f.k">
          <div class="text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">{{ f.k }}</div>
          <div class="font-mono text-sm">{{ val(f.v) }}</div>
        </div>
      </section>

      <!-- Campos clave -->
      <dl class="grid grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-2">
        <div v-for="r in rows" :key="r.label" class="min-w-0">
          <dt class="text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">{{ r.label }}</dt>
          <dd class="text-sm">{{ r.value }}</dd>
        </div>
      </dl>

      <!-- Obligaciones -->
      <section v-if="obligaciones.length">
        <h4 class="mb-1.5 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          {{ t.contract.obligations }}
        </h4>
        <ul class="list-disc space-y-1 pl-5 text-sm">
          <li v-for="(o, i) in obligaciones" :key="i">{{ o }}</li>
        </ul>
      </section>

      <!-- Riesgos -->
      <section v-if="riesgos.length">
        <h4 class="mb-1.5 flex items-center gap-1.5 text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          <AlertTriangle class="h-3.5 w-3.5" /> {{ t.contract.risks }}
        </h4>
        <div class="overflow-hidden rounded-sm border border-border">
          <table class="w-full text-sm">
            <thead>
              <tr class="bg-muted/50 text-left text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                <th class="px-2.5 py-1.5 font-semibold">{{ t.contract.clause }}</th>
                <th class="px-2.5 py-1.5 font-semibold">{{ t.contract.severity }}</th>
                <th class="px-2.5 py-1.5 font-semibold">{{ t.contract.reason }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(r, i) in riesgos" :key="i" class="border-t border-border align-top">
                <td class="px-2.5 py-1.5">{{ val(r.clausula) }}</td>
                <td class="px-2.5 py-1.5">
                  <span
                    class="inline-block rounded-sm px-1.5 py-0.5 text-[10px] font-bold uppercase tracking-wide"
                    :class="severityClass(r.severidad)"
                  >
                    {{ r.severidad || '—' }}
                  </span>
                </td>
                <td class="px-2.5 py-1.5 text-muted-foreground">{{ val(r.motivo) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <p class="border-t border-border pt-2 text-[11px] italic text-muted-foreground">
        {{ t.contract.disclaimer }}
      </p>
    </div>
  </div>
</template>
