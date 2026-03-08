<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { api, type Entry, type Stats } from '../api'

const text = ref('')
const submitting = ref(false)
const recentEntries = ref<Entry[]>([])
const stats = ref<Stats | null>(null)

async function capture() {
  const body = text.value.trim()
  if (!body || submitting.value) return
  submitting.value = true
  try {
    const entry = await api.createEntry({
      title: body.substring(0, 60),
      body,
      source: 'web',
    })
    text.value = ''
    // Auto-classify in background
    api.classify(entry.id).catch(() => {})
    await load()
  } finally {
    submitting.value = false
  }
}

async function load() {
  const [entries, s] = await Promise.all([
    api.listEntries({ limit: 10 }),
    api.stats(),
  ])
  recentEntries.value = entries
  stats.value = s
}

function handleKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
    capture()
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-8">
    <!-- Stats bar -->
    <div v-if="stats" class="flex gap-3 flex-wrap">
      <div
        v-for="(count, cat) in stats.categories"
        :key="cat"
        class="bg-gray-900 border border-gray-800 rounded-lg px-3 py-2 text-center"
      >
        <div class="text-lg font-bold text-sky-400">{{ count }}</div>
        <div class="text-xs text-gray-500 uppercase tracking-wider">{{ cat }}</div>
      </div>
    </div>

    <!-- Capture -->
    <div class="space-y-2">
      <textarea
        v-model="text"
        @keydown="handleKeydown"
        rows="3"
        class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-3 text-gray-200 placeholder-gray-600 focus:outline-none focus:border-sky-500 resize-y"
        placeholder="Capture a thought... (Ctrl+Enter to save)"
        autofocus
      ></textarea>
      <div class="flex justify-end">
        <button
          @click="capture"
          :disabled="!text.trim() || submitting"
          class="bg-sky-500 text-gray-950 font-semibold px-4 py-2 rounded-lg hover:bg-sky-400 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          {{ submitting ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Recent entries -->
    <div>
      <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-3">Recent</h2>
      <div v-if="recentEntries.length === 0" class="text-center py-8 text-gray-600">
        No thoughts yet. Capture one above.
      </div>
      <div v-else class="space-y-2">
        <RouterLink
          v-for="entry in recentEntries"
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
            {{ new Date(entry.created_at).toLocaleDateString() }}
            · {{ entry.source }}
            · {{ Math.round(entry.confidence * 100) }}%
          </div>
        </RouterLink>
      </div>
    </div>
  </div>
</template>
