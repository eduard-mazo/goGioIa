<script setup lang="ts">
// Cajón lateral de detalle (documento / consulta) del dashboard de operaciones.
import { onBeforeUnmount, onMounted } from 'vue'
import { X } from 'lucide-vue-next'

defineProps<{ title: string; subtitle?: string }>()
const emit = defineEmits<{ close: [] }>()

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="fixed inset-0 z-[70]">
    <div class="absolute inset-0 bg-black/50 backdrop-blur-sm" @click="emit('close')" />
    <aside
      class="absolute inset-y-0 right-0 flex w-full max-w-2xl flex-col border-l border-border bg-background shadow-2xl"
      role="dialog"
      aria-modal="true"
      :aria-label="title"
    >
      <header class="flex h-14 shrink-0 items-center gap-3 border-b border-border px-4">
        <div class="min-w-0 flex-1">
          <h3 class="truncate text-sm font-extrabold tracking-tight">{{ title }}</h3>
          <div v-if="subtitle" class="truncate font-mono text-[10px] text-muted-foreground">{{ subtitle }}</div>
        </div>
        <button
          class="grid h-8 w-8 shrink-0 place-items-center rounded-sm border border-border transition hover:bg-muted"
          aria-label="Cerrar"
          @click="emit('close')"
        >
          <X class="h-4 w-4" />
        </button>
      </header>
      <div class="min-h-0 flex-1 overflow-y-auto p-4">
        <slot />
      </div>
    </aside>
  </div>
</template>
