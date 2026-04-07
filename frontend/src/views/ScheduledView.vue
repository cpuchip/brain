<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { api, type ScheduledTask, type TaskRun, type Project, type AgentInfo, type NudgeBotStatus } from '../api'

const tasks = ref<ScheduledTask[]>([])
const projects = ref<Project[]>([])
const agents = ref<AgentInfo[]>([])
const loading = ref(true)
const error = ref('')

// Nudge bot
const nudgeBot = ref<NudgeBotStatus | null>(null)
const togglingNudge = ref(false)

async function loadNudgeBot() {
  try {
    nudgeBot.value = await api.getNudgeBotStatus()
  } catch {
    // nudge bot may not be configured
  }
}

async function toggleNudgePause() {
  if (!nudgeBot.value || togglingNudge.value) return
  togglingNudge.value = true
  try {
    nudgeBot.value = await api.setNudgeBotPaused(!nudgeBot.value.paused)
  } finally {
    togglingNudge.value = false
  }
}

// Create form
const showCreate = ref(false)
const createForm = ref({
  name: '',
  description: '',
  schedule: 'daily',
  agent_name: '',
  prompt: '',
  project_id: null as number | null,
})

// Detail / runs
const selectedTask = ref<ScheduledTask | null>(null)
const taskRuns = ref<TaskRun[]>([])
const loadingRuns = ref(false)

const activeCount = computed(() => tasks.value.filter(t => t.status === 'active').length)
const pausedCount = computed(() => tasks.value.filter(t => t.status === 'paused').length)

async function loadAll() {
  try {
    const [t, p, a] = await Promise.all([
      api.listScheduledTasks(),
      api.listProjects(),
      api.libraryAgents(),
    ])
    tasks.value = t
    projects.value = p
    agents.value = a
    error.value = ''
    await loadNudgeBot()
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
  } finally {
    loading.value = false
  }
}

async function createTask() {
  if (!createForm.value.name || !createForm.value.agent_name || !createForm.value.prompt) return
  try {
    const task = await api.createScheduledTask({
      name: createForm.value.name,
      description: createForm.value.description || undefined,
      schedule: createForm.value.schedule,
      agent_name: createForm.value.agent_name,
      prompt: createForm.value.prompt,
      project_id: createForm.value.project_id,
    })
    tasks.value.push(task)
    showCreate.value = false
    createForm.value = { name: '', description: '', schedule: 'daily', agent_name: '', prompt: '', project_id: null }
  } catch (e: any) {
    error.value = e.message
  }
}

async function toggleStatus(task: ScheduledTask) {
  const newStatus = task.status === 'active' ? 'paused' : 'active'
  try {
    const updated = await api.updateScheduledTask(task.id, { status: newStatus })
    const idx = tasks.value.findIndex(t => t.id === task.id)
    if (idx >= 0) tasks.value[idx] = updated
  } catch (e: any) {
    error.value = e.message
  }
}

async function deleteTask(task: ScheduledTask) {
  try {
    await api.deleteScheduledTask(task.id)
    tasks.value = tasks.value.filter(t => t.id !== task.id)
    if (selectedTask.value?.id === task.id) selectedTask.value = null
  } catch (e: any) {
    error.value = e.message
  }
}

async function triggerRun(task: ScheduledTask) {
  try {
    await api.triggerTaskRun(task.id)
    // Reload runs if viewing this task
    if (selectedTask.value?.id === task.id) {
      await loadRuns(task)
    }
    await loadAll()
  } catch (e: any) {
    error.value = e.message
  }
}

async function loadRuns(task: ScheduledTask) {
  selectedTask.value = task
  loadingRuns.value = true
  try {
    taskRuns.value = await api.listTaskRuns(task.id)
  } catch (e: any) {
    error.value = e.message
  } finally {
    loadingRuns.value = false
  }
}

function formatTime(ts: string | null | undefined): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

function projectName(id: number | null | undefined): string {
  if (!id) return ''
  return projects.value.find(p => p.id === id)?.name || ''
}

onMounted(loadAll)
</script>

<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-bold text-gray-100">Scheduled Tasks</h1>
        <p class="text-sm text-gray-500 mt-1">
          {{ activeCount }} active<span v-if="pausedCount">, {{ pausedCount }} paused</span>
        </p>
      </div>
      <button
        @click="showCreate = !showCreate"
        class="px-3 py-1.5 text-sm bg-sky-600 hover:bg-sky-500 text-white rounded-lg transition-colors"
      >
        {{ showCreate ? 'Cancel' : '+ New Task' }}
      </button>
    </div>

    <!-- Error -->
    <div v-if="error" class="bg-red-900/50 text-red-300 px-4 py-2 rounded-lg text-sm">{{ error }}</div>

    <!-- Nudge Bot Status -->
    <div v-if="nudgeBot?.enabled" class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium text-gray-200">🔔 Nudge Bot</span>
          <span class="text-xs px-2 py-0.5 rounded-full"
            :class="nudgeBot.paused ? 'bg-gray-800 text-gray-500' : 'bg-emerald-900 text-emerald-300'">
            {{ nudgeBot.paused ? 'Paused' : 'Active' }}
          </span>
          <span v-if="!nudgeBot.paused && !nudgeBot.user_present" class="text-xs px-2 py-0.5 rounded-full bg-amber-900 text-amber-300" title="No API activity in 2+ hours — nudges will skip until you return">
            😴 Waiting for presence
          </span>
        </div>
        <button
          @click="toggleNudgePause"
          :disabled="togglingNudge"
          class="text-xs px-3 py-1 rounded-lg border transition-colors"
          :class="nudgeBot.paused ? 'border-emerald-700 text-emerald-400 hover:bg-emerald-900' : 'border-gray-700 text-gray-400 hover:bg-gray-800'"
        >{{ nudgeBot.paused ? '▶ Resume' : '⏸ Pause' }}</button>
      </div>
      <div class="flex items-center gap-4 mt-2 text-xs text-gray-500">
        <span>Wake hours: {{ nudgeBot.wake_hours.map(h => h + ':00').join(', ') }}</span>
        <span>Last run: {{ nudgeBot.last_run_at ? formatTime(nudgeBot.last_run_at) : '—' }}</span>
        <span>Next: {{ nudgeBot.next_run_at ? formatTime(nudgeBot.next_run_at) : '—' }}</span>
      </div>
      <div class="flex items-center gap-4 mt-1 text-xs text-gray-600">
        <span>Last cycle: {{ nudgeBot.last_nudge_count }} nudge{{ nudgeBot.last_nudge_count === 1 ? '' : 's' }}</span>
        <span>Total: {{ nudgeBot.total_nudges }} nudges</span>
        <span>Cost: 🎟️ {{ nudgeBot.total_cost.toFixed(2) }}</span>
      </div>
    </div>

    <!-- Create form -->
    <Transition
      enter-active-class="transition-all duration-200"
      leave-active-class="transition-all duration-150"
      enter-from-class="opacity-0 -translate-y-2"
      leave-to-class="opacity-0 -translate-y-2"
    >
      <div v-if="showCreate" class="bg-gray-900 border border-gray-800 rounded-lg p-4 space-y-3">
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="text-xs text-gray-500 block mb-1">Name</label>
            <input v-model="createForm.name" class="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-sky-500 focus:outline-none" placeholder="Weekly AI Research" />
          </div>
          <div>
            <label class="text-xs text-gray-500 block mb-1">Schedule</label>
            <select v-model="createForm.schedule" class="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-sky-500 focus:outline-none">
              <option value="hourly">Hourly</option>
              <option value="daily">Daily</option>
              <option value="daily:08:00">Daily at 8am</option>
              <option value="daily:18:00">Daily at 6pm</option>
              <option value="weekly:monday">Weekly (Monday)</option>
              <option value="weekly:friday">Weekly (Friday)</option>
              <option value="weekly:sunday">Weekly (Sunday)</option>
            </select>
          </div>
        </div>
        <div>
          <label class="text-xs text-gray-500 block mb-1">Description</label>
          <input v-model="createForm.description" class="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-sky-500 focus:outline-none" placeholder="What this task does" />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="text-xs text-gray-500 block mb-1">Agent</label>
            <select v-model="createForm.agent_name" class="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-sky-500 focus:outline-none">
              <option value="">Select agent...</option>
              <option v-for="a in agents" :key="a.name" :value="a.name">{{ a.name }}</option>
            </select>
          </div>
          <div>
            <label class="text-xs text-gray-500 block mb-1">Project (optional)</label>
            <select v-model="createForm.project_id" class="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-sky-500 focus:outline-none">
              <option :value="null">No project</option>
              <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.emoji }} {{ p.name }}</option>
            </select>
          </div>
        </div>
        <div>
          <label class="text-xs text-gray-500 block mb-1">Prompt</label>
          <textarea v-model="createForm.prompt" rows="3" class="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm text-gray-200 focus:border-sky-500 focus:outline-none resize-none" placeholder="What the agent should do each run..." />
        </div>
        <button
          @click="createTask"
          :disabled="!createForm.name || !createForm.agent_name || !createForm.prompt"
          class="px-4 py-1.5 text-sm bg-sky-600 hover:bg-sky-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-lg transition-colors"
        >
          Create Task
        </button>
      </div>
    </Transition>

    <!-- Loading -->
    <div v-if="loading" class="text-gray-500 text-sm">Loading tasks...</div>

    <!-- Task list -->
    <div v-else-if="tasks.length === 0" class="text-center py-12 text-gray-500">
      <p class="text-lg">No scheduled tasks yet</p>
      <p class="text-sm mt-1">Create one to automate recurring research or weekly digests.</p>
    </div>

    <div v-else class="space-y-2">
      <div
        v-for="task in tasks"
        :key="task.id"
        class="bg-gray-900 border rounded-lg px-4 py-3 transition-colors cursor-pointer"
        :class="[
          selectedTask?.id === task.id ? 'border-sky-700' : 'border-gray-800 hover:border-gray-700',
          task.status === 'paused' ? 'opacity-60' : ''
        ]"
        @click="loadRuns(task)"
      >
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2 min-w-0">
            <span class="font-medium text-sm text-gray-200 truncate">{{ task.name }}</span>
            <span class="text-xs px-2 py-0.5 rounded-full shrink-0"
              :class="task.status === 'active' ? 'bg-emerald-900 text-emerald-300' : 'bg-gray-800 text-gray-500'">
              {{ task.status }}
            </span>
            <span class="text-xs px-2 py-0.5 rounded-full bg-purple-900 text-purple-300 shrink-0">{{ task.agent_name }}</span>
            <span v-if="task.project_id" class="text-xs px-2 py-0.5 rounded-full bg-indigo-900 text-indigo-300 shrink-0">{{ projectName(task.project_id) }}</span>
          </div>
          <div class="flex items-center gap-2 shrink-0 ml-3">
            <span class="text-xs text-gray-500">{{ task.schedule }}</span>
            <button
              @click.stop="triggerRun(task)"
              class="text-xs px-2 py-0.5 bg-gray-800 hover:bg-gray-700 text-gray-400 rounded transition-colors"
              title="Run now"
            >▶</button>
            <button
              @click.stop="toggleStatus(task)"
              class="text-xs px-2 py-0.5 bg-gray-800 hover:bg-gray-700 text-gray-400 rounded transition-colors"
              :title="task.status === 'active' ? 'Pause' : 'Resume'"
            >{{ task.status === 'active' ? '⏸' : '▶️' }}</button>
            <button
              @click.stop="deleteTask(task)"
              class="text-xs px-2 py-0.5 bg-gray-800 hover:bg-red-900 text-gray-400 hover:text-red-300 rounded transition-colors"
              title="Delete"
            >✕</button>
          </div>
        </div>
        <div v-if="task.description" class="text-sm text-gray-500 mt-1">{{ task.description }}</div>
        <div class="flex items-center gap-4 mt-2 text-xs text-gray-600">
          <span>Last run: {{ formatTime(task.last_run_at) }}</span>
          <span>Next run: {{ formatTime(task.next_run_at) }}</span>
        </div>
      </div>
    </div>

    <!-- Run history panel -->
    <Transition
      enter-active-class="transition-all duration-200"
      leave-active-class="transition-all duration-150"
      enter-from-class="opacity-0"
      leave-to-class="opacity-0"
    >
      <div v-if="selectedTask" class="bg-gray-900 border border-gray-800 rounded-lg p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="text-sm font-medium text-gray-300">
            Run History — {{ selectedTask.name }}
          </h2>
          <button @click="selectedTask = null" class="text-gray-500 hover:text-gray-300 text-sm">Close</button>
        </div>

        <div v-if="loadingRuns" class="text-gray-500 text-sm">Loading runs...</div>
        <div v-else-if="taskRuns.length === 0" class="text-gray-600 text-sm">No runs yet</div>
        <div v-else class="space-y-1.5">
          <div
            v-for="run in taskRuns"
            :key="run.id"
            class="flex items-center gap-3 text-sm px-3 py-2 bg-gray-800/50 rounded"
          >
            <span class="text-xs px-2 py-0.5 rounded-full shrink-0"
              :class="{
                'bg-emerald-900 text-emerald-300': run.status === 'complete',
                'bg-red-900 text-red-300': run.status === 'failed',
                'bg-blue-900 text-blue-300': run.status === 'running',
              }">
              {{ run.status }}
            </span>
            <span class="text-gray-400">{{ formatTime(run.started_at) }}</span>
            <span v-if="run.entry_id" class="text-gray-500">
              → <RouterLink :to="`/entries/${run.entry_id}`" class="text-sky-400 hover:text-sky-300">{{ run.entry_id.slice(0, 8) }}</RouterLink>
            </span>
            <span v-if="run.error" class="text-red-400 truncate">{{ run.error }}</span>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>
