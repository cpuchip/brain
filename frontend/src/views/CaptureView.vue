<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { api, type Entry, type Stats } from '../api'
import { useStatusFilter } from '../composables/useStatusFilter'

const text = ref('')
const submitting = ref(false)
const asNotebook = ref(false)
const recentEntries = ref<Entry[]>([])
const stats = ref<Stats | null>(null)

// Hide someday/archived from Recent. Roll off done after 7 days.
const { showParked, setShowParked, visibleEntries, hiddenCount, hiddenBreakdown } = useStatusFilter(
  recentEntries,
  { doneRollOffDays: 7, storageKey: 'capture-show-parked' },
)

async function capture() {
  const body = text.value.trim()
  if (!body || submitting.value) return
  submitting.value = true
  try {
    const entry = await api.createEntry({
      title: body.substring(0, 60),
      body,
      source: 'web',
      notebook: asNotebook.value || undefined,
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
  // Pull more than 10 so the filter has room — Recent still shows up to 10.
  const [entries, s] = await Promise.all([
    api.listEntries({ limit: 30 }),
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
      <div class="flex items-center justify-between">
        <label class="inline-flex items-center gap-1.5 text-xs cursor-pointer select-none text-gray-500">
          <input type="checkbox" v-model="asNotebook" class="accent-amber-500 w-3.5 h-3.5">
          <span :class="asNotebook ? 'text-amber-400' : ''">📓 Save as notebook</span>
        </label>
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
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider">Recent</h2>
        <button
          v-if="hiddenCount > 0 || showParked"
          @click="setShowParked(!showParked)"
          class="text-xs text-gray-500 hover:text-gray-300 transition-colors"
          :title="`${hiddenBreakdown.parked} parked (someday/archived) + ${hiddenBreakdown.staleDone} done >7d`"
        >
          <span v-if="!showParked">{{ hiddenCount }} hidden · show all</span>
          <span v-else>showing all · hide parked</span>
        </button>
      </div>
      <div v-if="visibleEntries.length === 0" class="text-center py-8 text-gray-600">
        No thoughts yet. Capture one above.
      </div>
      <div v-else class="space-y-2">
        <RouterLink
          v-for="entry in visibleEntries.slice(0, 10)"
          :key="entry.id"
          :to="`/entries/${entry.id}`"
          class="block bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 hover:border-sky-600 transition-colors"
          :class="{ 'opacity-60': entry.status === 'someday' || entry.status === 'archived' }"
        >
          <div class="flex items-center justify-between mb-1">
            <span class="font-medium text-sm">{{ entry.title }}</span>
            <div class="flex items-center gap-1.5">
              <span v-if="entry.status" class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-amber-400">{{ entry.status }}</span>
              <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
            </div>
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
