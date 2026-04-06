<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Entry, type SubTask, type Project, type SessionMessage } from '../api'
import { useAutoExpand } from '../composables/useAutoExpand'
import { renderMarkdown } from '../composables/useMarkdown'
import FileViewer from '../components/FileViewer.vue'

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
  } finally {
    loading.value = false
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
const fileViewerOpen = ref(false)
const fileViewerPath = ref('')

function openFileViewer(path: string) {
  fileViewerPath.value = path
  fileViewerOpen.value = true
}

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

onMounted(load)
</script>

<template>
  <div class="relative transition-[margin] duration-200" :style="fileViewerOpen ? 'margin-right: 45vw' : ''">
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
              :title="isDone ? 'Mark incomplete' : 'Mark complete'"
            >
              <span v-if="isDone" class="text-xs">✓</span>
            </button>
            <h1 class="text-xl font-bold" :class="{ 'line-through text-gray-500': isDone }">{{ entry.title }}</h1>
          </div>
          <div class="flex items-center gap-2 mt-1 text-sm text-gray-500 flex-wrap">
            <span class="px-2 py-0.5 rounded-full bg-gray-800 text-sky-400 text-xs">{{ entry.category }}</span>
            <span v-if="entry.status" class="px-2 py-0.5 rounded-full bg-gray-800 text-amber-400 text-xs">{{ entry.status }}</span>
            <RouterLink
              v-if="entry.project_id"
              :to="`/projects/${entry.project_id}`"
              class="px-2 py-0.5 rounded-full bg-indigo-900 text-indigo-300 text-xs hover:bg-indigo-800 transition-colors"
            >{{ projects.find(p => p.id === entry!.project_id)?.emoji }} {{ projects.find(p => p.id === entry!.project_id)?.name || 'Project' }}</RouterLink>
            <span v-if="entry.due_date" class="text-xs">📅 {{ entry.due_date }}</span>
            <span>{{ new Date(entry.created_at).toLocaleString() }}</span>
            <span>· {{ entry.source }}</span>
            <span>· {{ Math.round(entry.confidence * 100) }}%</span>
            <span v-if="entry.needs_review" class="text-amber-400">⚠ Needs review</span>
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
            <div v-if="entry.route_status && entry.route_status !== 'complete'" class="flex gap-2">
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
                v-if="entry.route_status !== 'complete'"
                @click="markComplete"
                class="px-2 py-0.5 text-xs text-green-400 border border-green-800 rounded-full hover:bg-green-900 transition-colors"
              >✓ Complete</button>
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
          <div v-if="entry.route_status && entry.route_status !== 'complete'" class="mt-3">
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
    </div>
  </div>
</template>
