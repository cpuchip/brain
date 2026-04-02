<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { RouterLink } from 'vue-router'
import { api, type ReviewEntry } from '../api'

const entries = ref<ReviewEntry[]>([])
const loading = ref(true)
const error = ref('')
const actionInProgress = ref<string | null>(null)
const toast = ref('')
const expandedId = ref<string | null>(null)

let pollTimer: ReturnType<typeof setInterval> | null = null

async function load() {
  try {
    const res = await api.reviewQueue()
    entries.value = res.entries
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load review queue'
  } finally {
    loading.value = false
  }
}

function showToast(msg: string) {
  toast.value = msg
  setTimeout(() => { toast.value = '' }, 2500)
}

async function accept(id: string) {
  actionInProgress.value = id
  try {
    await api.reviewAction(id, 'accept')
    entries.value = entries.value.filter(e => e.id !== id)
    if (expandedId.value === id) expandedId.value = null
    showToast('Accepted')
  } catch (e: any) {
    showToast('Failed: ' + (e.message || 'unknown'))
  } finally {
    actionInProgress.value = null
  }
}

async function reject(id: string) {
  actionInProgress.value = id
  try {
    await api.reviewAction(id, 'reject')
    entries.value = entries.value.filter(e => e.id !== id)
    if (expandedId.value === id) expandedId.value = null
    showToast('Rejected')
  } catch (e: any) {
    showToast('Failed: ' + (e.message || 'unknown'))
  } finally {
    actionInProgress.value = null
  }
}

function toggleExpand(id: string) {
  expandedId.value = expandedId.value === id ? null : id
}

function formatDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

const queueCount = computed(() => entries.value.length)

onMounted(() => {
  load()
  pollTimer = setInterval(() => {
    if (!document.hidden) load()
  }, 30000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="relative">
    <!-- Toast -->
    <Transition enter-active-class="transition-opacity duration-200" leave-active-class="transition-opacity duration-150"
      enter-from-class="opacity-0" leave-to-class="opacity-0">
      <div v-if="toast"
        class="fixed top-4 right-4 z-50 bg-sky-500 text-gray-950 font-semibold px-4 py-2 rounded-lg shadow-lg text-sm">
        {{ toast }}
      </div>
    </Transition>

    <div class="flex items-center justify-between mb-6">
      <h1 class="text-xl font-bold">
        Review Queue
        <span v-if="queueCount > 0" class="text-sky-400 text-base font-normal ml-1">({{ queueCount }})</span>
      </h1>
      <button
        @click="load"
        :disabled="loading"
        class="px-3 py-1.5 text-xs text-gray-400 hover:text-white border border-gray-700 rounded-lg hover:bg-gray-800 transition-colors disabled:opacity-40"
      >
        ↻ Refresh
      </button>
    </div>

    <!-- Error -->
    <div v-if="error" class="bg-red-900/30 border border-red-800 rounded-lg px-4 py-3 text-sm text-red-300 mb-4">
      {{ error }}
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-center py-12 text-gray-500">Loading review queue...</div>

    <!-- Empty -->
    <div v-else-if="entries.length === 0" class="text-center py-16">
      <div class="text-3xl mb-3">✅</div>
      <div class="text-gray-400">No entries awaiting review</div>
      <div class="text-sm text-gray-600 mt-1">Completed agent work will appear here</div>
    </div>

    <!-- Entries -->
    <div v-else class="space-y-3">
      <div
        v-for="entry in entries"
        :key="entry.id"
        class="bg-gray-900 border border-gray-800 rounded-lg overflow-hidden transition-colors"
        :class="{ 'border-sky-800': expandedId === entry.id }"
      >
        <!-- Header row -->
        <div
          class="px-4 py-3 cursor-pointer hover:bg-gray-800/50 transition-colors"
          @click="toggleExpand(entry.id)"
        >
          <div class="flex items-center justify-between mb-1">
            <div class="flex items-center gap-2 min-w-0 flex-1">
              <span class="text-xs text-gray-600 select-none">{{ expandedId === entry.id ? '▾' : '▸' }}</span>
              <span class="font-medium text-sm text-gray-200 truncate">{{ entry.title }}</span>
            </div>
            <div class="flex items-center gap-1.5 shrink-0 ml-3">
              <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
              <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-purple-400">{{ entry.agent_route }}</span>
            </div>
          </div>
          <div class="flex items-center gap-3 text-xs text-gray-500">
            <span>{{ formatDate(entry.updated_at) }}</span>
            <span v-if="entry.tokens_used">{{ entry.tokens_used.toLocaleString() }} tokens</span>
          </div>
        </div>

        <!-- Expanded content -->
        <template v-if="expandedId === entry.id">
          <!-- Original entry body -->
          <div v-if="entry.body" class="px-4 pb-3 border-t border-gray-800">
            <div class="text-xs font-medium text-gray-500 uppercase tracking-wider mt-3 mb-2">Original Entry</div>
            <div class="text-sm text-gray-400 whitespace-pre-wrap bg-gray-950/50 rounded-lg px-3 py-2 max-h-40 overflow-y-auto">{{ entry.body }}</div>
          </div>

          <!-- Agent output -->
          <div class="px-4 pb-3 border-t border-gray-800">
            <div class="text-xs font-medium text-gray-500 uppercase tracking-wider mt-3 mb-2">Agent Output</div>
            <div class="text-sm text-gray-300 whitespace-pre-wrap bg-gray-950/50 rounded-lg px-3 py-2 max-h-96 overflow-y-auto">{{ entry.agent_output || 'No output recorded' }}</div>
          </div>

          <!-- Action buttons -->
          <div class="px-4 py-3 border-t border-gray-800 flex items-center justify-between">
            <RouterLink
              :to="`/entries/${entry.id}`"
              class="text-xs text-gray-500 hover:text-sky-400 transition-colors"
            >View full entry →</RouterLink>
            <div class="flex gap-2">
              <button
                @click.stop="reject(entry.id)"
                :disabled="actionInProgress === entry.id"
                class="px-4 py-1.5 text-sm text-red-400 border border-red-800 rounded-lg hover:bg-red-900/50 transition-colors disabled:opacity-40"
              >✗ Reject</button>
              <button
                @click.stop="accept(entry.id)"
                :disabled="actionInProgress === entry.id"
                class="px-4 py-1.5 text-sm text-green-400 border border-green-800 rounded-lg hover:bg-green-900/50 transition-colors disabled:opacity-40"
              >✓ Accept</button>
            </div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>
