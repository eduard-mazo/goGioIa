<script setup lang="ts">
// Vista «Consultas y respuestas»: filtros + tabla de consultas con traza.
import { ref } from 'vue'

import QueryTable from './QueryTable.vue'

defineProps<{ hours: number }>()

const status = ref('')
const feedback = ref('')
const noResults = ref(false)
const model = ref('')
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap items-center gap-2">
      <select v-model="status" class="h-8 rounded-sm border border-border bg-card px-2 text-xs">
        <option value="">Todos los estados</option>
        <option value="answered">Respondidas</option>
        <option value="pending">Incompletas</option>
        <option value="error">Con error</option>
      </select>
      <select v-model="feedback" class="h-8 rounded-sm border border-border bg-card px-2 text-xs">
        <option value="">Cualquier feedback</option>
        <option value="up">👍 útiles</option>
        <option value="down">👎 incorrectas</option>
      </select>
      <label class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <input v-model="noResults" type="checkbox" class="accent-[color:var(--epm-bosque)]" /> Sin resultados
      </label>
      <input
        v-model="model"
        type="search"
        placeholder="Filtrar por modelo…"
        class="h-8 w-44 rounded-sm border border-border bg-card px-2 text-xs outline-none focus:ring-2 focus:ring-ring"
      />
    </div>
    <QueryTable :hours="hours" :status="status" :feedback="feedback" :no-results="noResults" :model="model" />
  </div>
</template>
