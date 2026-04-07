<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Project, type Entry, type SessionMessage } from '../api'
import { useWebSocket } from '../composables/useWebSocket'

const route = useRoute()
const router = useRouter()
const project = ref<Project | null>(null)
const entries = ref<Entry[]>([])
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const confirmDelete = ref(false)
const viewMode = ref<'board' | 'list'>(localStorage.getItem('project-view-mode') === 'list' ? 'list' : 'board')

// Slide-out panel
const selectedEntry = ref<Entry | null>(null)
const panelMessages = ref<SessionMessage[]>([])
const panelLoading = ref(false)

// Pipeline action state
const advancingEntry = ref<string | null>(null)
const feedbackDialog = ref(false)
const feedbackEntryId = ref('')
const feedbackAction = ref<'revise' | 'defer'>('revise')
const feedbackText = ref('')

// Execution gate state (Phase 4e)
const executeDialog = ref(false)
const executeEntryId = ref('')
const executionContext = ref<{ scenarios: string[]; model: string; cost: number; prompt: string } | null>(null)
const executeFeedback = ref('')
const executingEntry = ref<string | null>(null)
const verifyDialog = ref(false)
const verifyEntryId = ref('')
const verifyScenarios = ref<{ scenario: string; passed: boolean; notes: string }[]>([])
const verifySubmitting = ref(false)
const scaffolding = ref(false)
const scaffoldResult = ref<{ project_dir: string; git_inited: boolean; gh_created: boolean; error?: string } | null>(null)

const editForm = ref({ name: '', description: '', emoji: '', status: '', context_file: '', workspace_type: 'integrated', workspace_path: '', github_repo: '', repo_visibility: 'private' })

const maturityStages = ['raw', 'researched', 'planned', 'specced', 'executing', 'verified'] as const
const stageLabels: Record<string, string> = {
  raw: 'Raw',
  researched: 'Researched',
  planned: 'Planned',
  specced: 'Specced',
  executing: 'Executing',
  verified: 'Verified',
  unset: 'No Stage',
}

const entriesByMaturity = computed(() => {
  const grouped: Record<string, Entry[]> = {}
  for (const stage of maturityStages) {
    grouped[stage] = []
  }
  grouped['unset'] = []
  for (const e of entries.value) {
    const m = e.maturity || 'unset'
    if (grouped[m]) {
      grouped[m].push(e)
    } else {
      grouped['unset'].push(e)
    }
  }
  return grouped
})

const nonEmptyStages = computed(() => {
  return [...maturityStages, 'unset'].filter(s => (entriesByMaturity.value[s]?.length ?? 0) > 0)
})

// 3-column board: Inbox / Working / Done
const boardColumns = computed(() => {
  const inbox: Entry[] = []
  const working: Entry[] = []
  const done: Entry[] = []

  for (const e of entries.value) {
    if (e.notebook || !e.maturity || e.maturity === 'raw') {
      inbox.push(e)
    } else if (e.maturity === 'verified') {
      done.push(e)
    } else {
      working.push(e)
    }
  }

  return [
    { key: 'inbox', label: 'Inbox', entries: inbox, color: 'bg-gray-700 text-gray-300', borderColor: 'border-gray-600' },
    { key: 'working', label: 'Working', entries: working, color: 'bg-blue-900 text-blue-300', borderColor: 'border-blue-800' },
    { key: 'done', label: 'Done', entries: done, color: 'bg-green-900 text-green-300', borderColor: 'border-green-800' },
  ]
})

const totalPremiumRequests = computed(() => {
  return entries.value.reduce((sum, e) => sum + (e.premium_requests_used || 0), 0)
})

function maturityColor(stage: string) {
  switch (stage) {
    case 'raw': return 'bg-gray-700 text-gray-300'
    case 'researched': return 'bg-blue-900 text-blue-300'
    case 'planned': return 'bg-purple-900 text-purple-300'
    case 'specced': return 'bg-indigo-900 text-indigo-300'
    case 'executing': return 'bg-amber-900 text-amber-300'
    case 'verified': return 'bg-green-900 text-green-300'
    default: return 'bg-gray-800 text-gray-400'
  }
}

function routeStatusIndicator(entry: Entry) {
  switch (entry.route_status) {
    case 'your_turn':
      if (entry.agent_route === 'review') {
        return { class: 'border-l-purple-400', badge: 'bg-purple-900 text-purple-300', label: 'Review', icon: '🤖' }
      }
      return { class: 'border-l-amber-400', badge: 'bg-amber-900 text-amber-300', label: 'Your Turn', icon: '🔔' }
    case 'running': return { class: 'border-l-blue-400', badge: 'bg-blue-900 text-blue-300', label: 'Running', icon: '⚡' }
    case 'complete': return { class: 'border-l-green-400', badge: 'bg-green-900 text-green-300', label: 'Complete', icon: '✓' }
    case 'suggested': return { class: 'border-l-gray-600', badge: 'bg-gray-800 text-gray-400', label: 'Suggested', icon: '→' }
    default: return null
  }
}

function statusColor(status: string) {
  switch (status) {
    case 'active': return 'bg-green-900 text-green-300'
    case 'paused': return 'bg-yellow-900 text-yellow-300'
    case 'archived': return 'bg-gray-800 text-gray-500'
    default: return 'bg-gray-800 text-gray-400'
  }
}

// Can we advance this entry in the pipeline?
function canAdvance(entry: Entry): boolean {
  const m = entry.maturity || 'raw'
  return ['raw', 'researched', 'planned'].includes(m)
}

function canRevise(entry: Entry): boolean {
  const m = entry.maturity || 'raw'
  return ['researched', 'planned'].includes(m)
}

function canExecute(entry: Entry): boolean {
  return entry.maturity === 'specced'
}

function canVerify(entry: Entry): boolean {
  return entry.maturity === 'executing' && entry.route_status === 'your_turn'
}

watch(viewMode, (v) => localStorage.setItem('project-view-mode', v))

async function load() {
  loading.value = true
  try {
    const id = Number(route.params.id)
    const [p, e] = await Promise.all([
      api.getProject(id),
      api.projectEntries(id),
    ])
    project.value = p
    entries.value = e
  } finally {
    loading.value = false
  }
}

function startEdit() {
  if (!project.value) return
  editForm.value = {
    name: project.value.name,
    description: project.value.description || '',
    emoji: project.value.emoji || '',
    status: project.value.status,
    context_file: project.value.context_file || '',
    workspace_type: project.value.workspace_type || 'integrated',
    workspace_path: project.value.workspace_path || '',
    github_repo: project.value.github_repo || '',
    repo_visibility: project.value.repo_visibility || 'private',
  }
  editing.value = true
}

async function saveEdit() {
  if (!project.value) return
  saving.value = true
  try {
    await api.updateProject(project.value.id, {
      name: editForm.value.name,
      description: editForm.value.description || undefined,
      emoji: editForm.value.emoji || undefined,
      status: editForm.value.status,
      context_file: editForm.value.context_file || undefined,
      workspace_type: editForm.value.workspace_type,
      workspace_path: editForm.value.workspace_path || undefined,
      github_repo: editForm.value.github_repo || undefined,
      repo_visibility: editForm.value.repo_visibility,
    })
    editing.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function doDelete() {
  if (!project.value) return
  await api.deleteProject(project.value.id)
  router.push('/projects')
}

async function doScaffold() {
  if (!project.value) return
  scaffolding.value = true
  scaffoldResult.value = null
  try {
    scaffoldResult.value = await api.scaffoldProject(project.value.id)
    await load()
  } catch (e: any) {
    scaffoldResult.value = { project_dir: '', git_inited: false, gh_created: false, error: e.message || String(e) }
  } finally {
    scaffolding.value = false
  }
}

async function removeEntry(entryId: string) {
  await api.setEntryProject(entryId, null)
  if (selectedEntry.value?.id === entryId) {
    selectedEntry.value = null
  }
  await load()
}

async function openPanel(entry: Entry) {
  selectedEntry.value = entry
  panelLoading.value = true
  try {
    panelMessages.value = await api.listMessages(entry.id)
  } catch {
    panelMessages.value = []
  } finally {
    panelLoading.value = false
  }
}

function closePanel() {
  selectedEntry.value = null
  panelMessages.value = []
}

async function advanceEntry(entryId: string) {
  advancingEntry.value = entryId
  try {
    await api.pipelineAdvance(entryId, 'advance')
    await load()
    // Refresh panel if open
    if (selectedEntry.value?.id === entryId) {
      const updated = entries.value.find(e => e.id === entryId)
      if (updated) selectedEntry.value = updated
    }
  } catch (e: any) {
    alert(e.message || 'Advance failed')
  } finally {
    advancingEntry.value = null
  }
}

function openFeedbackDialog(entryId: string, action: 'revise' | 'defer') {
  feedbackEntryId.value = entryId
  feedbackAction.value = action
  feedbackText.value = ''
  feedbackDialog.value = true
}

async function submitFeedback() {
  if (!feedbackEntryId.value) return
  advancingEntry.value = feedbackEntryId.value
  feedbackDialog.value = false
  try {
    await api.pipelineAdvance(feedbackEntryId.value, feedbackAction.value, feedbackText.value || undefined)
    await load()
    if (selectedEntry.value?.id === feedbackEntryId.value) {
      const updated = entries.value.find(e => e.id === feedbackEntryId.value)
      if (updated) selectedEntry.value = updated
    }
  } catch (e: any) {
    alert(e.message || `${feedbackAction.value} failed`)
  } finally {
    advancingEntry.value = null
  }
}

async function openExecuteDialog(entryId: string) {
  executeEntryId.value = entryId
  executeFeedback.value = ''
  executionContext.value = null
  executeDialog.value = true
  try {
    const ctx = await api.executionContext(entryId)
    executionContext.value = ctx
  } catch (e: any) {
    executionContext.value = null
  }
}

async function confirmExecute() {
  if (!executeEntryId.value) return
  executingEntry.value = executeEntryId.value
  executeDialog.value = false
  try {
    await api.executeEntry(executeEntryId.value, executeFeedback.value || undefined)
    await load()
    if (selectedEntry.value?.id === executeEntryId.value) {
      const updated = entries.value.find(e => e.id === executeEntryId.value)
      if (updated) selectedEntry.value = updated
    }
  } catch (e: any) {
    alert(e.message || 'Execute failed')
  } finally {
    executingEntry.value = null
  }
}

async function openVerifyDialog(entryId: string) {
  verifyEntryId.value = entryId
  verifySubmitting.value = false
  try {
    const ctx = await api.executionContext(entryId)
    verifyScenarios.value = (ctx.scenarios || []).map(s => ({ scenario: s, passed: false, notes: '' }))
  } catch {
    verifyScenarios.value = []
  }
  verifyDialog.value = true
}

async function submitVerification() {
  if (!verifyEntryId.value || verifyScenarios.value.length === 0) return
  verifySubmitting.value = true
  try {
    await api.verifyEntry(verifyEntryId.value, verifyScenarios.value)
    verifyDialog.value = false
    await load()
    if (selectedEntry.value?.id === verifyEntryId.value) {
      const updated = entries.value.find(e => e.id === verifyEntryId.value)
      if (updated) selectedEntry.value = updated
    }
  } catch (e: any) {
    alert(e.message || 'Verification failed')
  } finally {
    verifySubmitting.value = false
  }
}

onMounted(load)

// Live updates — refresh entries when any entry changes
const { subscribe } = useWebSocket()
subscribe('entry.updated', () => {
  const id = Number(route.params.id)
  if (id) api.projectEntries(id).then(e => { entries.value = e })
})
subscribe('entry.created', () => {
  const id = Number(route.params.id)
  if (id) api.projectEntries(id).then(e => { entries.value = e })
})
</script>

<template>
  <div class="space-y-6">
    <!-- Back link -->
    <RouterLink to="/projects" class="text-sm text-gray-500 hover:text-gray-300">&larr; Projects</RouterLink>

    <div v-if="loading" class="text-center py-12 text-gray-500">Loading...</div>

    <template v-else-if="project">
      <!-- Project header -->
      <div v-if="!editing" class="flex items-start justify-between">
        <div>
          <div class="flex items-center gap-2 mb-1">
            <span v-if="project.emoji" class="text-2xl">{{ project.emoji }}</span>
            <h1 class="text-xl font-bold">{{ project.name }}</h1>
            <span :class="['px-2 py-0.5 text-xs rounded-full', statusColor(project.status)]">
              {{ project.status }}
            </span>
          </div>
          <p v-if="project.description" class="text-sm text-gray-400">{{ project.description }}</p>
          <div class="flex items-center gap-3 mt-1">
            <span class="text-xs text-gray-600">{{ entries.length }} entries</span>
            <span v-if="totalPremiumRequests > 0" class="text-xs text-emerald-400" title="Total premium requests consumed across all entries">🎟️ {{ totalPremiumRequests.toFixed(2) }} premium requests</span>
            <span v-if="project.context_file" class="text-xs text-purple-400" title="Agents receive this file as context">📄 {{ project.context_file }}</span>
            <span v-if="project.workspace_type && project.workspace_type !== 'integrated'" class="text-xs text-sky-400" :title="`${project.workspace_type}: ${project.workspace_path || ''}`">
              {{ project.workspace_type === 'subfolder' ? '📁' : '🔗' }} {{ project.workspace_path || project.workspace_type }}
            </span>
          </div>
        </div>
        <div class="flex gap-2 items-center">
          <!-- Board/List toggle -->
          <div class="flex bg-gray-900 border border-gray-800 rounded-lg overflow-hidden">
            <button
              @click="viewMode = 'board'"
              :class="['px-3 py-1.5 text-xs transition-colors', viewMode === 'board' ? 'bg-gray-700 text-white' : 'text-gray-500 hover:text-gray-300']"
            >Board</button>
            <button
              @click="viewMode = 'list'"
              :class="['px-3 py-1.5 text-xs transition-colors', viewMode === 'list' ? 'bg-gray-700 text-white' : 'text-gray-500 hover:text-gray-300']"
            >List</button>
          </div>
          <button
            v-if="project.workspace_type === 'external'"
            @click="doScaffold"
            :disabled="scaffolding"
            class="px-3 py-1.5 text-sm text-emerald-400 hover:text-emerald-300 border border-gray-700 rounded-lg hover:border-emerald-700 transition-colors disabled:opacity-40"
            title="Create project directory, git init, scaffold structure, and optionally create GitHub repo"
          >
            {{ scaffolding ? 'Initializing...' : '🚀 Initialize' }}
          </button>
          <button @click="startEdit" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white border border-gray-700 rounded-lg hover:border-gray-600 transition-colors">
            Edit
          </button>
          <button @click="confirmDelete = true" class="px-3 py-1.5 text-sm text-red-400 hover:text-red-300 border border-gray-700 rounded-lg hover:border-red-700 transition-colors">
            Delete
          </button>
        </div>
      </div>

      <!-- Scaffold result -->
      <Transition
        enter-active-class="transition-opacity duration-200"
        leave-active-class="transition-opacity duration-150"
        enter-from-class="opacity-0" leave-to-class="opacity-0"
      >
        <div v-if="scaffoldResult" class="rounded-lg px-4 py-3 text-sm" :class="scaffoldResult.error ? 'bg-red-900/50 border border-red-800 text-red-300' : 'bg-emerald-900/50 border border-emerald-800 text-emerald-300'">
          <div v-if="!scaffoldResult.error">
            ✓ Project initialized at <code class="text-xs">{{ scaffoldResult.project_dir }}</code>
            <span v-if="scaffoldResult.gh_created"> · GitHub repo created</span>
          </div>
          <div v-else>{{ scaffoldResult.error }}</div>
          <button @click="scaffoldResult = null" class="text-xs underline mt-1 opacity-60 hover:opacity-100">dismiss</button>
        </div>
      </Transition>

      <!-- Edit form -->
      <form v-else @submit.prevent="saveEdit" class="bg-gray-900 border border-gray-800 rounded-lg p-4 space-y-3">
        <div class="flex gap-3">
          <input v-model="editForm.emoji" placeholder="📋" class="w-14 bg-gray-950 border border-gray-700 rounded-lg px-2 py-2 text-center text-lg focus:outline-none focus:ring-2 focus:ring-sky-500" maxlength="4" />
          <input v-model="editForm.name" placeholder="Project name" class="flex-1 bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500" />
        </div>
        <textarea v-model="editForm.description" placeholder="Description" rows="2" class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500 resize-none" />
        <select v-model="editForm.status" class="bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500">
          <option value="active">Active</option>
          <option value="paused">Paused</option>
          <option value="archived">Archived</option>
        </select>
        <input v-model="editForm.context_file" placeholder="Context file path (e.g. .spec/context/project.md)" class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-400 focus:outline-none focus:ring-2 focus:ring-sky-500" />

        <!-- Workspace settings -->
        <div class="border-t border-gray-800 pt-3 space-y-3">
          <label class="block text-xs font-medium text-gray-500 uppercase tracking-wider">Workspace</label>
          <select v-model="editForm.workspace_type" class="bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500">
            <option value="integrated">Integrated (workspace root)</option>
            <option value="subfolder">Subfolder (same repo)</option>
            <option value="external">External (own repo)</option>
          </select>
          <input
            v-if="editForm.workspace_type !== 'integrated'"
            v-model="editForm.workspace_path"
            :placeholder="editForm.workspace_type === 'subfolder' ? 'scripts/becoming/' : 'projects/space-center/'"
            class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-400 focus:outline-none focus:ring-2 focus:ring-sky-500"
          />
          <template v-if="editForm.workspace_type === 'external'">
            <input v-model="editForm.github_repo" placeholder="GitHub repo (e.g. cpuchip/space-center)" class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-400 focus:outline-none focus:ring-2 focus:ring-sky-500" />
            <select v-model="editForm.repo_visibility" class="bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500">
              <option value="private">Private</option>
              <option value="public">Public</option>
            </select>
          </template>
        </div>

        <div class="flex justify-end gap-2">
          <button type="button" @click="editing = false" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white">Cancel</button>
          <button type="submit" :disabled="!editForm.name.trim() || saving" class="px-4 py-2 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-500 disabled:opacity-40">Save</button>
        </div>
      </form>

      <!-- Delete confirmation -->
      <Teleport to="body">
        <dialog
          ref="deleteDialog"
          :open="confirmDelete"
          class="fixed inset-0 z-40 flex items-center justify-center bg-transparent"
          v-if="confirmDelete"
        >
          <div class="fixed inset-0 bg-black/50" @click="confirmDelete = false" />
          <div class="relative bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl max-w-sm mx-auto">
            <h3 class="font-semibold mb-2">Delete project?</h3>
            <p class="text-sm text-gray-400 mb-4">Entries will be unlinked, not deleted.</p>
            <div class="flex justify-end gap-2">
              <button @click="confirmDelete = false" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white">Cancel</button>
              <button @click="doDelete" class="px-4 py-2 text-sm bg-red-600 text-white rounded-lg hover:bg-red-500">Delete</button>
            </div>
          </div>
        </dialog>
      </Teleport>

      <!-- Feedback dialog for revise/defer -->
      <Teleport to="body">
        <dialog
          :open="feedbackDialog"
          class="fixed inset-0 z-40 flex items-center justify-center bg-transparent"
          v-if="feedbackDialog"
        >
          <div class="fixed inset-0 bg-black/50" @click="feedbackDialog = false" />
          <div class="relative bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl max-w-md mx-auto w-full">
            <h3 class="font-semibold mb-2">{{ feedbackAction === 'revise' ? 'Revise' : 'Defer' }} Entry</h3>
            <p class="text-sm text-gray-400 mb-3">
              {{ feedbackAction === 'revise' ? 'Provide feedback to guide the revision.' : 'Add a note about why this is deferred.' }}
            </p>
            <textarea
              v-model="feedbackText"
              :placeholder="feedbackAction === 'revise' ? 'What needs to change...' : 'Reason for deferring...'"
              rows="3"
              class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500 resize-none mb-4"
            />
            <div class="flex justify-end gap-2">
              <button @click="feedbackDialog = false" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white">Cancel</button>
              <button
                @click="submitFeedback"
                :disabled="feedbackAction === 'revise' && !feedbackText.trim()"
                class="px-4 py-2 text-sm rounded-lg transition-colors disabled:opacity-40"
                :class="feedbackAction === 'revise' ? 'bg-amber-600 text-white hover:bg-amber-500' : 'bg-gray-600 text-white hover:bg-gray-500'"
              >{{ feedbackAction === 'revise' ? 'Revise' : 'Defer' }}</button>
            </div>
          </div>
        </dialog>
      </Teleport>

      <!-- Execute confirmation dialog (Phase 4e) -->
      <Teleport to="body">
        <dialog
          :open="executeDialog"
          class="fixed inset-0 z-40 flex items-center justify-center bg-transparent"
          v-if="executeDialog"
        >
          <div class="fixed inset-0 bg-black/50" @click="executeDialog = false" />
          <div class="relative bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl max-w-lg mx-auto w-full">
            <h3 class="font-semibold mb-2 text-green-400">▶ Execute Entry</h3>
            <div v-if="!executionContext" class="text-sm text-gray-500 py-4">Loading execution context...</div>
            <template v-else>
              <div class="space-y-3 mb-4">
                <div class="flex items-center gap-3 text-sm">
                  <span class="text-gray-500">Model:</span>
                  <span class="text-gray-200 font-mono">{{ executionContext.model }}</span>
                </div>
                <div class="flex items-center gap-3 text-sm">
                  <span class="text-gray-500">Cost:</span>
                  <span class="text-gray-200">{{ executionContext.cost }} premium request{{ executionContext.cost !== 1 ? 's' : '' }}</span>
                </div>
                <div class="flex items-center gap-3 text-sm">
                  <span class="text-gray-500">Scenarios:</span>
                  <span class="text-gray-200">{{ executionContext.scenarios?.length || 0 }}</span>
                </div>
                <div v-if="executionContext.scenarios?.length" class="text-sm">
                  <div class="text-gray-500 mb-1">Acceptance criteria:</div>
                  <ul class="list-disc list-inside space-y-1 text-gray-300 text-xs bg-gray-950 rounded-lg px-3 py-2 max-h-40 overflow-y-auto">
                    <li v-for="(s, i) in executionContext.scenarios" :key="i">{{ s }}</li>
                  </ul>
                </div>
              </div>
              <textarea
                v-model="executeFeedback"
                placeholder="Optional guidance for the agent..."
                rows="2"
                class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-green-500 resize-none mb-4"
              />
              <div class="flex justify-end gap-2">
                <button @click="executeDialog = false" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white">Cancel</button>
                <button
                  @click="confirmExecute"
                  class="px-4 py-2 text-sm bg-green-600 text-white rounded-lg hover:bg-green-500 transition-colors"
                >Execute ▶</button>
              </div>
            </template>
          </div>
        </dialog>
      </Teleport>

      <!-- Verify dialog (Phase 4e) -->
      <Teleport to="body">
        <dialog
          :open="verifyDialog"
          class="fixed inset-0 z-40 flex items-center justify-center bg-transparent"
          v-if="verifyDialog"
        >
          <div class="fixed inset-0 bg-black/50" @click="verifyDialog = false" />
          <div class="relative bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl max-w-lg mx-auto w-full">
            <h3 class="font-semibold mb-2 text-emerald-400">✓ Verify Scenarios</h3>
            <p class="text-sm text-gray-400 mb-4">Check each scenario that passes. Failed scenarios will return the entry to planned.</p>
            <div v-if="verifyScenarios.length === 0" class="text-sm text-gray-500 py-4">No scenarios found.</div>
            <div v-else class="space-y-3 mb-4 max-h-80 overflow-y-auto">
              <div
                v-for="(s, i) in verifyScenarios"
                :key="i"
                :class="['border rounded-lg px-3 py-2', s.passed ? 'border-green-800 bg-green-950/30' : 'border-gray-700']"
              >
                <label class="flex items-start gap-2 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="s.passed"
                    class="mt-1 accent-green-500"
                  />
                  <span class="text-sm text-gray-200">{{ s.scenario }}</span>
                </label>
                <input
                  v-if="!s.passed"
                  v-model="s.notes"
                  placeholder="What failed? (optional)"
                  class="mt-2 w-full bg-gray-950 border border-gray-700 rounded px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-amber-500"
                />
              </div>
            </div>
            <div class="flex justify-between items-center">
              <span class="text-xs text-gray-500">{{ verifyScenarios.filter(s => s.passed).length }}/{{ verifyScenarios.length }} passed</span>
              <div class="flex gap-2">
                <button @click="verifyDialog = false" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white">Cancel</button>
                <button
                  @click="submitVerification"
                  :disabled="verifySubmitting"
                  class="px-4 py-2 text-sm rounded-lg transition-colors disabled:opacity-40"
                  :class="verifyScenarios.every(s => s.passed) ? 'bg-emerald-600 text-white hover:bg-emerald-500' : 'bg-amber-600 text-white hover:bg-amber-500'"
                >{{ verifyScenarios.every(s => s.passed) ? '✓ All Pass — Verify' : 'Submit (some failed)' }}</button>
              </div>
            </div>
          </div>
        </dialog>
      </Teleport>

      <!-- Empty state -->
      <div v-if="entries.length === 0" class="text-center py-8">
        <div class="text-gray-600 mb-1">No entries in this project</div>
        <div class="text-sm text-gray-500">Assign entries from the Entries view.</div>
      </div>

      <!-- ========== BOARD VIEW (3-column) ========== -->
      <div v-else-if="viewMode === 'board'" class="grid grid-cols-3 gap-4" style="min-height: 400px;">
        <div
          v-for="col in boardColumns"
          :key="col.key"
          class="min-w-0"
        >
          <!-- Column header -->
          <div :class="['flex items-center justify-between px-3 py-2 rounded-t-lg border-b-2', col.borderColor]">
            <div class="flex items-center gap-2">
              <span :class="['px-2 py-0.5 text-xs rounded-full font-medium', col.color]">
                {{ col.label }}
              </span>
              <span class="text-xs text-gray-600">{{ col.entries.length }}</span>
            </div>
          </div>

          <!-- Column body -->
          <div class="space-y-2 mt-2 min-h-[100px]">
            <div
              v-for="entry in col.entries"
              :key="entry.id"
              @click="openPanel(entry)"
              :class="[
                'bg-gray-900 border rounded-lg px-3 py-2.5 cursor-pointer hover:border-gray-600 transition-colors border-l-3',
                routeStatusIndicator(entry)?.class || 'border-gray-800 border-l-gray-800',
                selectedEntry?.id === entry.id ? 'ring-1 ring-sky-500' : ''
              ]"
            >
              <div class="font-medium text-gray-200 text-sm truncate">{{ entry.title }}</div>
              <p v-if="entry.body" class="text-xs text-gray-500 mt-1 line-clamp-2">{{ entry.body.slice(0, 120) }}</p>
              <div class="flex items-center gap-1.5 mt-2 flex-wrap">
                <span class="text-xs text-gray-600 bg-gray-800 px-1.5 py-0.5 rounded">{{ entry.category }}</span>
                <!-- Notebook badge in Inbox -->
                <span v-if="entry.notebook" class="text-xs bg-indigo-900/50 text-indigo-300 px-1.5 py-0.5 rounded" title="Notebook entry">📓 Notebook</span>
                <!-- Sub-stage badge in Working column -->
                <span
                  v-if="col.key === 'working' && entry.maturity"
                  :class="['text-xs px-1.5 py-0.5 rounded', maturityColor(entry.maturity)]"
                >{{ stageLabels[entry.maturity] || entry.maturity }}</span>
                <!-- Route status -->
                <span
                  v-if="routeStatusIndicator(entry)"
                  :class="['text-xs px-1.5 py-0.5 rounded', routeStatusIndicator(entry)!.badge]"
                >{{ routeStatusIndicator(entry)!.icon }} {{ routeStatusIndicator(entry)!.label }}</span>
              </div>

              <!-- Pipeline action buttons -->
              <div v-if="canAdvance(entry) || canRevise(entry) || canExecute(entry) || canVerify(entry)" class="flex gap-1.5 mt-2 pt-2 border-t border-gray-800" @click.stop>
                <button
                  v-if="canAdvance(entry)"
                  @click.stop="advanceEntry(entry.id)"
                  :disabled="advancingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-sky-900/50 text-sky-300 rounded hover:bg-sky-800 transition-colors disabled:opacity-40"
                  :title="'Advance to next stage'"
                >▶ Advance</button>
                <button
                  v-if="canRevise(entry)"
                  @click.stop="openFeedbackDialog(entry.id, 'revise')"
                  :disabled="advancingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-amber-900/50 text-amber-300 rounded hover:bg-amber-800 transition-colors disabled:opacity-40"
                >↻ Revise</button>
                <button
                  v-if="canAdvance(entry)"
                  @click.stop="openFeedbackDialog(entry.id, 'defer')"
                  :disabled="advancingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-gray-800 text-gray-400 rounded hover:bg-gray-700 transition-colors disabled:opacity-40"
                >⏸ Defer</button>
                <button
                  v-if="canExecute(entry)"
                  @click.stop="openExecuteDialog(entry.id)"
                  :disabled="executingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-green-900/50 text-green-300 rounded hover:bg-green-800 transition-colors disabled:opacity-40"
                >▶ Execute</button>
                <button
                  v-if="canVerify(entry)"
                  @click.stop="openVerifyDialog(entry.id)"
                  class="px-2 py-1 text-xs bg-emerald-900/50 text-emerald-300 rounded hover:bg-emerald-800 transition-colors"
                >✓ Verify</button>
              </div>
            </div>

            <!-- Empty column indicator -->
            <div
              v-if="col.entries.length === 0"
              class="text-center py-6 text-gray-700 text-xs"
            >No entries</div>
          </div>
        </div>
      </div>

      <!-- ========== LIST VIEW (original) ========== -->
      <div v-else class="space-y-6">
        <div v-for="stage in nonEmptyStages" :key="stage">
          <div class="flex items-center gap-2 mb-2">
            <span :class="['px-2 py-0.5 text-xs rounded-full font-medium', maturityColor(stage)]">
              {{ stageLabels[stage] || stage }}
            </span>
            <span class="text-xs text-gray-600">{{ entriesByMaturity[stage]?.length ?? 0 }}</span>
          </div>
          <div class="space-y-2">
            <div
              v-for="entry in entriesByMaturity[stage]"
              :key="entry.id"
              :class="[
                'bg-gray-900 border rounded-lg px-4 py-3 hover:border-gray-700 transition-colors flex items-start justify-between border-l-3',
                routeStatusIndicator(entry)?.class || 'border-gray-800 border-l-gray-800'
              ]"
            >
              <div class="flex-1 min-w-0 cursor-pointer" @click="openPanel(entry)">
                <div class="font-medium text-gray-200 text-sm truncate">{{ entry.title }}</div>
                <p v-if="entry.body" class="text-xs text-gray-500 mt-1 line-clamp-2">{{ entry.body.slice(0, 200) }}</p>
                <div class="flex items-center gap-2 mt-2">
                  <span class="text-xs text-gray-600 bg-gray-800 px-1.5 py-0.5 rounded">{{ entry.category }}</span>
                  <span
                    v-if="routeStatusIndicator(entry)"
                    :class="['text-xs px-1.5 py-0.5 rounded', routeStatusIndicator(entry)!.badge]"
                  >{{ routeStatusIndicator(entry)!.icon }} {{ routeStatusIndicator(entry)!.label }}</span>
                </div>
              </div>
              <div class="flex items-center gap-1 ml-2 shrink-0">
                <button
                  v-if="canAdvance(entry)"
                  @click="advanceEntry(entry.id)"
                  :disabled="advancingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-sky-900/50 text-sky-300 rounded hover:bg-sky-800 transition-colors disabled:opacity-40"
                  title="Advance to next stage"
                >▶</button>
                <button
                  v-if="canExecute(entry)"
                  @click="openExecuteDialog(entry.id)"
                  :disabled="executingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-green-900/50 text-green-300 rounded hover:bg-green-800 transition-colors disabled:opacity-40"
                  title="Execute"
                >▶</button>
                <button
                  v-if="canVerify(entry)"
                  @click="openVerifyDialog(entry.id)"
                  class="px-2 py-1 text-xs bg-emerald-900/50 text-emerald-300 rounded hover:bg-emerald-800 transition-colors"
                  title="Verify scenarios"
                >✓</button>
                <button
                  v-if="canRevise(entry)"
                  @click="openFeedbackDialog(entry.id, 'revise')"
                  :disabled="advancingEntry === entry.id"
                  class="px-2 py-1 text-xs bg-amber-900/50 text-amber-300 rounded hover:bg-amber-800 transition-colors disabled:opacity-40"
                  title="Revise"
                >↻</button>
                <button
                  @click.prevent="removeEntry(entry.id)"
                  class="px-2 py-1 text-xs text-gray-600 hover:text-red-400"
                  title="Remove from project"
                >✕</button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- ========== SLIDE-OUT PANEL ========== -->
      <Teleport to="body">
        <Transition
          enter-active-class="transition-transform duration-200 ease-out"
          leave-active-class="transition-transform duration-150 ease-in"
          enter-from-class="translate-x-full"
          leave-to-class="translate-x-full"
        >
          <div
            v-if="selectedEntry"
            class="fixed inset-y-0 right-0 z-30 w-96 max-w-[90vw] bg-gray-950 border-l border-gray-800 shadow-2xl overflow-y-auto"
          >
            <!-- Panel header -->
            <div class="sticky top-0 bg-gray-950 border-b border-gray-800 px-4 py-3 flex items-center justify-between">
              <div class="flex items-center gap-2 min-w-0">
                <span :class="['px-2 py-0.5 text-xs rounded-full font-medium', maturityColor(selectedEntry.maturity || 'raw')]">
                  {{ stageLabels[selectedEntry.maturity || 'raw'] }}
                </span>
                <span
                  v-if="routeStatusIndicator(selectedEntry)"
                  :class="['text-xs px-1.5 py-0.5 rounded', routeStatusIndicator(selectedEntry)!.badge]"
                >{{ routeStatusIndicator(selectedEntry)!.icon }} {{ routeStatusIndicator(selectedEntry)!.label }}</span>
              </div>
              <div class="flex items-center gap-2">
                <RouterLink
                  :to="`/entries/${selectedEntry.id}`"
                  class="text-xs text-sky-400 hover:text-sky-300"
                >Open →</RouterLink>
                <button @click="closePanel" class="text-gray-500 hover:text-gray-300 text-lg">&times;</button>
              </div>
            </div>

            <!-- Panel body -->
            <div class="px-4 py-4 space-y-4">
              <h2 class="text-lg font-semibold text-gray-100">{{ selectedEntry.title }}</h2>

              <div class="flex items-center gap-2 flex-wrap">
                <span class="text-xs bg-gray-800 text-gray-400 px-2 py-0.5 rounded">{{ selectedEntry.category }}</span>
                <span v-if="selectedEntry.agent_route" class="text-xs bg-gray-800 text-purple-400 px-2 py-0.5 rounded">{{ selectedEntry.agent_route }}</span>
              </div>

              <div v-if="selectedEntry.body" class="text-sm text-gray-300 whitespace-pre-wrap">{{ selectedEntry.body }}</div>

              <!-- Pipeline actions -->
              <div v-if="canAdvance(selectedEntry) || canRevise(selectedEntry) || canExecute(selectedEntry) || canVerify(selectedEntry)" class="flex gap-2 pt-2 border-t border-gray-800">
                <button
                  v-if="canAdvance(selectedEntry)"
                  @click="advanceEntry(selectedEntry!.id)"
                  :disabled="advancingEntry === selectedEntry!.id"
                  class="px-3 py-1.5 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-500 transition-colors disabled:opacity-40"
                >▶ Advance</button>
                <button
                  v-if="canRevise(selectedEntry)"
                  @click="openFeedbackDialog(selectedEntry!.id, 'revise')"
                  :disabled="advancingEntry === selectedEntry!.id"
                  class="px-3 py-1.5 text-sm bg-amber-600 text-white rounded-lg hover:bg-amber-500 transition-colors disabled:opacity-40"
                >↻ Revise</button>
                <button
                  v-if="canAdvance(selectedEntry)"
                  @click="openFeedbackDialog(selectedEntry!.id, 'defer')"
                  :disabled="advancingEntry === selectedEntry!.id"
                  class="px-3 py-1.5 text-sm bg-gray-700 text-gray-300 rounded-lg hover:bg-gray-600 transition-colors disabled:opacity-40"
                >⏸ Defer</button>
                <button
                  v-if="canExecute(selectedEntry)"
                  @click="openExecuteDialog(selectedEntry!.id)"
                  :disabled="executingEntry === selectedEntry!.id"
                  class="px-3 py-1.5 text-sm bg-green-600 text-white rounded-lg hover:bg-green-500 transition-colors disabled:opacity-40"
                >▶ Execute</button>
                <button
                  v-if="canVerify(selectedEntry)"
                  @click="openVerifyDialog(selectedEntry!.id)"
                  class="px-3 py-1.5 text-sm bg-emerald-600 text-white rounded-lg hover:bg-emerald-500 transition-colors"
                >✓ Verify</button>
              </div>

              <!-- Conversation history -->
              <div class="pt-2 border-t border-gray-800">
                <h3 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-2">Conversation</h3>
                <div v-if="panelLoading" class="text-sm text-gray-600">Loading...</div>
                <div v-else-if="panelMessages.length === 0" class="text-sm text-gray-600">No messages yet</div>
                <div v-else class="space-y-2 max-h-80 overflow-y-auto">
                  <div
                    v-for="msg in panelMessages"
                    :key="msg.id"
                    :class="[
                      'rounded-lg px-3 py-2 text-sm',
                      msg.role === 'human' ? 'bg-sky-900/30 text-sky-200' : 'bg-gray-900 text-gray-300'
                    ]"
                  >
                    <div class="text-xs text-gray-600 mb-1">{{ msg.role === 'human' ? 'You' : 'Agent' }}</div>
                    <div class="whitespace-pre-wrap">{{ msg.content.slice(0, 500) }}{{ msg.content.length > 500 ? '...' : '' }}</div>
                  </div>
                </div>
              </div>

              <!-- Agent output preview -->
              <div v-if="selectedEntry.agent_output" class="pt-2 border-t border-gray-800">
                <h3 class="text-sm font-medium text-gray-500 uppercase tracking-wider mb-2">Agent Output</h3>
                <div class="text-sm text-gray-300 whitespace-pre-wrap bg-gray-900 rounded-lg px-3 py-2 max-h-60 overflow-y-auto">
                  {{ selectedEntry.agent_output.slice(0, 1000) }}{{ (selectedEntry.agent_output?.length ?? 0) > 1000 ? '...' : '' }}
                </div>
              </div>

              <!-- Remove from project -->
              <div class="pt-2 border-t border-gray-800">
                <button
                  @click="removeEntry(selectedEntry!.id)"
                  class="text-xs text-gray-600 hover:text-red-400 transition-colors"
                >Remove from project</button>
              </div>
            </div>
          </div>
        </Transition>
        <!-- Backdrop -->
        <Transition
          enter-active-class="transition-opacity duration-200"
          leave-active-class="transition-opacity duration-150"
          enter-from-class="opacity-0"
          leave-to-class="opacity-0"
        >
          <div
            v-if="selectedEntry"
            class="fixed inset-0 z-20 bg-black/30"
            @click="closePanel"
          />
        </Transition>
      </Teleport>
    </template>
  </div>
</template>
