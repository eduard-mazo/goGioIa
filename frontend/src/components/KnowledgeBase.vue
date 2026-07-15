<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Database, FileUp, Loader2, Trash2, TriangleAlert, X } from 'lucide-vue-next'
import Button from './ui/Button.vue'
import StatusPill from './StatusPill.vue'
import { deleteRagDocument, fetchRagHealth, listRagDocuments, uploadRagDocument } from '@/lib/api'
import { t } from '@/i18n'
import type { RagDocStatus, RagDocument, RagHealth } from '@/types'

const emit = defineEmits<{ close: [] }>()

const docs = ref<RagDocument[]>([])
const health = ref<RagHealth | null>(null)
const loading = ref(true)
const uploading = ref(false)
const error = ref<string | null>(null)
const dragOver = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

let pollTimer: number | undefined

// Estados intermedios del pipeline → seguir consultando.
const PROCESSING: RagDocStatus[] = ['UPLOADED', 'EXTRACTING', 'CHUNKED']

const isProcessing = computed(() => docs.value.some((d) => PROCESSING.includes(d.status)))

const oracleState = computed<'ok' | 'fault' | 'idle'>(() => {
  if (!health.value) return 'idle'
  return health.value.oracle === 'online' ? 'ok' : 'fault'
})

onMounted(async () => {
  await refresh()
  loading.value = false
  pollTimer = window.setInterval(async () => {
    // Sondeo ligero: solo mientras haya documentos en proceso.
    if (isProcessing.value) await refresh()
  }, 3000)
})

onBeforeUnmount(() => window.clearInterval(pollTimer))

async function refresh() {
  try {
    const [list, h] = await Promise.all([listRagDocuments(), fetchRagHealth()])
    docs.value = list
    health.value = h
    if (h.oracle === 'online') error.value = null
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function onFiles(files: FileList | null) {
  if (!files || files.length === 0) return
  error.value = null
  uploading.value = true
  try {
    for (const file of Array.from(files)) {
      await uploadRagDocument(file)
    }
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    uploading.value = false
  }
}

function onPick(e: Event) {
  const input = e.target as HTMLInputElement
  void onFiles(input.files)
  input.value = ''
}

function onDrop(e: DragEvent) {
  dragOver.value = false
  void onFiles(e.dataTransfer?.files ?? null)
}

async function onDelete(doc: RagDocument) {
  if (!window.confirm(t.rag.confirmDelete)) return
  error.value = null
  try {
    await deleteRagDocument(doc.id)
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function statusState(s: RagDocStatus): 'ok' | 'fault' | 'wait' {
  if (s === 'EMBEDDED') return 'ok'
  if (s === 'FAILED') return 'fault'
  return 'wait'
}

function formatSize(bytes: number): string {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(0)} KB`
  return `${bytes} B`
}

function formatDate(iso: string): string {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '—' : d.toLocaleString()
}
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center p-4">
    <!-- Fondo -->
    <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="emit('close')" />

    <!-- Panel -->
    <div
      class="relative flex max-h-[90dvh] w-full max-w-3xl flex-col overflow-hidden rounded-md border border-border bg-background shadow-2xl"
      role="dialog"
      aria-modal="true"
      :aria-label="t.rag.kbTitle"
    >
      <!-- Cabecera -->
      <header class="flex h-16 shrink-0 items-center gap-3 border-b border-border px-5">
        <div class="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-primary text-primary-foreground">
          <Database class="h-4 w-4" />
        </div>
        <div class="min-w-0 flex-1 leading-none">
          <h2 class="truncate text-lg font-extrabold tracking-tight">{{ t.rag.kbTitle }}</h2>
          <div class="mt-1 text-[10px] font-medium uppercase tracking-[0.2em] text-muted-foreground">
            {{ t.rag.kbSubtitle }}
          </div>
        </div>
        <StatusPill
          :label="t.rag.oracle"
          :state="oracleState"
          :value="health ? (health.oracle === 'online' ? t.status.online : t.status.offline) : t.status.checking"
        />
        <button
          class="grid h-9 w-9 shrink-0 place-items-center rounded-sm border border-border transition hover:bg-muted"
          :aria-label="t.rag.close"
          @click="emit('close')"
        >
          <X class="h-4 w-4" />
        </button>
      </header>

      <div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-5">
        <p class="text-sm text-muted-foreground">{{ t.rag.kbIntro }}</p>

        <div v-if="health" class="font-mono text-xs text-muted-foreground">
          {{ t.rag.stats(health.documents, health.chunks) }} · {{ health.embedModel }} → {{ health.ragModel }}
        </div>

        <!-- Errores -->
        <div
          v-if="error"
          class="flex items-start gap-2 rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
        >
          <TriangleAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span class="min-w-0 break-words">{{ error }}</span>
        </div>

        <!-- Zona de subida -->
        <button
          class="flex w-full flex-col items-center justify-center gap-2 rounded-md border-2 border-dashed px-4 py-8 text-sm text-muted-foreground transition"
          :class="dragOver ? 'border-[color:var(--epm-citrico)] bg-muted/50' : 'border-border hover:border-[color:var(--epm-citrico)] hover:bg-muted/30'"
          :disabled="uploading"
          @click="fileInput?.click()"
          @dragover.prevent="dragOver = true"
          @dragleave="dragOver = false"
          @drop.prevent="onDrop"
        >
          <Loader2 v-if="uploading" class="h-6 w-6 animate-spin text-[color:var(--epm-citrico)]" />
          <FileUp v-else class="h-6 w-6 text-[color:var(--epm-citrico)]" />
          <span>{{ uploading ? t.rag.uploading : t.rag.dropHere }}</span>
        </button>
        <input ref="fileInput" type="file" accept="application/pdf,.pdf" multiple class="hidden" @change="onPick" />

        <!-- Tabla de documentos -->
        <div>
          <div class="mb-2 text-[10px] font-bold uppercase tracking-[0.2em] text-muted-foreground">
            {{ t.rag.documents }}
          </div>

          <div v-if="loading" class="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <Loader2 class="h-4 w-4 animate-spin" /> {{ t.status.checking }}
          </div>
          <p v-else-if="docs.length === 0" class="py-6 text-sm text-muted-foreground">
            {{ t.rag.empty }}
          </p>

          <div v-else class="overflow-x-auto rounded-md border border-border">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b border-border bg-muted/40 text-left text-[10px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
                  <th class="px-3 py-2">{{ t.rag.columns.name }}</th>
                  <th class="px-3 py-2 text-right">{{ t.rag.columns.pages }}</th>
                  <th class="px-3 py-2 text-right">{{ t.rag.columns.chunks }}</th>
                  <th class="px-3 py-2 text-right">{{ t.rag.columns.size }}</th>
                  <th class="px-3 py-2">{{ t.rag.columns.status }}</th>
                  <th class="px-3 py-2">{{ t.rag.columns.uploaded }}</th>
                  <th class="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                <tr v-for="doc in docs" :key="doc.id" class="border-b border-border/60 last:border-0">
                  <td class="max-w-[220px] px-3 py-2">
                    <div class="truncate font-medium" :title="doc.fileName">{{ doc.fileName }}</div>
                    <div
                      v-if="doc.status === 'FAILED' && doc.error"
                      class="mt-0.5 truncate text-xs text-[color:var(--signal-fault)]"
                      :title="doc.error"
                    >
                      {{ doc.error }}
                    </div>
                  </td>
                  <td class="px-3 py-2 text-right font-mono text-xs">{{ doc.pageCount || '—' }}</td>
                  <td class="px-3 py-2 text-right font-mono text-xs">{{ doc.chunkCount || '—' }}</td>
                  <td class="px-3 py-2 text-right font-mono text-xs">{{ formatSize(doc.sizeBytes) }}</td>
                  <td class="px-3 py-2">
                    <StatusPill
                      :state="statusState(doc.status)"
                      :value="t.rag.status[doc.status] ?? doc.status"
                      :pulse="false"
                    />
                  </td>
                  <td class="whitespace-nowrap px-3 py-2 font-mono text-xs text-muted-foreground">
                    {{ formatDate(doc.uploadedAt) }}
                  </td>
                  <td class="px-3 py-2 text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      class="h-8 w-8 text-muted-foreground hover:text-[color:var(--signal-fault)]"
                      :title="t.rag.delete"
                      @click="onDelete(doc)"
                    >
                      <Trash2 />
                    </Button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
