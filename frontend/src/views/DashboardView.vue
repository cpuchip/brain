<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { api, type BrainStatus, type RoutableEntry, type RunningEntry, type Stats } from '../api'

const status = ref<BrainStatus | null>(null)
const stats = ref<Stats | null>(null)
const sessions = ref<string[]>([])
const running = ref<RunningEntry[]>([])
const routable = ref<RoutableEntry[]>([])
const loading = ref(true)
const error = ref('')
const shuttingDown = ref(false)
const showShutdownConfirm = ref(false)
const actionInProgress = ref<string | null>(null)

let pollTimer: ReturnType<typeof setInterval> | null = null

async function loadAll() {
  try {
    const [st, ss, run, rout, ses] = await Promise.all([
      api.brainStatus(),
      api.stats(),
      api.agentRunning(),
      api.agentRoutable(),
      api.agentSessions(),
    ])
    status.value = st
    stats.value = ss
    running.value = run.entries
    routable.value = rout.entries
    sessions.value = ses.sessions
    error.value = ''
  } catch (e: any) {
    if (shuttingDown.value) return
    error.value = e.message || 'Failed to connect'
  } finally {
    loading.value = false
  }
}

async function routeEntry(entryId: string) {
  actionInProgress.value = entryId
  try {
    await api.agentRoute(entryId)
    await loadAll()
  } catch (e: any) {
    error.value = e.message || 'Route failed'
  } finally {
    actionInProgress.value = null
  }
}

async function dismissEntry(entryId: string) {
  actionInProgress.value = entryId
  try {
    await api.dismissRoute(entryId)
    await loadAll()
  } catch (e: any) {
    error.value = e.message || 'Dismiss failed'
  } finally {
    actionInProgress.value = null
  }
}

async function confirmShutdown() {
  showShutdownConfirm.value = false
  shuttingDown.value = true
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  try {
    await api.shutdown()
  } catch {
    // Expected — server shuts down
  }
}

function startPolling() {
  pollTimer = setInterval(() => {
    if (!document.hidden && !shuttingDown.value) {
      loadAll()
    }
  }, 15000)
}

onMounted(() => {
  loadAll()
  startPolling()
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="space-y-8">
    <!-- Shutting down overlay -->
    <div v-if="shuttingDown" class="fixed inset-0 z-50 flex items-center justify-center bg-gray-950/80">
      <div class="text-center space-y-3">
        <div class="text-2xl">🧠</div>
        <div class="text-lg text-gray-300">Brain stopped</div>
        <div class="text-sm text-gray-500">Close this tab or restart the brain server.</div>
      </div>
    </div>

    <!-- Shutdown confirmation dialog -->
    <Teleport to="body">
      <dialog
        ref="shutdownDialog"
        :open="showShutdownConfirm"
        class="fixed inset-0 z-40 flex items-center justify-center bg-transparent"
      >
        <div v-if="showShutdownConfirm" class="fixed inset-0 bg-black/50" @click="showShutdownConfirm = false" />
        <div v-if="showShutdownConfirm" class="relative z-50 bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl max-w-sm mx-auto">
          <h3 class="text-lg font-semibold text-gray-100 mb-2">Shut down the brain?</h3>
          <p class="text-sm text-gray-400 mb-4">Running agent tasks will be cancelled.</p>
          <div class="flex justify-end gap-3">
            <button
              @click="showShutdownConfirm = false"
              class="px-4 py-2 text-sm text-gray-400 hover:text-white transition-colors"
            >Cancel</button>
            <button
              @click="confirmShutdown"
              class="px-4 py-2 text-sm bg-red-600 text-white rounded-lg hover:bg-red-500 transition-colors"
            >Shut Down</button>
          </div>
        </div>
      </dialog>
    </Teleport>

    <!-- Loading state -->
    <div v-if="loading" class="text-center py-12 text-gray-500">Loading dashboard...</div>

    <template v-else>
      <!-- Section 1: System Status -->
      <div class="flex items-start justify-between gap-4">
        <div class="flex-1 space-y-3">
          <div class="flex items-center gap-3">
            <span class="text-lg">🧠</span>
            <span class="text-lg font-semibold text-gray-100">Brain</span>
            <span class="px-2 py-0.5 text-xs rounded-full bg-green-900 text-green-300">online</span>
          </div>

          <div v-if="status" class="text-sm text-gray-400 space-y-1">
            <div>Model: <span class="text-gray-300">{{ status.model || 'unknown' }}</span></div>
            <div>Entries: <span class="text-gray-300">{{ status.total_entries }}</span></div>
            <div v-if="sessions.length">
              Agent sessions: <span class="text-gray-300">{{ sessions.filter(s => s !== '_default').join(', ') || 'none' }}</span>
            </div>
          </div>

          <!-- Category badges -->
          <div v-if="stats" class="flex gap-2 flex-wrap">
            <div
              v-for="(count, cat) in stats.categories"
              :key="cat"
              class="bg-gray-900 border border-gray-800 rounded-lg px-3 py-1.5 text-center"
            >
              <span class="text-sm font-bold text-sky-400">{{ count }}</span>
              <span class="text-xs text-gray-500 ml-1 uppercase tracking-wider">{{ cat }}</span>
            </div>
          </div>
        </div>

        <!-- Kill switch -->
        <button
          @click="showShutdownConfirm = true"
          class="px-4 py-2 bg-red-900/50 border border-red-800 text-red-400 text-sm font-medium rounded-lg hover:bg-red-800 hover:text-red-200 transition-colors flex items-center gap-2"
        >
          <span>🛑</span> Shut Down
        </button>
      </div>

      <!-- Error banner -->
      <div v-if="error" class="bg-red-900/30 border border-red-800 rounded-lg px-4 py-3 text-sm text-red-300">
        {{ error }}
      </div>

      <!-- Section 2: Active Work -->
      <div>
        <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-3">Active Work</h2>
        <div v-if="running.length === 0" class="text-center py-6 text-gray-600 bg-gray-900/50 border border-gray-800 rounded-lg">
          No active agent work
        </div>
        <div v-else class="space-y-2">
          <div
            v-for="task in running"
            :key="task.entry_id"
            class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 flex items-center justify-between"
          >
            <div>
              <RouterLink
                :to="`/entries/${task.entry_id}`"
                class="text-sm font-medium text-gray-200 hover:text-sky-400 transition-colors"
              >{{ task.entry_id }}</RouterLink>
              <div class="text-xs text-gray-500 mt-0.5">Agent: {{ task.agent_name }}</div>
            </div>
            <span class="px-2 py-0.5 text-xs rounded-full bg-amber-900 text-amber-300 animate-pulse">running</span>
          </div>
        </div>
      </div>

      <!-- Section 3: Approval Queue -->
      <div>
        <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-3">
          Approval Queue
          <span v-if="routable.length" class="text-sky-400 ml-1">({{ routable.length }})</span>
        </h2>
        <div v-if="routable.length === 0" class="text-center py-6 text-gray-600 bg-gray-900/50 border border-gray-800 rounded-lg">
          No entries waiting for approval
        </div>
        <div v-else class="space-y-2">
          <div
            v-for="entry in routable"
            :key="entry.id"
            class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3"
          >
            <div class="flex items-center justify-between mb-1">
              <RouterLink
                :to="`/entries/${entry.id}`"
                class="font-medium text-sm text-gray-200 hover:text-sky-400 transition-colors truncate mr-4"
              >{{ entry.title }}</RouterLink>
              <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400 shrink-0">{{ entry.category }}</span>
            </div>
            <div class="text-xs text-gray-500 mb-2">→ {{ entry.agent_name }} agent</div>
            <div class="flex gap-2 justify-end">
              <button
                @click="dismissEntry(entry.id)"
                :disabled="actionInProgress === entry.id"
                class="px-3 py-1.5 text-xs text-gray-500 hover:text-gray-300 border border-gray-700 rounded-lg hover:bg-gray-800 transition-colors disabled:opacity-40"
              >✗ Skip</button>
              <button
                @click="routeEntry(entry.id)"
                :disabled="actionInProgress === entry.id"
                class="px-3 py-1.5 text-xs text-green-400 border border-green-800 rounded-lg hover:bg-green-900 transition-colors disabled:opacity-40"
              >✓ Route</button>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
