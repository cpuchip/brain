<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Entry } from '../api'

const route = useRoute()
const router = useRouter()
const entry = ref<Entry | null>(null)
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const toast = ref('')
const toastTimeout = ref<ReturnType<typeof setTimeout>>()

const editForm = ref({
  title: '',
  category: '',
  body: '',
  tags: '',
  status: '',
  due_date: '',
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
    entry.value = await api.getEntry(route.params.id as string)
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

onMounted(load)
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
              :title="isDone ? 'Mark incomplete' : 'Mark complete'"
            >
              <span v-if="isDone" class="text-xs">✓</span>
            </button>
            <h1 class="text-xl font-bold" :class="{ 'line-through text-gray-500': isDone }">{{ entry.title }}</h1>
          </div>
          <div class="flex items-center gap-2 mt-1 text-sm text-gray-500 flex-wrap">
            <span class="px-2 py-0.5 rounded-full bg-gray-800 text-sky-400 text-xs">{{ entry.category }}</span>
            <span v-if="entry.status" class="px-2 py-0.5 rounded-full bg-gray-800 text-amber-400 text-xs">{{ entry.status }}</span>
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
    </div>
  </div>
</template>
