<script setup lang="ts">
import { ref } from 'vue'
import { api, type Entry } from '../api'

const query = ref('')
const results = ref<Entry[]>([])
const searching = ref(false)
const searchMode = ref<'text' | 'semantic'>('text')

async function search() {
  const q = query.value.trim()
  if (!q || searching.value) return
  searching.value = true
  try {
    if (searchMode.value === 'semantic') {
      const semantic = await api.semanticSearch(q, 20)
      const entries: Entry[] = []
      for (const r of semantic) {
        try {
          entries.push(await api.getEntry(r.entry_id))
        } catch { /* skip missing */ }
      }
      results.value = entries
    } else {
      results.value = await api.search(q, 20)
    }
  } finally {
    searching.value = false
  }
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') search()
}
</script>

<template>
  <div>
    <h1 class="text-xl font-bold mb-4">Search</h1>

    <div class="flex gap-2 mb-6">
      <input
        v-model="query"
        @keydown="handleKeydown"
        class="flex-1 bg-gray-900 border border-gray-700 rounded-lg px-4 py-2.5 text-gray-200 placeholder-gray-600 focus:outline-none focus:border-sky-500"
        placeholder="Search thoughts..."
        autofocus
      />
      <button
        @click="search"
        :disabled="!query.trim() || searching"
        class="bg-sky-500 text-gray-950 font-semibold px-4 rounded-lg hover:bg-sky-400 disabled:opacity-40 transition-colors"
      >
        Search
      </button>
    </div>

    <!-- Mode toggle -->
    <div class="flex gap-2 mb-4">
      <button
        @click="searchMode = 'text'"
        class="text-sm px-3 py-1 rounded-lg border transition-colors"
        :class="searchMode === 'text' ? 'bg-sky-500 text-gray-950 border-sky-500' : 'border-gray-700 text-gray-400 hover:border-sky-600'"
      >
        Text
      </button>
      <button
        @click="searchMode = 'semantic'"
        class="text-sm px-3 py-1 rounded-lg border transition-colors"
        :class="searchMode === 'semantic' ? 'bg-purple-500 text-gray-950 border-purple-500' : 'border-gray-700 text-gray-400 hover:border-purple-500'"
      >
        🔮 Semantic
      </button>
    </div>

    <!-- Results -->
    <div v-if="searching" class="text-center py-8 text-gray-500">Searching...</div>
    <div v-else-if="results.length === 0 && query.trim()" class="text-center py-12 text-gray-600">
      No results found.
    </div>
    <div v-else class="space-y-2">
      <RouterLink
        v-for="entry in results"
        :key="entry.id"
        :to="`/entries/${entry.id}`"
        class="block bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 hover:border-sky-600 transition-colors"
      >
        <div class="flex items-center justify-between mb-1">
          <span class="font-medium text-sm">{{ entry.title }}</span>
          <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
        </div>
        <div class="text-sm text-gray-500 truncate">{{ entry.body }}</div>
        <div class="text-xs text-gray-600 mt-1">
          {{ new Date(entry.created_at).toLocaleDateString() }} · {{ entry.source }}
        </div>
      </RouterLink>
    </div>
  </div>
</template>
