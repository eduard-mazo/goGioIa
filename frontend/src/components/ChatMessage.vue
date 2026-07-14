<script setup lang="ts">
import { computed } from 'vue'
import { Bot, User } from 'lucide-vue-next'
import MarkdownRenderer from './MarkdownRenderer.vue'
import ContractReport from './ContractReport.vue'
import { t } from '@/i18n'
import type { ChatMessage, ContractReport as ContractReportData } from '@/types'

const props = defineProps<{ message: ChatMessage }>()

const isUser = props.message.role === 'user'

// Parse a finished contract reply into the structured report (null while
// streaming or if the model didn't return valid JSON → falls back to markdown).
const report = computed<ContractReportData | null>(() => {
  if (!props.message.contract || props.message.streaming) return null
  try {
    const obj = JSON.parse(props.message.content)
    return obj && typeof obj === 'object' ? (obj as ContractReportData) : null
  } catch {
    return null
  }
})
</script>

<template>
  <div class="flex gap-3 px-4 py-5" :class="isUser ? '' : 'bg-muted/30'">
    <div
      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-sm"
      :class="isUser ? 'bg-secondary text-secondary-foreground' : 'bg-primary text-primary-foreground'"
    >
      <User v-if="isUser" class="h-4 w-4" />
      <Bot v-else class="h-4 w-4" />
    </div>

    <div class="min-w-0 flex-1">
      <div class="mb-1 text-[10px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
        {{ isUser ? t.chat.you : t.app.name }}
      </div>

      <!-- Analizando contrato (modo contrato, en streaming) -->
      <div
        v-if="!isUser && message.contract && message.streaming"
        class="flex items-center gap-2 text-sm text-muted-foreground"
      >
        <span class="status-dot text-primary" />
        {{ t.contract.analyzing }}
      </div>

      <!-- Escribiendo (respuesta normal) -->
      <div
        v-else-if="!isUser && message.streaming && !message.content"
        class="typing"
        :aria-label="t.chat.typing"
      >
        <span /><span /><span />
      </div>

      <!-- Informe estructurado de contrato -->
      <ContractReport v-else-if="report" :data="report" />

      <!-- Texto / Markdown (incluye fallback si el JSON no es válido) -->
      <template v-else>
        <MarkdownRenderer :content="message.content" :class="{ 'text-destructive': message.error }" />
        <span v-if="message.streaming && message.content" class="stream-caret" />
      </template>
    </div>
  </div>
</template>
