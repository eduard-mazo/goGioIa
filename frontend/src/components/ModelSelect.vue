<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Check, ChevronDown } from 'lucide-vue-next'
import { t } from '@/i18n'

const props = defineProps<{
  models: string[]
  selected: string
  disabled?: boolean
}>()

const emit = defineEmits<{ 'update:selected': [value: string] }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)

function toggle() {
  if (props.disabled) return
  open.value = !open.value
}

function choose(name: string) {
  emit('update:selected', name)
  open.value = false
}

function onDocClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div ref="root" class="relative">
    <button
      type="button"
      :disabled="disabled"
      :aria-label="t.models.choose"
      :aria-expanded="open"
      class="flex items-center gap-1.5 rounded-sm border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted disabled:cursor-default disabled:opacity-70 disabled:hover:bg-card"
      @click="toggle"
    >
      <span class="max-w-[120px] truncate font-mono sm:max-w-[180px]">{{ selected || '—' }}</span>
      <ChevronDown v-if="!disabled" class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
    </button>

    <Transition name="pop">
      <div
        v-if="open"
        class="absolute right-0 z-50 mt-1.5 max-h-[60vh] min-w-[200px] overflow-y-auto rounded-sm border border-border bg-popover p-1 shadow-lg"
        role="listbox"
      >
        <p v-if="models.length === 0" class="px-2 py-1.5 text-xs text-muted-foreground">
          {{ t.models.none }}
        </p>
        <button
          v-for="m in models"
          :key="m"
          type="button"
          role="option"
          :aria-selected="m === selected"
          class="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent hover:text-accent-foreground"
          @click="choose(m)"
        >
          <Check
            class="h-3.5 w-3.5 shrink-0"
            :class="m === selected ? 'text-primary' : 'text-transparent'"
          />
          <span class="truncate font-mono">{{ m }}</span>
        </button>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.pop-enter-active,
.pop-leave-active {
  transition:
    opacity 120ms ease,
    transform 120ms ease;
}
.pop-enter-from,
.pop-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
