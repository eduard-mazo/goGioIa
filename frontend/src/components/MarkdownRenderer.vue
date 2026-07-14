<script setup lang="ts">
import { computed } from 'vue'
import { renderMarkdown } from '@/lib/markdown'

const props = defineProps<{ content: string }>()

const html = computed(() => renderMarkdown(props.content))

// Delegated click handler for the "Copy" buttons injected into code blocks.
function onClick(e: MouseEvent) {
  const btn = (e.target as HTMLElement).closest('.code-block__copy') as HTMLElement | null
  if (!btn) return
  const code = btn.closest('.code-block')?.querySelector('code')?.textContent ?? ''
  navigator.clipboard.writeText(code).then(() => {
    const previous = btn.textContent
    btn.textContent = 'Copied!'
    btn.classList.add('is-copied')
    setTimeout(() => {
      btn.textContent = previous
      btn.classList.remove('is-copied')
    }, 1200)
  })
}
</script>

<template>
  <!-- html is produced by markdown-it with html:false, so raw HTML in the
       model output is escaped — this v-html is safe. -->
  <div class="markdown-body" @click="onClick" v-html="html" />
</template>
