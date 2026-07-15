<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { DatabaseZap, Loader2, Paperclip, Send, Square } from 'lucide-vue-next'
import Button from './ui/Button.vue'
import { UPLOAD_ACCEPT } from '@/lib/api'
import { t } from '@/i18n'

const props = defineProps<{
  streaming: boolean
  uploading: boolean
  ragMode: boolean
}>()

const emit = defineEmits<{
  send: [text: string]
  stop: []
  attach: [file: File]
  toggleRag: []
}>()

const text = ref('')
const textarea = ref<HTMLTextAreaElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

function autoGrow() {
  const el = textarea.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 200)}px`
}

function submit() {
  const value = text.value.trim()
  if (!value || props.streaming) return
  emit('send', value)
  text.value = ''
  nextTick(autoGrow)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
  }
}

function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  if (input.files && input.files[0]) emit('attach', input.files[0])
  input.value = ''
}
</script>

<template>
  <div class="border-t border-border bg-background px-4 py-3">
    <p v-if="ragMode" class="mx-auto mb-2 max-w-3xl text-xs text-muted-foreground">
      {{ t.rag.hint }}
    </p>
    <div
      class="mx-auto flex max-w-3xl items-end gap-2 rounded-md border border-border bg-card p-2 transition focus-within:border-[color:var(--epm-citrico)] focus-within:ring-1 focus-within:ring-ring"
    >
      <input
        ref="fileInput"
        type="file"
        :accept="UPLOAD_ACCEPT"
        class="hidden"
        @change="onFile"
      />
      <Button
        variant="ghost"
        size="icon"
        :disabled="uploading"
        :title="t.chat.attach"
        @click="fileInput?.click()"
      >
        <Loader2 v-if="uploading" class="animate-spin" />
        <Paperclip v-else />
      </Button>
      <Button
        :variant="ragMode ? 'default' : 'ghost'"
        size="icon"
        :title="t.rag.toggle"
        :aria-pressed="ragMode"
        @click="emit('toggleRag')"
      >
        <DatabaseZap />
      </Button>

      <textarea
        ref="textarea"
        v-model="text"
        rows="1"
        :placeholder="t.chat.placeholder"
        class="max-h-[200px] flex-1 resize-none bg-transparent py-2 text-sm text-foreground outline-none placeholder:text-muted-foreground"
        @input="autoGrow"
        @keydown="onKeydown"
      />

      <Button
        v-if="streaming"
        variant="destructive"
        size="icon"
        :title="t.chat.stop"
        @click="emit('stop')"
      >
        <Square />
      </Button>
      <Button v-else size="icon" :title="t.chat.send" :disabled="!text.trim()" @click="submit">
        <Send />
      </Button>
    </div>
  </div>
</template>
