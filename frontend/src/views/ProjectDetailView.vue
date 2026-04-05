<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Project, type Entry } from '../api'

const route = useRoute()
const router = useRouter()
const project = ref<Project | null>(null)
const entries = ref<Entry[]>([])
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const confirmDelete = ref(false)

const editForm = ref({ name: '', description: '', emoji: '', status: '' })

const maturityStages = ['raw', 'researched', 'planned', 'specced', 'executing', 'verified'] as const

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

function statusColor(status: string) {
  switch (status) {
    case 'active': return 'bg-green-900 text-green-300'
    case 'paused': return 'bg-yellow-900 text-yellow-300'
    case 'archived': return 'bg-gray-800 text-gray-500'
    default: return 'bg-gray-800 text-gray-400'
  }
}

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
  await load()
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
        <div class="flex gap-2">
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

      <!-- Entries by maturity -->
      <div v-if="entries.length === 0" class="text-center py-8">
        <div class="text-gray-600 mb-1">No entries in this project</div>
        <div class="text-sm text-gray-500">Assign entries from the Entries view.</div>
      </div>

      <div v-else class="space-y-6">
        <div v-for="stage in nonEmptyStages" :key="stage">
          <div class="flex items-center gap-2 mb-2">
            <span :class="['px-2 py-0.5 text-xs rounded-full font-medium', maturityColor(stage)]">
              {{ stage === 'unset' ? 'No maturity' : stage }}
            </span>
            <span class="text-xs text-gray-600">{{ entriesByMaturity[stage]?.length ?? 0 }}</span>
          </div>
          <div class="space-y-2">
            <div
              v-for="entry in entriesByMaturity[stage]"
              :key="entry.id"
              class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 hover:border-gray-700 transition-colors flex items-start justify-between"
            >
              <RouterLink :to="`/entries/${entry.id}`" class="flex-1 min-w-0">
                <div class="font-medium text-gray-200 text-sm truncate">{{ entry.title }}</div>
                <p v-if="entry.body" class="text-xs text-gray-500 mt-1 line-clamp-2">{{ entry.body.slice(0, 200) }}</p>
                <div class="flex items-center gap-2 mt-2">
                  <span class="text-xs text-gray-600 bg-gray-800 px-1.5 py-0.5 rounded">{{ entry.category }}</span>
                  <span v-if="entry.route_status" class="text-xs text-gray-600">{{ entry.route_status }}</span>
                </div>
              </RouterLink>
              <button
                @click.prevent="removeEntry(entry.id)"
                class="ml-2 text-xs text-gray-600 hover:text-red-400 shrink-0"
                title="Remove from project"
              >✕</button>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
