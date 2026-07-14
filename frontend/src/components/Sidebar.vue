<script setup lang="ts">
import {
  Cpu,
  FileText,
  MessageSquare,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
  Sun,
  Trash2,
  X,
} from 'lucide-vue-next'
import { t } from '@/i18n'
import type { AttachedDoc, Conversation, HealthStatus } from '@/types'

defineProps<{
  conversations: Conversation[]
  activeId: string
  docs: AttachedDoc[]
  model: string
  health: HealthStatus | null
  dark: boolean
  collapsed: boolean
  mobileOpen: boolean
}>()

const emit = defineEmits<{
  newChat: []
  selectChat: [id: string]
  deleteChat: [id: string]
  removeDoc: [id: string]
  toggleTheme: []
  toggleCollapse: []
  closeMobile: []
}>()

function formatChars(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}
</script>

<template>
  <aside
    class="app-sidebar fixed inset-y-0 left-0 z-50 flex h-dvh w-[280px] flex-col overflow-hidden border-r border-sidebar-border bg-sidebar text-sidebar-foreground transition-[width,transform] duration-300 ease-out md:static md:z-auto md:h-full"
    :class="[
      collapsed ? 'is-collapsed md:w-[72px]' : 'md:w-[248px]',
      mobileOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0',
    ]"
    aria-label="Menú lateral"
  >
    <!-- Marca -->
    <div class="sidebar-row flex h-16 shrink-0 items-center gap-3 border-b border-sidebar-border px-4">
      <div
        class="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-[color:var(--epm-citrico)] text-base font-black text-[color:var(--epm-bosque)]"
      >
        g
      </div>
      <div class="sidebar-label min-w-0 flex-1 leading-none">
        <div class="truncate text-[18px] font-extrabold tracking-tight text-white">{{ t.app.name }}</div>
        <div class="mt-1 text-[10px] font-medium uppercase tracking-[0.2em] text-[color:var(--epm-citrico)]">
          {{ t.app.tagline }}
        </div>
      </div>
      <button
        class="grid h-8 w-8 shrink-0 place-items-center rounded-sm text-white/70 hover:bg-sidebar-accent md:hidden"
        :aria-label="t.sidebar.closeMenu"
        @click="emit('closeMobile')"
      >
        <X class="h-4 w-4" />
      </button>
    </div>

    <div class="p-3">
      <button
        class="sidebar-row flex w-full items-center gap-2 rounded-sm bg-sidebar-accent px-3 py-2.5 text-sm font-semibold text-sidebar-accent-foreground transition hover:brightness-110"
        :title="t.sidebar.newChat"
        @click="emit('newChat')"
      >
        <Plus class="h-4 w-4 shrink-0" />
        <span class="sidebar-label">{{ t.sidebar.newChat }}</span>
      </button>
    </div>

    <!-- Historial de conversaciones -->
    <div class="sidebar-wide-only flex min-h-0 flex-1 flex-col overflow-hidden px-3">
      <div class="mb-2 px-1 text-[10px] font-bold uppercase tracking-[0.2em] text-[color:var(--epm-citrico)]">
        {{ t.sidebar.history }}
      </div>
      <p v-if="conversations.length === 0" class="px-1 text-xs leading-relaxed text-white/55">
        {{ t.sidebar.noHistory }}
      </p>
      <ul v-else class="min-h-0 flex-1 space-y-0.5 overflow-y-auto">
        <li v-for="c in conversations" :key="c.id">
          <div
            class="group flex items-center gap-2 rounded-sm px-2.5 py-2 transition"
            :class="
              c.id === activeId
                ? 'bg-sidebar-accent text-white'
                : 'text-white/70 hover:bg-white/[0.06] hover:text-white'
            "
          >
            <button
              class="flex min-w-0 flex-1 items-center gap-2 text-left"
              :aria-current="c.id === activeId ? 'true' : undefined"
              @click="emit('selectChat', c.id)"
            >
              <MessageSquare
                class="h-3.5 w-3.5 shrink-0"
                :class="c.id === activeId ? 'text-[color:var(--epm-citrico)]' : 'text-white/40'"
              />
              <span class="min-w-0 flex-1 truncate text-xs font-medium">
                {{ c.title || t.sidebar.untitled }}
              </span>
            </button>
            <button
              class="shrink-0 text-white/40 opacity-0 transition hover:text-[color:var(--signal-fault)] focus-visible:opacity-100 group-hover:opacity-100"
              :class="{ 'opacity-100': c.id === activeId }"
              :title="t.sidebar.deleteChat"
              :aria-label="t.sidebar.deleteChat"
              @click="emit('deleteChat', c.id)"
            >
              <Trash2 class="h-3.5 w-3.5" />
            </button>
          </div>
        </li>
      </ul>
    </div>

    <!-- Documentos adjuntos a la conversación actual -->
    <div v-if="docs.length > 0" class="sidebar-wide-only shrink-0 border-t border-sidebar-border px-3 pt-3">
      <div class="mb-2 px-1 text-[10px] font-bold uppercase tracking-[0.2em] text-[color:var(--epm-citrico)]">
        {{ t.sidebar.docs }}
      </div>
      <ul class="max-h-36 space-y-1.5 overflow-y-auto">
        <li
          v-for="doc in docs"
          :key="doc.id"
          class="group flex items-center gap-2 rounded-sm border border-sidebar-border bg-black/10 px-2 py-2"
        >
          <FileText class="h-4 w-4 shrink-0 text-[color:var(--epm-citrico)]" />
          <div class="min-w-0 flex-1">
            <div class="truncate text-xs font-semibold text-white" :title="doc.filename">{{ doc.filename }}</div>
            <div class="font-mono text-[10px] text-white/50">{{ formatChars(doc.chars) }} {{ t.sidebar.chars }}</div>
          </div>
          <button
            class="text-white/50 opacity-0 transition hover:text-[color:var(--signal-fault)] group-hover:opacity-100"
            :title="t.sidebar.remove"
            :aria-label="t.sidebar.remove"
            @click="emit('removeDoc', doc.id)"
          >
            <X class="h-4 w-4" />
          </button>
        </li>
      </ul>
    </div>

    <!-- Empuja el pie hacia abajo cuando el panel está colapsado (escritorio). -->
    <div v-if="collapsed" class="hidden flex-1 md:block" />

    <!-- Pie: modelo, estado, tema, colapsar -->
    <div class="shrink-0 space-y-2 border-t border-sidebar-border p-3">
      <div class="sidebar-wide-only flex items-center gap-2 text-xs">
        <Cpu class="h-4 w-4 shrink-0 text-white/60" />
        <span class="text-white/60">{{ t.sidebar.model }}</span>
        <span class="ml-auto truncate font-mono text-white/90" :title="model">{{ model || '—' }}</span>
      </div>
      <div class="sidebar-wide-only flex items-center gap-2 text-xs">
        <span :class="health?.ollama === 'online' ? 'text-[color:var(--signal-ok)]' : 'text-[color:var(--signal-fault)]'">
          <span :class="health?.ollama === 'online' ? 'status-dot' : 'status-dot-static'" />
        </span>
        <span class="text-white/60">{{ t.sidebar.ollama }}</span>
        <span
          class="ml-auto font-semibold"
          :class="health?.ollama === 'online' ? 'text-[color:var(--signal-ok)]' : 'text-[color:var(--signal-fault)]'"
        >
          {{ health ? (health.ollama === 'online' ? t.status.online : t.status.offline) : t.status.checking }}
        </span>
      </div>
      <button
        class="sidebar-row flex w-full items-center gap-3 rounded-sm px-3 py-2 text-sm text-white/85 transition hover:bg-sidebar-accent"
        :title="dark ? t.sidebar.light : t.sidebar.dark"
        @click="emit('toggleTheme')"
      >
        <component :is="dark ? Sun : Moon" class="h-4 w-4 shrink-0" />
        <span class="sidebar-label">{{ dark ? t.sidebar.light : t.sidebar.dark }}</span>
      </button>
      <!-- Colapsar: solo escritorio (en móvil se usa el cajón). -->
      <button
        class="sidebar-row hidden w-full items-center gap-3 rounded-sm px-3 py-2 text-sm text-white/85 transition hover:bg-sidebar-accent md:flex"
        :title="t.sidebar.collapse"
        @click="emit('toggleCollapse')"
      >
        <component :is="collapsed ? PanelLeftOpen : PanelLeftClose" class="h-4 w-4 shrink-0" />
        <span class="sidebar-label">{{ t.sidebar.collapse }}</span>
      </button>
    </div>
  </aside>
</template>
