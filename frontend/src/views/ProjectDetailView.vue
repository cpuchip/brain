<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Project, type Entry, type SessionMessage } from '../api'

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

const editForm = ref({ name: '', description: '', emoji: '', status: '' })

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

// For board mode, show all stages (even empty) so the kanban layout is consistent
const boardStages = computed(() => {
  const stages: string[] = [...maturityStages]
  if ((entriesByMaturity.value['unset']?.length ?? 0) > 0) {
    stages.unshift('unset')
  }
  return stages
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

function maturityBorderColor(stage: string) {
  switch (stage) {
    case 'raw': return 'border-gray-700'
    case 'researched': return 'border-blue-800'
    case 'planned': return 'border-purple-800'
    case 'specced': return 'border-indigo-800'
    case 'executing': return 'border-amber-800'
    case 'verified': return 'border-green-800'
    default: return 'border-gray-800'
  }
}

function routeStatusIndicator(entry: Entry) {
  switch (entry.route_status) {
    case 'your_turn': return { class: 'border-l-amber-400', badge: 'bg-amber-900 text-amber-300', label: 'Your Turn', icon: '🔔' }
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

onMounted(load)
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
          <div class="text-xs text-gray-600 mt-1">{{ entries.length }} entries</div>
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
          <button @click="startEdit" class="px-3 py-1.5 text-sm text-gray-400 hover:text-white border border-gray-700 rounded-lg hover:border-gray-600 transition-colors">
            Edit
          </button>
          <button @click="confirmDelete = true" class="px-3 py-1.5 text-sm text-red-400 hover:text-red-300 border border-gray-700 rounded-lg hover:border-red-700 transition-colors">
            Delete
          </button>
        </div>
      </div>

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

      <!-- Empty state -->
      <div v-if="entries.length === 0" class="text-center py-8">
        <div class="text-gray-600 mb-1">No entries in this project</div>
        <div class="text-sm text-gray-500">Assign entries from the Entries view.</div>
      </div>

      <!-- ========== BOARD VIEW ========== -->
      <div v-else-if="viewMode === 'board'" class="flex gap-3 overflow-x-auto pb-4 -mx-4 px-4" style="min-height: 400px;">
        <div
          v-for="stage in boardStages"
          :key="stage"
          class="flex-shrink-0 w-64"
        >
          <!-- Column header -->
          <div :class="['flex items-center justify-between px-3 py-2 rounded-t-lg border-b-2', maturityBorderColor(stage)]">
            <div class="flex items-center gap-2">
              <span :class="['px-2 py-0.5 text-xs rounded-full font-medium', maturityColor(stage)]">
                {{ stageLabels[stage] || stage }}
              </span>
              <span class="text-xs text-gray-600">{{ entriesByMaturity[stage]?.length ?? 0 }}</span>
            </div>
          </div>

          <!-- Column body -->
          <div class="space-y-2 mt-2 min-h-[100px]">
            <div
              v-for="entry in entriesByMaturity[stage]"
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
                <span
                  v-if="routeStatusIndicator(entry)"
                  :class="['text-xs px-1.5 py-0.5 rounded', routeStatusIndicator(entry)!.badge]"
                >{{ routeStatusIndicator(entry)!.icon }} {{ routeStatusIndicator(entry)!.label }}</span>
              </div>

              <!-- Pipeline action buttons -->
              <div v-if="canAdvance(entry) || canRevise(entry)" class="flex gap-1.5 mt-2 pt-2 border-t border-gray-800" @click.stop>
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
              </div>
            </div>

            <!-- Empty column indicator -->
            <div
              v-if="(entriesByMaturity[stage]?.length ?? 0) === 0"
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
              <div v-if="canAdvance(selectedEntry) || canRevise(selectedEntry)" class="flex gap-2 pt-2 border-t border-gray-800">
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
