<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Entry, type Stats } from '../api'

const route = useRoute()
const router = useRouter()
const entries = ref<Entry[]>([])
const stats = ref<Stats | null>(null)
const loading = ref(true)
const activeCategory = ref('')

const categories = ['people', 'projects', 'ideas', 'actions', 'study', 'journal', 'inbox']

async function loadEntries() {
  loading.value = true
  try {
    const params: { category?: string; needs_review?: boolean } = {}
    if (activeCategory.value === 'review') {
      params.needs_review = true
    } else if (activeCategory.value) {
      params.category = activeCategory.value
    }
    entries.value = await api.listEntries(params)
  } finally {
    loading.value = false
  }
}

function setCategory(cat: string) {
  activeCategory.value = cat
  router.replace({ query: cat ? { category: cat } : {} })
}

watch(activeCategory, loadEntries)

onMounted(async () => {
  activeCategory.value = (route.query.category as string) || ''
  stats.value = await api.stats()
  await loadEntries()
})
</script>

<template>
  <div>
    <h1 class="text-xl font-bold mb-4">Entries</h1>

    <!-- Category tabs -->
    <div class="flex gap-2 flex-wrap mb-6">
      <button
        @click="setCategory('')"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === '' ? 'bg-sky-500 text-gray-950 border-sky-500 font-semibold' : 'border-gray-700 text-gray-400 hover:border-sky-600 hover:text-gray-200'"
      >
        All
      </button>
      <button
        v-for="cat in categories"
        :key="cat"
        @click="setCategory(cat)"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === cat ? 'bg-sky-500 text-gray-950 border-sky-500 font-semibold' : 'border-gray-700 text-gray-400 hover:border-sky-600 hover:text-gray-200'"
      >
        {{ cat }}
        <span v-if="stats?.categories[cat]" class="ml-1 text-xs opacity-70">({{ stats.categories[cat] }})</span>
      </button>
      <button
        @click="setCategory('review')"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === 'review' ? 'bg-amber-500 text-gray-950 border-amber-500 font-semibold' : 'border-gray-700 text-amber-400 hover:border-amber-500'"
      >
        ⚠ Review
      </button>
    </div>

    <!-- Entry list -->
    <div v-if="loading" class="text-center py-8 text-gray-500">Loading...</div>
    <div v-else-if="entries.length === 0" class="text-center py-12 text-gray-600">
      No entries{{ activeCategory ? ` in "${activeCategory}"` : '' }}.
    </div>
    <div v-else class="space-y-2">
      <RouterLink
        v-for="entry in entries"
        :key="entry.id"
        :to="`/entries/${entry.id}`"
        class="block bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 hover:border-sky-600 transition-colors"
      >
        <div class="flex items-center justify-between mb-1">
          <span class="font-medium text-sm">{{ entry.title }}</span>
          <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
        </div>
        <div class="text-sm text-gray-500 truncate">{{ entry.body }}</div>
        <div class="flex items-center gap-2 mt-1">
          <span
            v-for="tag in (entry.tags || []).slice(0, 5)"
            :key="tag"
            class="text-xs px-1.5 py-0.5 rounded-full border border-gray-700 text-gray-500"
          >
            {{ tag }}
          </span>
          <span class="text-xs text-gray-600 ml-auto">
            {{ new Date(entry.created_at).toLocaleDateString() }}
          </span>
        </div>
      </RouterLink>
    </div>
  </div>
</template>
