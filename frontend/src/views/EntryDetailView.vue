<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Entry, type SubTask, type Project, type SessionMessage, type Commission } from '../api'
import { useAutoExpand } from '../composables/useAutoExpand'
import { renderMarkdown } from '../composables/useMarkdown'
import { useFilePanel } from '../composables/useFilePanel'
import { useWebSocket } from '../composables/useWebSocket'
import FileViewer from '../components/FileViewer.vue'
import CommissionDialog from '../components/CommissionDialog.vue'
import ResumeDialog from '../components/ResumeDialog.vue'

const route = useRoute()
const router = useRouter()
const entry = ref<Entry | null>(null)
const projects = ref<Project[]>([])
const messages = ref<SessionMessage[]>([])
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const toast = ref('')
const toastTimeout = ref<ReturnType<typeof setTimeout>>()
const agentContext = ref('')
const showAgentContext = ref(false)

// Commission state
const commission = ref<Commission | null>(null)
const commissionDialog = ref(false)
const commissionPausing = ref(false)
const commissionResuming = ref(false)
const commissionRevoking = ref(false)
const showResumeDialog = ref(false)
const resumeSurfaceReason = ref('')

const editForm = ref({
  title: '',
  category: '',
  body: '',
  tags: '',
  status: '',
  due_date: '',
  project_id: null as number | null,
})

const categories = ['people', 'projects', 'ideas', 'actions', 'study', 'journal', 'inbox']

const isDone = computed(() => {
  if (!entry.value) return false
  if (entry.value.category === 'actions') return entry.value.action_done
  if (entry.value.category === 'projects') return entry.value.status === 'done'
  return false
})

const isActionable = computed(() => {
  if (!entry.value) return false
  return entry.value.category === 'actions' || entry.value.category === 'projects'
})

function showToast(msg: string) {
  toast.value = msg
  if (toastTimeout.value) clearTimeout(toastTimeout.value)
  toastTimeout.value = setTimeout(() => { toast.value = '' }, 2000)
}

async function load() {
  loading.value = true
  try {
    const [e, p, m] = await Promise.all([
      api.getEntry(route.params.id as string),
      api.listProjects(),
      api.listMessages(route.params.id as string),
    ])
    entry.value = e
    projects.value = p
    messages.value = m

    // Load agent context if entry has a project
    if (e.project_id) {
      try {
        const ctx = await api.entryContext(e.id)
        agentContext.value = ctx.formatted || ''
      } catch {
        agentContext.value = ''
      }
    } else {
      agentContext.value = ''
    }
    // Load active commission for this entry
    try {
      const commissions = await api.listCommissions()
      commission.value = commissions.find(c => c.entry_id === e.id && (c.status === 'active' || c.status === 'paused')) || null
    } catch {
      commission.value = null
    }
  } finally {
    loading.value = false
  }
}

// Commission actions
const hasActiveCommission = computed(() => commission.value != null && (commission.value.status === 'active' || commission.value.status === 'paused'))

const costBreakdown = computed(() => {
  const decisions = commission.value?.decisions ?? []
  const map = new Map<string, { cost: number; count: number }>()
  for (const d of decisions) {
    const t = d.cost_type || 'pipeline'
    const entry = map.get(t) ?? { cost: 0, count: 0 }
    entry.cost += d.cost
    entry.count++
    map.set(t, entry)
  }
  return Array.from(map.entries()).map(([type, v]) => ({ type, ...v }))
})

function canCommission(): boolean {
  if (!entry.value) return false
  if (entry.value.notebook) return false
  if (hasActiveCommission.value) return false
  const m = entry.value.maturity || 'raw'
  return ['raw', 'researched', 'planned', 'specced'].includes(m)
}

function openCommissionDialog() {
  commissionDialog.value = true
}

function onCommissioned(c: Commission) {
  commission.value = c
  commissionDialog.value = false
  showToast('Steward commissioned')
}

async function pauseCommission() {
  if (!commission.value || commissionPausing.value) return
  commissionPausing.value = true
  try {
    await api.pauseCommission(commission.value.id)
    commission.value.status = 'paused'
    showToast('Commission paused')
  } catch (e: any) {
    showToast(e.message || 'Failed to pause')
  } finally {
    commissionPausing.value = false
  }
}

async function resumeCommission() {
  if (!commission.value || commissionResuming.value) return
  commissionResuming.value = true
  try {
    await api.resumeCommission(commission.value.id)
    commission.value.status = 'active'
    showResumeDialog.value = false
    showToast('Commission resumed')
  } catch (e: any) {
    showToast(e.message || 'Failed to resume')
  } finally {
    commissionResuming.value = false
  }
}

function openResumeDialog() {
  // Extract the surface reason from the last system message about surfacing
  const surfaceMsg = [...messages.value].reverse().find(
    m => m.content.includes('Surfacing for your input')
  )
  if (surfaceMsg) {
    // Extract the detail text after the header line
    const lines = surfaceMsg.content.split('\n')
    const detailLines = lines.filter(l => !l.includes('Surfacing for your input') && !l.includes('commission is paused') && l.trim())
    resumeSurfaceReason.value = detailLines.join('\n').trim()
  } else {
    resumeSurfaceReason.value = ''
  }
  showResumeDialog.value = true
}

async function onResumeWithFeedback(feedback: string) {
  if (!commission.value || !entry.value) return
  if (feedback) {
    try {
      await api.reply(entry.value.id, feedback)
    } catch {
      // Non-fatal — proceed with resume even if reply fails
    }
  }
  await resumeCommission()
}

async function revokeCommission() {
  if (!commission.value || commissionRevoking.value) return
  commissionRevoking.value = true
  try {
    await api.revokeCommission(commission.value.id)
    commission.value = null
    showToast('Commission revoked — manual control restored')
  } catch (e: any) {
    showToast(e.message || 'Failed to revoke')
  } finally {
    commissionRevoking.value = false
  }
}

function startEdit() {
  if (!entry.value) return
  editForm.value = {
    title: entry.value.title,
    category: entry.value.category,
    body: entry.value.body,
    tags: (entry.value.tags || []).join(', '),
    status: entry.value.status || '',
    due_date: entry.value.due_date || '',
    project_id: entry.value.project_id ?? null,
  }
  editing.value = true
}

async function save() {
  if (!entry.value) return
  saving.value = true
  try {
    const tags = editForm.value.tags
      ? editForm.value.tags.split(',').map(t => t.trim()).filter(Boolean)
      : []
    await api.updateEntry(entry.value.id, {
      title: editForm.value.title,
      category: editForm.value.category,
      body: editForm.value.body,
      tags,
      status: editForm.value.status || undefined,
      due_date: editForm.value.due_date || undefined,
      project_id: editForm.value.project_id,
    })
    editing.value = false
    showToast('Saved')
    await load()
  } finally {
    saving.value = false
  }
}

async function toggleDone() {
  if (!entry.value) return
  const wasDone = isDone.value
  try {
    if (entry.value.category === 'actions') {
      await api.updateEntry(entry.value.id, { action_done: !wasDone })
    } else if (entry.value.category === 'projects') {
      await api.updateEntry(entry.value.id, { status: wasDone ? 'active' : 'done' })
    }
    showToast(wasDone ? 'Reopened' : 'Done!')
    await load()
  } catch {
    showToast('Failed to update')
  }
}

// Status verbs for one-tap status change. `null` clears the status.
const STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: '', label: '(none)' },
  { value: 'active', label: 'Active' },
  { value: 'waiting', label: 'Waiting' },
  { value: 'roadmap', label: 'Roadmap' },
  { value: 'someday', label: 'Someday' },
  { value: 'done', label: 'Done' },
  { value: 'archived', label: 'Archived' },
]
const statusMenuOpen = ref(false)
const changingStatus = ref(false)
async function changeStatus(newStatus: string) {
  if (!entry.value || changingStatus.value) return
  statusMenuOpen.value = false
  if ((entry.value.status || '') === newStatus) return
  changingStatus.value = true
  try {
    // Empty string means clear the status field.
    await api.updateEntry(entry.value.id, { status: newStatus || undefined as any })
    showToast(newStatus ? `Status: ${newStatus}` : 'Status cleared')
    await load()
  } catch {
    showToast('Failed to change status')
  } finally {
    changingStatus.value = false
  }
}

async function deleteEntry() {
  if (!entry.value) return
  await api.deleteEntry(entry.value.id)
  router.push('/entries')
}

async function reclassify(category: string) {
  if (!entry.value) return
  const result = await api.reclassify(entry.value.id, category)
  showToast(`Moved to ${category}`)
  router.push(`/entries/${result.id}`)
}

// AI Classify
const classifying = ref(false)
const togglingAutoContinue = ref(false)
const togglingNotebook = ref(false)
async function classify() {
  if (!entry.value || classifying.value) return
  classifying.value = true
  try {
    await api.classify(entry.value.id)
    showToast('Classified!')
    await load()
  } catch {
    showToast('Classification failed')
  } finally {
    classifying.value = false
  }
}

async function toggleAutoContinue() {
  if (!entry.value || togglingAutoContinue.value) return
  togglingAutoContinue.value = true
  try {
    const newVal = !entry.value.auto_continue
    await api.setAutoContinue(entry.value.id, newVal)
    entry.value.auto_continue = newVal
    showToast(newVal ? 'Auto-continue on — delegation mode' : 'Auto-continue off — sabbath mode')
  } catch {
    showToast('Failed to toggle auto-continue')
  } finally {
    togglingAutoContinue.value = false
  }
}

async function toggleNotebook() {
  if (!entry.value || togglingNotebook.value) return
  togglingNotebook.value = true
  try {
    const newVal = !entry.value.notebook
    await api.setNotebook(entry.value.id, newVal)
    entry.value.notebook = newVal
    showToast(newVal ? '📓 Moved to notebook — outside pipeline' : '📓 Removed from notebook — back in pipeline')
  } catch {
    showToast('Failed to toggle notebook')
  } finally {
    togglingNotebook.value = false
  }
}

// Subtasks
const showSubTasks = ref(false)
const newSubTaskText = ref('')
const addingSubTask = ref(false)

async function addSubTask() {
  if (!entry.value || !newSubTaskText.value.trim() || addingSubTask.value) return
  addingSubTask.value = true
  try {
    await api.createSubTask(entry.value.id, newSubTaskText.value.trim())
    newSubTaskText.value = ''
    await load()
  } catch {
    showToast('Failed to add sub-task')
  } finally {
    addingSubTask.value = false
  }
}

async function toggleSubTask(st: SubTask) {
  if (!entry.value) return
  try {
    await api.updateSubTask(entry.value.id, st.id, { done: !st.done })
    await load()
  } catch {
    showToast('Failed to update sub-task')
  }
}

async function deleteSubTask(st: SubTask) {
  if (!entry.value) return
  try {
    await api.deleteSubTask(entry.value.id, st.id)
    await load()
  } catch {
    showToast('Failed to delete sub-task')
  }
}

// Session messages / iterative turns
const replyText = ref('')
const replying = ref(false)
const replyTextarea = ref<HTMLTextAreaElement | null>(null)
const { resize: resizeTextarea } = useAutoExpand(replyTextarea, 300)

// File viewer state
const { filePanelOpen } = useFilePanel()
const fileViewerOpen = ref(false)
const fileViewerPath = ref('')

function openFileViewer(path: string) {
  fileViewerPath.value = path
  fileViewerOpen.value = true
}

watch(fileViewerOpen, (v) => { filePanelOpen.value = v })

function handleMessageClick(e: MouseEvent) {
  const target = e.target as HTMLElement
  if (target.classList.contains('file-link') && target.dataset.filePath) {
    e.preventDefault()
    openFileViewer(target.dataset.filePath)
  }
}

const hasConversation = computed(() => messages.value.length > 0)

async function sendReply() {
  if (!entry.value || !replyText.value.trim() || replying.value) return
  replying.value = true
  try {
    await api.reply(entry.value.id, replyText.value.trim())
    replyText.value = ''
    showToast('Reply sent')
    await load()
  } catch {
    showToast('Failed to send reply')
  } finally {
    replying.value = false
  }
}

async function markComplete() {
  if (!entry.value) return
  try {
    await api.markComplete(entry.value.id)
    showToast('Marked complete')
    await load()
  } catch {
    showToast('Failed to mark complete')
  }
}

async function undoComplete() {
  if (!entry.value) return
  try {
    await api.updateEntry(entry.value.id, { maturity: 'verified', route_status: 'your_turn' } as any)
    showToast('Reverted to verified')
    await load()
  } catch {
    showToast('Failed to undo complete')
  }
}

async function dismissRoute() {
  if (!entry.value) return
  try {
    await api.dismissRoute(entry.value.id)
    showToast('Dismissed')
    await load()
  } catch {
    showToast('Failed to dismiss')
  }
}

// Pipeline gate state
const scenarioText = ref('')
const advancingPipeline = ref(false)
const cancellingExecution = ref(false)
const verifyScenarios = ref<{ scenario: string; passed: boolean; notes: string }[]>([])
const verifySubmitting = ref(false)
const executionTools = ref<{ tool: string; detail: string }[]>([])
const executionStartedAt = ref<number | null>(null)
const executionElapsed = ref('')
let elapsedInterval: ReturnType<typeof setInterval> | null = null

function startElapsedTimer() {
  stopElapsedTimer()
  elapsedInterval = setInterval(() => {
    if (!executionStartedAt.value) return
    const secs = Math.floor((Date.now() - executionStartedAt.value) / 1000)
    const m = Math.floor(secs / 60)
    const s = secs % 60
    executionElapsed.value = m > 0 ? `${m}m ${s.toString().padStart(2, '0')}s` : `${s}s`
  }, 1000)
}

function stopElapsedTimer() {
  if (elapsedInterval) { clearInterval(elapsedInterval); elapsedInterval = null }
  executionElapsed.value = ''
  executionStartedAt.value = null
}

const maturityLabel: Record<string, string> = {
  raw: 'Raw', researched: 'Researched', planned: 'Planned',
  specced: 'Specced', executing: 'Executing', verified: 'Verified', complete: 'Complete',
}

function maturityColor(stage: string): string {
  switch (stage) {
    case 'raw': return 'bg-gray-700 text-gray-300'
    case 'researched': return 'bg-blue-900 text-blue-300'
    case 'planned': return 'bg-purple-900 text-purple-300'
    case 'specced': return 'bg-indigo-900 text-indigo-300'
    case 'executing': return 'bg-amber-900 text-amber-300'
    case 'verified': return 'bg-green-900 text-green-300'
    case 'complete': return 'bg-green-900 text-green-300'
    default: return 'bg-gray-800 text-gray-400'
  }
}

async function advancePipeline() {
  if (!entry.value || advancingPipeline.value) return

  // If planned, need scenarios
  if (entry.value.maturity === 'planned') {
    const scenarios = scenarioText.value.split('\n').map(s => s.replace(/^[-*]\s*/, '').trim()).filter(Boolean)
    if (scenarios.length === 0) {
      showToast('Add at least one scenario')
      return
    }
    advancingPipeline.value = true
    try {
      await api.pipelineAdvance(entry.value.id, 'advance', undefined, scenarios)
      scenarioText.value = ''
      showToast('Advanced to specced')
      await load()
    } catch (e: any) {
      showToast(e.message || 'Advance failed')
    } finally {
      advancingPipeline.value = false
    }
    return
  }

  // Normal advance
  advancingPipeline.value = true
  try {
    await api.pipelineAdvance(entry.value.id, 'advance')
    showToast('Advanced')
    await load()
  } catch (e: any) {
    showToast(e.message || 'Advance failed')
  } finally {
    advancingPipeline.value = false
  }
}

async function executeEntry() {
  if (!entry.value) return
  advancingPipeline.value = true
  try {
    await api.executeEntry(entry.value.id)
    showToast('Execution started')
    await load()
  } catch (e: any) {
    showToast(e.message || 'Execute failed')
  } finally {
    advancingPipeline.value = false
  }
}

async function cancelExecution() {
  if (!entry.value || cancellingExecution.value) return
  cancellingExecution.value = true
  try {
    await api.cancelExecution(entry.value.id)
    showToast('Execution cancelled')
    await load()
  } catch (e: any) {
    showToast(e.message || 'Cancel failed')
  } finally {
    cancellingExecution.value = false
  }
}

async function loadVerifyScenarios() {
  if (!entry.value) return
  try {
    const ctx = await api.executionContext(entry.value.id)
    verifyScenarios.value = (ctx.scenarios || []).map(s => ({ scenario: s, passed: false, notes: '' }))
  } catch {
    verifyScenarios.value = []
  }
}

async function submitVerification() {
  if (!entry.value || verifyScenarios.value.length === 0 || verifySubmitting.value) return
  verifySubmitting.value = true
  try {
    await api.verifyEntry(entry.value.id, verifyScenarios.value)
    showToast('Verification submitted')
    verifyScenarios.value = []
    await load()
  } catch (e: any) {
    showToast(e.message || 'Verification failed')
  } finally {
    verifySubmitting.value = false
  }
}

onMounted(load)
onUnmounted(() => { filePanelOpen.value = false; stopElapsedTimer() })

// Live updates via WebSocket
const { subscribe } = useWebSocket()
const currentId = computed(() => route.params.id as string)

subscribe('message.new', (evt) => {
  if (evt.entry_id === currentId.value) {
    // Refresh messages when a new one arrives
    api.listMessages(currentId.value).then(m => { messages.value = m })
  }
})

subscribe('entry.updated', (evt) => {
  if (evt.entry_id === currentId.value) {
    // Refresh the entry when it's updated
    api.getEntry(currentId.value).then(e => {
      entry.value = e
      // Clear tool log and elapsed timer when execution finishes
      if (e.maturity !== 'executing' || e.route_status !== 'agent') {
        executionTools.value = []
        stopElapsedTimer()
      }
    })
  }
})

subscribe('execution.tool', (evt) => {
  if (evt.entry_id === currentId.value && evt.data?.tool) {
    executionTools.value.push({ tool: evt.data.tool, detail: evt.data.detail || '' })
    // Keep only last 20 to avoid unbounded growth
    if (executionTools.value.length > 20) {
      executionTools.value = executionTools.value.slice(-20)
    }
  }
})

subscribe('execution.started', (evt) => {
  if (evt.entry_id === currentId.value) {
    const ts = evt.data?.started_at
    executionStartedAt.value = ts ? new Date(ts).getTime() : Date.now()
    startElapsedTimer()
  }
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

    <button @click="router.back()" class="text-sm text-gray-500 hover:text-gray-300 mb-4">&larr; Back</button>

    <div v-if="loading" class="text-center py-8 text-gray-500">Loading...</div>

    <div v-else-if="!entry" class="text-center py-12 text-gray-600">Entry not found.</div>

    <div v-else class="space-y-6">
      <!-- Header -->
      <div class="flex items-start justify-between gap-4">
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <!-- Done toggle for actionable entries -->
            <button
              v-if="isActionable"
              @click="toggleDone"
              class="shrink-0 w-6 h-6 rounded-full border-2 flex items-center justify-center transition-colors"
              :class="isDone ? 'bg-emerald-500 border-emerald-500 text-white' : 'border-gray-600 hover:border-sky-500'"
              :title="isDone ? 'Reopen' : 'Mark done'"
            >
              <span v-if="isDone" class="text-xs">✓</span>
            </button>
            <h1 class="text-xl font-bold" :class="{ 'line-through text-gray-500': isDone }">{{ entry.title }}</h1>
          </div>
          <div class="flex items-center gap-2 mt-1 text-sm text-gray-500 flex-wrap">
            <span class="px-2 py-0.5 rounded-full bg-gray-800 text-sky-400 text-xs">{{ entry.category }}</span>
            <span v-if="entry.maturity" :class="['px-2 py-0.5 rounded-full text-xs', maturityColor(entry.maturity)]">{{ maturityLabel[entry.maturity] || entry.maturity }}</span>
            <!-- Status pill: click to change -->
            <div class="relative inline-block">
              <button
                @click="statusMenuOpen = !statusMenuOpen"
                :disabled="changingStatus"
                class="px-2 py-0.5 rounded-full text-xs flex items-center gap-1 transition-colors"
                :class="entry.status
                  ? (entry.status === 'someday' || entry.status === 'archived'
                      ? 'bg-gray-800 text-gray-400 hover:bg-gray-700'
                      : 'bg-gray-800 text-amber-400 hover:bg-gray-700')
                  : 'bg-gray-800/50 text-gray-600 hover:text-gray-400 border border-dashed border-gray-700'"
                :title="entry.status ? `Status: ${entry.status} — click to change` : 'Set status'"
              >
                <span>{{ entry.status || '+ status' }}</span>
                <span class="text-[10px] opacity-60">▾</span>
              </button>
              <!-- Backdrop closes menu on outside click -->
              <div
                v-if="statusMenuOpen"
                @click="statusMenuOpen = false"
                class="fixed inset-0 z-10"
              ></div>
              <div
                v-if="statusMenuOpen"
                class="absolute z-20 mt-1 left-0 bg-gray-900 border border-gray-700 rounded-lg shadow-xl py-1 min-w-[140px]"
              >
                <button
                  v-for="opt in STATUS_OPTIONS"
                  :key="opt.value"
                  @click="changeStatus(opt.value)"
                  class="w-full text-left px-3 py-1.5 text-xs hover:bg-gray-800 transition-colors flex items-center justify-between"
                  :class="(entry.status || '') === opt.value ? 'text-sky-400' : 'text-gray-300'"
                >
                  <span>{{ opt.label }}</span>
                  <span v-if="(entry.status || '') === opt.value" class="text-sky-400">✓</span>
                </button>
              </div>
            </div>
            <RouterLink
              v-if="entry.project_id"
              :to="`/projects/${entry.project_id}`"
              class="px-2 py-0.5 rounded-full bg-indigo-900 text-indigo-300 text-xs hover:bg-indigo-800 transition-colors"
            >{{ projects.find(p => p.id === entry!.project_id)?.emoji }} {{ projects.find(p => p.id === entry!.project_id)?.name || 'Project' }}</RouterLink>
            <span v-if="entry.due_date" class="text-xs">📅 {{ entry.due_date }}</span>
            <span>{{ new Date(entry.created_at).toLocaleString() }}</span>
            <span>· {{ entry.source }}</span>
            <span>· {{ Math.round(entry.confidence * 100) }}%</span>
            <span v-if="entry.premium_requests_used" class="text-xs text-emerald-400" title="Premium requests consumed by pipeline agents">🎟️ {{ entry.premium_requests_used.toFixed(2) }}</span>
            <span v-if="entry.needs_review" class="text-amber-400">⚠ Needs review</span>
            <span v-if="entry.failure_count" class="text-xs text-red-400" :title="entry.last_failure_reason || 'Pipeline failure'">
              🔴 {{ entry.failure_count }} failure{{ entry.failure_count === 1 ? '' : 's' }}
            </span>
            <span v-if="entry.nudge_count" class="text-xs text-gray-500" :title="`Nudged ${entry.nudge_count} time${entry.nudge_count === 1 ? '' : 's'} by review bot`">
              🔔 {{ entry.nudge_count }}
            </span>
            <label v-if="entry.maturity && !entry.notebook" class="inline-flex items-center gap-1.5 text-xs cursor-pointer select-none" :title="entry.auto_continue ? 'Delegation mode — stages advance automatically' : 'Sabbath mode — pause for review after each stage'">
              <input type="checkbox" :checked="entry.auto_continue" :disabled="togglingAutoContinue" @change="toggleAutoContinue" class="accent-violet-500 w-3.5 h-3.5">
              <span :class="entry.auto_continue ? 'text-violet-400' : 'text-gray-500'">{{ entry.auto_continue ? '⚡ Auto' : '🕊️ Sabbath' }}</span>
            </label>
            <div class="inline-flex bg-gray-800 rounded-lg overflow-hidden text-xs select-none" :title="entry.notebook ? 'Notebook mode — searchable but outside pipeline' : 'Pipeline mode — enters research/execute workflow'">
              <button
                @click="entry.notebook && toggleNotebook()"
                :disabled="togglingNotebook"
                :class="['px-2.5 py-1 transition-colors', !entry.notebook ? 'bg-sky-600 text-white' : 'text-gray-500 hover:text-gray-300']"
              >🔄 Pipeline</button>
              <button
                @click="!entry.notebook && toggleNotebook()"
                :disabled="togglingNotebook"
                :class="['px-2.5 py-1 transition-colors', entry.notebook ? 'bg-amber-600 text-white' : 'text-gray-500 hover:text-gray-300']"
              >📓 Notebook</button>
            </div>
          </div>
        </div>
        <div class="flex gap-2 shrink-0">
          <button
            v-if="!editing"
            @click="classify"
            :disabled="classifying"
            class="text-sm bg-violet-600 text-white px-3 py-1.5 rounded-lg hover:bg-violet-500 disabled:opacity-40"
          >
            {{ classifying ? 'Classifying...' : '✦ Classify' }}
          </button>
          <button
            v-if="!editing"
            @click="startEdit"
            class="text-sm bg-gray-800 text-gray-300 px-3 py-1.5 rounded-lg hover:bg-gray-700"
          >
            Edit
          </button>
          <button
            @click="deleteEntry"
            class="text-sm text-red-400 hover:text-red-300 px-3 py-1.5"
          >
            Delete
          </button>
        </div>
      </div>

      <!-- View mode -->
      <div v-if="!editing">
        <div class="bg-gray-900 border border-gray-800 rounded-lg p-4 whitespace-pre-wrap text-sm">{{ entry.body }}</div>
        <div v-if="entry.tags?.length" class="flex gap-1.5 mt-3 flex-wrap">
          <span
            v-for="tag in entry.tags"
            :key="tag"
            class="text-xs px-2 py-0.5 rounded-full border border-gray-700 text-gray-400"
          >
            {{ tag }}
          </span>
        </div>

        <!-- Commission status -->
        <div v-if="hasActiveCommission && commission" class="mt-4 bg-gray-900 border border-amber-800 rounded-lg p-4 space-y-3">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-medium text-amber-400">📜 Commission {{ commission.status === 'paused' ? '(Paused)' : 'Active' }}</h3>
            <span
              :class="['text-xs px-2 py-0.5 rounded-full', commission.status === 'active' ? 'bg-green-900 text-green-300' : 'bg-amber-900 text-amber-300']"
            >{{ commission.status }}</span>
          </div>
          <p class="text-sm text-gray-300">{{ commission.intent }}</p>
          <div class="flex items-center gap-4 text-xs text-gray-500">
            <span>Authority: <span class="text-gray-300">{{ commission.authority === 'advance_and_execute' ? 'Advance & Execute' : 'Advance Only' }}</span></span>
            <span>Model: <span class="text-gray-300 font-mono">{{ commission.model }}</span></span>
            <span>Budget: <span class="text-gray-300">{{ commission.cost_used.toFixed(1) }} / {{ commission.max_cost }}</span></span>
          </div>
          <!-- Cost breakdown by type -->
          <div v-if="commission.decisions?.length" class="grid grid-cols-3 gap-2 text-xs">
            <div v-for="ct in costBreakdown" :key="ct.type" class="bg-gray-800 rounded px-2 py-1.5">
              <span class="text-gray-500 capitalize">{{ ct.type }}</span>
              <span class="ml-1 text-gray-300 font-mono">{{ ct.cost.toFixed(1) }}</span>
              <span class="text-gray-600 ml-0.5">({{ ct.count }})</span>
            </div>
          </div>
          <div class="flex gap-2">
            <button
              v-if="commission.status === 'active'"
              @click="pauseCommission"
              :disabled="commissionPausing"
              class="px-3 py-1.5 text-xs bg-amber-900/50 text-amber-300 rounded hover:bg-amber-800 transition-colors disabled:opacity-40"
            >{{ commissionPausing ? 'Pausing...' : '⏸ Pause' }}</button>
            <button
              v-if="commission.status === 'paused'"
              @click="openResumeDialog"
              :disabled="commissionResuming"
              class="px-3 py-1.5 text-xs bg-green-900/50 text-green-300 rounded hover:bg-green-800 transition-colors disabled:opacity-40"
            >{{ commissionResuming ? 'Resuming...' : '▶ Resume' }}</button>
            <button
              @click="revokeCommission"
              :disabled="commissionRevoking"
              class="px-3 py-1.5 text-xs bg-red-900/50 text-red-300 rounded hover:bg-red-800 transition-colors disabled:opacity-40"
            >{{ commissionRevoking ? 'Revoking...' : '⏹ Revoke' }}</button>
          </div>
          <!-- Decision log -->
          <div v-if="commission.decisions?.length" class="space-y-1">
            <h4 class="text-xs text-gray-500 font-medium">Decision Log</h4>
            <div v-for="d in commission.decisions" :key="d.id" class="text-xs text-gray-400 font-mono flex items-start gap-2">
              <span class="text-gray-600 shrink-0">{{ d.stage }}</span>
              <span class="text-gray-600">→</span>
              <span :class="d.action === 'advance' || d.action === 'execute' ? 'text-green-400' : d.action === 'surface' ? 'text-amber-400' : 'text-gray-400'">{{ d.action }}</span>
              <span class="text-gray-600">({{ d.cost.toFixed(2) }})</span>
              <span v-if="d.model" class="text-gray-700 shrink-0">{{ d.model.split('/').pop() }}</span>
              <span class="text-gray-500 truncate" :title="d.reasoning">{{ d.reasoning }}</span>
            </div>
          </div>
        </div>

        <!-- Resume dialog -->
        <ResumeDialog
          :show="showResumeDialog"
          :surfaceReason="resumeSurfaceReason"
          @resume="onResumeWithFeedback"
          @cancel="showResumeDialog = false"
        />

        <!-- Pipeline gates (hidden when commission is active) -->
        <div v-if="entry.maturity && !entry.notebook && !hasActiveCommission" class="mt-4 space-y-3">

          <!-- Commission button -->
          <button
            v-if="canCommission()"
            @click="openCommissionDialog"
            class="px-3 py-1.5 text-sm bg-amber-900/50 text-amber-300 border border-amber-800 rounded-lg hover:bg-amber-800 transition-colors"
          >📜 Commission Steward</button>

          <!-- Scenario input (planned → specced) -->
          <div v-if="entry.maturity === 'planned'" class="bg-gray-900 border border-indigo-800 rounded-lg p-4">
            <h3 class="text-sm font-medium text-indigo-400 mb-2">📋 Define Scenarios</h3>
            <p class="text-xs text-gray-400 mb-2">Define acceptance criteria — one per line. These are how you'll verify the work is done.</p>
            <textarea
              v-model="scenarioText"
              placeholder="- User can see the clock display&#10;- Calculator handles basic operations&#10;- Theme matches LCARS color palette"
              rows="4"
              class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-indigo-500 resize-y"
            />
            <button
              @click="advancePipeline"
              :disabled="!scenarioText.trim() || advancingPipeline"
              class="mt-2 px-4 py-2 text-sm bg-indigo-600 text-white rounded-lg hover:bg-indigo-500 transition-colors disabled:opacity-40"
            >{{ advancingPipeline ? 'Advancing...' : 'Advance to Specced' }}</button>
          </div>

          <!-- Execute button (specced) -->
          <div v-if="entry.maturity === 'specced'" class="bg-gray-900 border border-green-800 rounded-lg p-4">
            <h3 class="text-sm font-medium text-green-400 mb-2">▶ Ready to Execute</h3>
            <p class="text-xs text-gray-400 mb-2">Entry is specced and ready for agent execution.</p>
            <button
              @click="executeEntry"
              :disabled="advancingPipeline"
              class="px-4 py-2 text-sm bg-green-600 text-white rounded-lg hover:bg-green-500 transition-colors disabled:opacity-40"
            >{{ advancingPipeline ? 'Starting...' : '▶ Execute' }}</button>
          </div>

          <!-- Executing status with cancel -->
          <div v-if="entry.maturity === 'executing' && entry.route_status === 'agent'" class="bg-gray-900 border border-amber-800 rounded-lg p-4">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <span class="inline-block w-2 h-2 bg-amber-400 rounded-full animate-pulse" />
                <span class="text-sm text-amber-300">Agent is executing...</span>
                <span v-if="executionElapsed" class="text-xs text-gray-500 font-mono">({{ executionElapsed }})</span>
              </div>
              <button
                @click="cancelExecution"
                :disabled="cancellingExecution"
                class="px-3 py-1.5 text-sm bg-red-600 text-white rounded-lg hover:bg-red-500 transition-colors disabled:opacity-40"
              >{{ cancellingExecution ? 'Cancelling...' : '✕ Cancel' }}</button>
            </div>
            <!-- Tool call progress log -->
            <div v-if="executionTools.length > 0" class="mt-3 space-y-1 max-h-40 overflow-y-auto">
              <div v-for="(t, i) in executionTools" :key="i" class="text-xs text-gray-500 font-mono flex items-center gap-1.5">
                <span class="text-gray-700">{{ i + 1 }}.</span>
                <span>{{ t.tool }}</span>
                <span v-if="t.detail" class="text-gray-600 truncate max-w-md" :title="t.detail">{{ t.detail }}</span>
              </div>
            </div>
          </div>

          <!-- Verify scenarios (executing + your_turn) -->
          <div v-if="entry.maturity === 'executing' && entry.route_status === 'your_turn'" class="bg-gray-900 border border-emerald-800 rounded-lg p-4">
            <h3 class="text-sm font-medium text-emerald-400 mb-2">✓ Verify Scenarios</h3>
            <p class="text-xs text-gray-400 mb-3">Check each scenario that passes. Failed scenarios will return the entry to planned.</p>
            <div v-if="verifyScenarios.length === 0" class="text-sm text-gray-500 mb-3">
              <button @click="loadVerifyScenarios" class="text-emerald-400 hover:text-emerald-300">Load scenarios</button>
            </div>
            <div v-else class="space-y-2 mb-3">
              <div
                v-for="(s, i) in verifyScenarios"
                :key="i"
                :class="['border rounded-lg px-3 py-2', s.passed ? 'border-green-800 bg-green-950/30' : 'border-gray-700']"
              >
                <label class="flex items-start gap-2 cursor-pointer">
                  <input type="checkbox" v-model="s.passed" class="mt-1 accent-green-500" />
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
            <button
              v-if="verifyScenarios.length > 0"
              @click="submitVerification"
              :disabled="verifySubmitting"
              class="px-4 py-2 text-sm rounded-lg transition-colors disabled:opacity-40"
              :class="verifyScenarios.every(s => s.passed) ? 'bg-emerald-600 text-white hover:bg-emerald-500' : 'bg-amber-600 text-white hover:bg-amber-500'"
            >{{ verifyScenarios.every(s => s.passed) ? '✓ All Pass — Verify' : 'Submit (some failed)' }}</button>
          </div>

          <!-- Advance buttons for raw/researched -->
          <div v-if="['raw', 'researched'].includes(entry.maturity)" class="flex gap-2">
            <button
              @click="advancePipeline"
              :disabled="advancingPipeline"
              class="px-3 py-1.5 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-500 transition-colors disabled:opacity-40"
            >{{ advancingPipeline ? 'Advancing...' : '▶ Advance' }}</button>
          </div>

          <!-- Mark complete (verified) -->
          <div v-if="entry.maturity === 'verified'" class="flex gap-2">
            <button
              @click="markComplete"
              class="px-3 py-1.5 text-sm bg-green-600 text-white rounded-lg hover:bg-green-500 transition-colors"
            >✓ Mark Complete</button>
          </div>

          <!-- Undo complete -->
          <div v-if="entry.maturity === 'complete'" class="flex gap-2">
            <span class="px-3 py-1.5 text-sm text-green-400">✓ Pipeline complete</span>
            <button
              @click="undoComplete"
              class="px-3 py-1.5 text-sm text-gray-400 border border-gray-700 rounded-lg hover:bg-gray-800 transition-colors"
            >↩ Undo</button>
          </div>
        </div>

        <!-- Subtasks -->
        <div class="mt-4">
          <button
            @click="showSubTasks = !showSubTasks"
            class="text-xs text-gray-500 hover:text-gray-300 flex items-center gap-1"
          >
            <span>{{ showSubTasks ? '▾' : '▸' }}</span>
            Sub-tasks
            <span v-if="entry.subtasks?.length" class="text-gray-600">({{ entry.subtasks.filter(s => s.done).length }}/{{ entry.subtasks.length }})</span>
            <span v-else class="text-gray-600">(0)</span>
          </button>
          <div v-if="showSubTasks" class="mt-2 space-y-1">
            <div v-for="st in entry.subtasks" :key="st.id" class="flex items-center gap-2 group">
              <button
                @click="toggleSubTask(st)"
                class="w-5 h-5 rounded border flex items-center justify-center flex-shrink-0 transition-colors"
                :class="st.done ? 'bg-emerald-500 border-emerald-500 text-white' : 'border-gray-600 hover:border-sky-500'"
              >
                <span v-if="st.done" class="text-[10px]">✓</span>
              </button>
              <span class="text-sm flex-1" :class="st.done ? 'line-through text-gray-600' : 'text-gray-300'">{{ st.text }}</span>
              <button
                @click="deleteSubTask(st)"
                class="text-xs text-red-500 opacity-0 group-hover:opacity-100 transition-opacity"
              >✕</button>
            </div>
            <!-- Add sub-task -->
            <div class="flex gap-2 mt-2">
              <input
                v-model="newSubTaskText"
                @keydown.enter="addSubTask"
                placeholder="Add item..."
                class="flex-1 bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm text-gray-300 placeholder-gray-600 focus:outline-none focus:border-sky-500"
              />
              <button
                @click="addSubTask"
                :disabled="!newSubTaskText.trim() || addingSubTask"
                class="text-sm text-sky-400 hover:text-sky-300 disabled:opacity-40 px-2"
              >+</button>
            </div>
          </div>
        </div>

        <!-- Quick reclassify -->
        <div class="mt-6">
          <p class="text-xs text-gray-600 mb-2">Reclassify:</p>
          <div class="flex gap-2 flex-wrap">
            <button
              v-for="cat in categories.filter(c => c !== entry!.category)"
              :key="cat"
              @click="reclassify(cat)"
              class="text-xs px-2.5 py-1 rounded border border-gray-700 text-gray-500 hover:border-sky-600 hover:text-sky-400 transition-colors"
            >
              {{ cat }}
            </button>
          </div>
        </div>

        <!-- Agent conversation / session messages -->
        <div v-if="hasConversation || entry.agent_output" class="mt-6">
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-sm font-medium text-gray-500 uppercase tracking-wider">Conversation</h3>
            <div v-if="entry.route_status && entry.route_status !== 'complete' && !entry.notebook" class="flex gap-2">
              <span
                :class="[
                  'px-2 py-0.5 text-xs rounded-full font-medium',
                  entry.route_status === 'your_turn' && entry.agent_route === 'review' ? 'bg-purple-900 text-purple-300' :
                  entry.route_status === 'your_turn' ? 'bg-amber-900 text-amber-300' :
                  entry.route_status === 'running' ? 'bg-blue-900 text-blue-300 animate-pulse' :
                  'bg-gray-800 text-gray-400'
                ]"
              >{{ entry.route_status === 'your_turn' && entry.agent_route === 'review' ? '🤖 Review' : entry.route_status === 'your_turn' ? 'Your Turn' : entry.route_status === 'running' ? 'Agent Working' : entry.route_status }}</span>
              <button
                v-if="entry.route_status === 'your_turn'"
                @click="dismissRoute"
                class="px-2 py-0.5 text-xs text-gray-400 border border-gray-700 rounded-full hover:bg-gray-800 transition-colors"
              >✓ Dismiss</button>
            </div>
          </div>

          <!-- Legacy agent output (if no messages yet) -->
          <div v-if="entry.agent_output && messages.length === 0" class="bg-gray-900 border border-gray-800 rounded-lg p-4 text-sm whitespace-pre-wrap text-gray-300">
            {{ entry.agent_output }}
          </div>

          <!-- Message thread -->
          <div v-if="messages.length > 0" class="space-y-3">
            <div
              v-for="msg in messages"
              :key="msg.id"
              :class="[
                'rounded-lg px-4 py-3 text-sm',
                msg.role === 'human'
                  ? 'bg-sky-950 border border-sky-900 ml-8'
                  : 'bg-gray-900 border border-gray-800 mr-8'
              ]"
            >
              <div class="flex items-center justify-between mb-1">
                <span class="text-xs font-medium" :class="msg.role === 'human' ? 'text-sky-400' : 'text-purple-400'">
                  {{ msg.role === 'human' ? 'You' : 'Agent' }}
                </span>
                <span class="text-xs text-gray-600">{{ new Date(msg.created_at).toLocaleString() }}</span>
              </div>
              <div
                class="prose prose-invert prose-sm max-w-none text-gray-300"
                v-html="renderMarkdown(msg.content)"
                @click="handleMessageClick"
              />
            </div>
          </div>

          <!-- Reply input -->
          <div v-if="entry.route_status && entry.route_status !== 'complete' && !entry.notebook" class="mt-3">
            <div class="flex gap-2">
              <textarea
                ref="replyTextarea"
                v-model="replyText"
                @input="resizeTextarea"
                @keydown.ctrl.enter="sendReply"
                placeholder="Reply with feedback..."
                rows="2"
                class="flex-1 bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-sky-500"
                style="overflow-y: hidden"
              />
              <button
                @click="sendReply"
                :disabled="!replyText.trim() || replying"
                class="px-4 py-2 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-500 disabled:opacity-40 self-end"
              >Reply</button>
            </div>
            <p class="text-xs text-gray-600 mt-1">Ctrl+Enter to send</p>
          </div>
        </div>

        <!-- Agent Context (project-aware) -->
        <div v-if="entry.project_id && agentContext" class="mt-6">
          <button @click="showAgentContext = !showAgentContext" class="text-sm font-medium text-gray-500 uppercase tracking-wider hover:text-gray-300 transition-colors flex items-center gap-1">
            <span class="text-xs">{{ showAgentContext ? '▼' : '▶' }}</span>
            Agent Context
            <span class="text-xs text-gray-600 normal-case font-normal ml-1">(what the agent sees)</span>
          </button>
          <div v-if="showAgentContext" class="mt-2 bg-gray-950 border border-gray-800 rounded-lg p-4 text-sm text-gray-400 whitespace-pre-wrap font-mono">{{ agentContext }}</div>
        </div>
      </div>

      <!-- Edit mode -->
      <div v-else class="space-y-4">
        <div>
          <label class="block text-xs text-gray-500 mb-1">Title</label>
          <input
            v-model="editForm.title"
            class="w-full bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
          />
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Category</label>
          <select
            v-model="editForm.category"
            class="bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
          >
            <option v-for="cat in categories" :key="cat" :value="cat">{{ cat }}</option>
          </select>
        </div>
        <div class="flex gap-4">
          <div v-if="editForm.category === 'projects' || editForm.category === 'actions'" class="flex-1">
            <label class="block text-xs text-gray-500 mb-1">Status</label>
            <input
              v-model="editForm.status"
              placeholder="e.g. active, blocked, waiting, done"
              class="w-full bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
            />
          </div>
          <div v-if="editForm.category === 'actions'" class="flex-1">
            <label class="block text-xs text-gray-500 mb-1">Due Date</label>
            <input
              v-model="editForm.due_date"
              type="date"
              class="w-full bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
            />
          </div>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Project</label>
          <select
            v-model="editForm.project_id"
            class="bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
          >
            <option :value="null">No project</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.emoji ? p.emoji + ' ' : '' }}{{ p.name }}</option>
          </select>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Body</label>
          <textarea
            v-model="editForm.body"
            rows="8"
            class="w-full bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500 resize-y"
          ></textarea>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Tags (comma-separated)</label>
          <input
            v-model="editForm.tags"
            class="w-full bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
          />
        </div>
        <div class="flex gap-2">
          <button
            @click="save"
            :disabled="saving"
            class="bg-sky-500 text-gray-950 font-semibold px-4 py-2 rounded-lg hover:bg-sky-400 disabled:opacity-40"
          >
            {{ saving ? 'Saving...' : 'Save' }}
          </button>
          <button
            @click="editing = false"
            class="text-sm text-gray-500 hover:text-gray-300 px-3 py-2"
          >
            Cancel
          </button>
        </div>
      </div>

      <!-- File Viewer Modal -->
      <FileViewer
        :open="fileViewerOpen"
        :file-path="fileViewerPath"
        @close="fileViewerOpen = false"
      />

      <!-- Commission Dialog -->
      <CommissionDialog
        v-if="entry"
        :open="commissionDialog"
        :entry-id="entry.id"
        :entry-title="entry.title"
        @close="commissionDialog = false"
        @commissioned="onCommissioned"
      />
    </div>
  </div>
</template>
