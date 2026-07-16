<script setup lang="ts">
import { computed } from 'vue'
import { Bot, Database, FileText, ThumbsDown, ThumbsUp, User, Zap } from 'lucide-vue-next'
import MarkdownRenderer from './MarkdownRenderer.vue'
import { t } from '@/i18n'
import type { ChatMessage } from '@/types'

const props = defineProps<{ message: ChatMessage }>()

const emit = defineEmits<{
  feedback: [messageId: string, rating: number]
}>()

const isUser = props.message.role === 'user'

const showRagMeta = computed(
  () => !isUser && props.message.rag && !props.message.streaming && !props.message.error,
)
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

      <!-- Consultando la base de conocimiento (modo RAG, sin fuentes aún) -->
      <div
        v-if="!isUser && message.rag && message.streaming && !message.content && !message.sources"
        class="flex items-center gap-2 text-sm text-muted-foreground"
      >
        <span class="status-dot text-primary" />
        {{ t.rag.searching }}
      </div>

      <!-- Escribiendo (respuesta normal) -->
      <div
        v-else-if="!isUser && message.streaming && !message.content"
        class="typing"
        :aria-label="t.chat.typing"
      >
        <span /><span /><span />
      </div>

      <!-- Texto / Markdown -->
      <template v-else>
        <MarkdownRenderer :content="message.content" :class="{ 'text-destructive': message.error }" />
        <span v-if="message.streaming && message.content" class="stream-caret" />
      </template>

      <!-- Fuentes del RAG (chunks recuperados de Oracle 23ai) -->
      <div v-if="showRagMeta && message.sources?.length" class="mt-3">
        <div class="mb-1.5 flex items-center gap-1.5 text-[10px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          <Database class="h-3 w-3" />
          {{ t.rag.sources }}
          <span
            v-if="message.cached"
            class="inline-flex items-center gap-1 rounded-sm border border-border px-1.5 py-0.5 text-[9px] normal-case tracking-normal text-[color:var(--epm-citrico)]"
            :title="t.rag.cachedHint"
          >
            <Zap class="h-2.5 w-2.5" /> {{ t.rag.cached }}
          </span>
        </div>
        <ul class="flex flex-wrap gap-1.5">
          <li
            v-for="(src, i) in message.sources"
            :key="src.chunkId"
            class="inline-flex max-w-full items-center gap-1.5 rounded-sm border border-border bg-card px-2 py-1 text-xs"
            :title="src.snippet"
          >
            <FileText class="h-3 w-3 shrink-0 text-[color:var(--epm-citrico)]" />
            <span class="truncate font-medium">[{{ i + 1 }}] {{ src.fileName }}</span>
            <span class="shrink-0 font-mono text-[10px] text-muted-foreground">
              <template v-if="src.page > 0">{{ t.rag.page }} {{ src.page }} · </template>{{ (src.score * 100).toFixed(0) }}%
            </span>
          </li>
        </ul>
      </div>

      <!-- Feedback sobre la respuesta (se guarda en rag_feedback) -->
      <div v-if="showRagMeta && message.queryId" class="mt-2 flex items-center gap-1">
        <button
          class="grid h-7 w-7 place-items-center rounded-sm border border-border transition hover:bg-muted"
          :class="message.feedback === 1 ? 'text-[color:var(--signal-ok)] border-[color:var(--signal-ok)]' : 'text-muted-foreground'"
          :title="t.rag.feedbackUp"
          :aria-label="t.rag.feedbackUp"
          @click="emit('feedback', message.id, 1)"
        >
          <ThumbsUp class="h-3.5 w-3.5" />
        </button>
        <button
          class="grid h-7 w-7 place-items-center rounded-sm border border-border transition hover:bg-muted"
          :class="message.feedback === -1 ? 'text-[color:var(--signal-fault)] border-[color:var(--signal-fault)]' : 'text-muted-foreground'"
          :title="t.rag.feedbackDown"
          :aria-label="t.rag.feedbackDown"
          @click="emit('feedback', message.id, -1)"
        >
          <ThumbsDown class="h-3.5 w-3.5" />
        </button>
        <span v-if="message.feedback" class="ml-1 text-[10px] text-muted-foreground">
          {{ t.rag.feedbackThanks }}
        </span>
      </div>
    </div>
  </div>
</template>
