<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { api, type BrainStatus, type RoutableEntry, type RunningEntry, type ReviewEntry, type Stats, type Project, type ActivityEvent } from '../api'

const status = ref<BrainStatus | null>(null)
const stats = ref<Stats | null>(null)
const projects = ref<Project[]>([])
const yourTurnEntries = ref<{ id: string; title: string; category: string; agent_route: string; body: string; updated_at: string }[]>([])
const sessions = ref<string[]>([])
const running = ref<RunningEntry[]>([])
const routable = ref<RoutableEntry[]>([])
const reviewEntries = ref<ReviewEntry[]>([])
const activityEvents = ref<ActivityEvent[]>([])
const loading = ref(true)
const error = ref('')
const shuttingDown = ref(false)
const showShutdownConfirm = ref(false)
const actionInProgress = ref<string | null>(null)

let pollTimer: ReturnType<typeof setInterval> | null = null

async function loadAll() {
  try {
    const [st, ss, run, rout, ses, rev, proj, yt, act] = await Promise.all([
      api.brainStatus(),
      api.stats(),
      api.agentRunning(),
      api.agentRoutable(),
      api.agentSessions(),
      api.reviewQueue(),
      api.listProjects(),
      api.yourTurn(),
      api.activity(15),
    ])
    status.value = st
    stats.value = ss
    running.value = run.entries
    routable.value = rout.entries
    sessions.value = ses.sessions
    reviewEntries.value = rev.entries
    projects.value = proj
    yourTurnEntries.value = yt.entries
    activityEvents.value = act
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

      <!-- Section: Projects -->
      <div v-if="projects.length > 0">
        <div class="flex items-center justify-between mb-3">
          <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider">Projects</h2>
          <RouterLink to="/projects" class="text-xs text-sky-400 hover:text-sky-300 transition-colors">View all &rarr;</RouterLink>
        </div>
        <div class="grid grid-cols-2 gap-2">
          <RouterLink
            v-for="project in projects.filter(p => p.status === 'active').slice(0, 6)"
            :key="project.id"
            :to="`/projects/${project.id}`"
            class="bg-gray-900 border border-gray-800 rounded-lg px-3 py-2.5 hover:border-gray-700 transition-colors block"
          >
            <div class="flex items-center gap-1.5 mb-0.5">
              <span v-if="project.emoji" class="text-sm">{{ project.emoji }}</span>
              <span class="font-medium text-sm text-gray-200 truncate">{{ project.name }}</span>
            </div>
            <div class="text-xs text-gray-500">{{ project.entry_count || 0 }} entries</div>
          </RouterLink>
        </div>
      </div>

      <!-- Section: Your Turn -->
      <div v-if="yourTurnEntries.length > 0">
        <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-3">
          Your Turn
          <span class="text-amber-400 ml-1">({{ yourTurnEntries.length }})</span>
        </h2>
        <div class="space-y-2">
          <RouterLink
            v-for="entry in yourTurnEntries"
            :key="entry.id"
            :to="`/entries/${entry.id}`"
            class="block bg-gray-900 border border-amber-900/50 rounded-lg px-4 py-3 hover:border-amber-700 transition-colors"
          >
            <div class="flex items-center justify-between mb-1">
              <span class="font-medium text-sm text-gray-200 truncate mr-4">{{ entry.title }}</span>
              <div class="flex items-center gap-1.5 shrink-0">
                <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
                <span class="text-xs px-2 py-0.5 rounded-full bg-amber-900 text-amber-300">Your Turn</span>
              </div>
            </div>
            <div v-if="entry.body" class="text-sm text-gray-500 line-clamp-2">{{ entry.body }}</div>
          </RouterLink>
        </div>
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

      <!-- Section 4: Review Queue (completed agent work) -->
      <div>
        <div class="flex items-center justify-between mb-3">
          <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider">
            Review Queue
            <span v-if="reviewEntries.length" class="text-amber-400 ml-1">({{ reviewEntries.length }})</span>
          </h2>
          <RouterLink
            v-if="reviewEntries.length > 0"
            to="/review"
            class="text-xs text-sky-400 hover:text-sky-300 transition-colors"
          >View all →</RouterLink>
        </div>
        <div v-if="reviewEntries.length === 0" class="text-center py-6 text-gray-600 bg-gray-900/50 border border-gray-800 rounded-lg">
          No completed work awaiting review
        </div>
        <div v-else class="space-y-2">
          <div
            v-for="entry in reviewEntries.slice(0, 3)"
            :key="entry.id"
            class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3"
          >
            <div class="flex items-center justify-between mb-1">
              <RouterLink
                :to="`/entries/${entry.id}`"
                class="font-medium text-sm text-gray-200 hover:text-sky-400 transition-colors truncate mr-4"
              >{{ entry.title }}</RouterLink>
              <div class="flex items-center gap-1.5 shrink-0">
                <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
                <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-purple-400">{{ entry.agent_route }}</span>
              </div>
            </div>
            <div class="text-sm text-gray-500 truncate">{{ entry.agent_output?.slice(0, 120) || 'No output' }}</div>
          </div>
          <RouterLink
            v-if="reviewEntries.length > 3"
            to="/review"
            class="block text-center py-2 text-xs text-sky-400 hover:text-sky-300 transition-colors"
          >+ {{ reviewEntries.length - 3 }} more</RouterLink>
        </div>
      </div>

      <!-- Section: Activity Feed -->
      <div v-if="activityEvents.length > 0">
        <h2 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-3">Recent Activity</h2>
        <div class="bg-gray-900 border border-gray-800 rounded-lg divide-y divide-gray-800">
          <div
            v-for="event in activityEvents"
            :key="event.id + event.type"
            class="px-4 py-2.5 flex items-center gap-3"
          >
            <span class="text-xs shrink-0"
              :class="{
                'text-emerald-400': event.type === 'entry_created',
                'text-blue-400': event.type === 'entry_routed',
                'text-purple-400': event.type === 'agent_completed',
                'text-amber-400': event.type === 'your_turn',
                'text-gray-500': event.type === 'entry_updated',
              }">
              {{ event.type === 'entry_created' ? '＋' :
                 event.type === 'entry_routed' ? '→' :
                 event.type === 'agent_completed' ? '✓' :
                 event.type === 'your_turn' ? '↩' : '·' }}
            </span>
            <RouterLink
              v-if="event.entry_id"
              :to="`/entries/${event.entry_id}`"
              class="text-sm text-gray-300 hover:text-sky-400 transition-colors truncate"
            >{{ event.title }}</RouterLink>
            <span v-else class="text-sm text-gray-300 truncate">{{ event.title }}</span>
            <span class="text-xs text-gray-600 shrink-0 ml-auto">
              {{ new Date(event.timestamp).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) }}
            </span>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
