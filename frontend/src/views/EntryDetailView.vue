<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Entry } from '../api'

const route = useRoute()
const router = useRouter()
const entry = ref<Entry | null>(null)
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)

const editForm = ref({
  category: '',
  body: '',
  tags: '',
})

const categories = ['people', 'projects', 'ideas', 'actions', 'study', 'journal', 'inbox']

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
    category: entry.value.category,
    body: entry.value.body,
    tags: (entry.value.tags || []).join(', '),
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
      category: editForm.value.category,
      body: editForm.value.body,
      tags,
    })
    editing.value = false
    await load()
  } finally {
    saving.value = false
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
  router.push(`/entries/${result.id}`)
}

onMounted(load)
</script>

<template>
  <div>
    <button @click="router.back()" class="text-sm text-gray-500 hover:text-gray-300 mb-4">&larr; Back</button>

    <div v-if="loading" class="text-center py-8 text-gray-500">Loading...</div>

    <div v-else-if="!entry" class="text-center py-12 text-gray-600">Entry not found.</div>

    <div v-else class="space-y-6">
      <!-- Header -->
      <div class="flex items-start justify-between gap-4">
        <div>
          <h1 class="text-xl font-bold">{{ entry.title }}</h1>
          <div class="flex items-center gap-2 mt-1 text-sm text-gray-500">
            <span class="px-2 py-0.5 rounded-full bg-gray-800 text-sky-400 text-xs">{{ entry.category }}</span>
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
          <label class="block text-xs text-gray-500 mb-1">Category</label>
          <select
            v-model="editForm.category"
            class="bg-gray-900 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
          >
            <option v-for="cat in categories" :key="cat" :value="cat">{{ cat }}</option>
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
    </div>
  </div>
</template>
