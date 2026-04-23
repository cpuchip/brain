<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Entry, type Stats, type Project } from '../api'
import { useStatusFilter } from '../composables/useStatusFilter'

const route = useRoute()
const router = useRouter()
const entries = ref<Entry[]>([])
const stats = ref<Stats | null>(null)
const projects = ref<Project[]>([])
const loading = ref(true)
const activeCategory = ref('')

// Hide someday/archived from default list. Roll off done after 30 days.
const { showParked, setShowParked, visibleEntries, hiddenCount, hiddenBreakdown } = useStatusFilter(
  entries,
  { doneRollOffDays: 30, storageKey: 'entries-show-parked' },
)

// Bulk selection
const selectMode = ref(false)
const selectedIds = ref(new Set<string>())
const bulkWorking = ref(false)

const selectedCount = computed(() => selectedIds.value.size)

function toggleSelect(id: string) {
  const s = new Set(selectedIds.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  selectedIds.value = s
}

function toggleSelectAll() {
  if (selectedIds.value.size === visibleEntries.value.length) {
    selectedIds.value = new Set()
  } else {
    selectedIds.value = new Set(visibleEntries.value.map(e => e.id))
  }
}

function exitSelectMode() {
  selectMode.value = false
  selectedIds.value = new Set()
}

async function bulkNotebook(notebook: boolean) {
  if (selectedIds.value.size === 0 || bulkWorking.value) return
  bulkWorking.value = true
  try {
    await api.bulkSetNotebook([...selectedIds.value], notebook)
    exitSelectMode()
    await loadEntries()
  } finally {
    bulkWorking.value = false
  }
}

const categories = ['people', 'projects', 'ideas', 'actions', 'study', 'journal', 'inbox']

async function loadEntries() {
  loading.value = true
  try {
    const params: { category?: string; needs_review?: boolean; unassigned?: boolean } = {}
    if (activeCategory.value === 'review') {
      params.needs_review = true
    } else if (activeCategory.value === 'unassigned') {
      params.unassigned = true
    } else if (activeCategory.value) {
      params.category = activeCategory.value
    }
    entries.value = await api.listEntries(params)
  } finally {
    loading.value = false
  }
}

function setCategory(cat: string) {
  activeCategory.value = cat
  router.replace({ query: cat ? { category: cat } : {} })
}

watch(activeCategory, loadEntries)

onMounted(async () => {
  activeCategory.value = (route.query.category as string) || ''
  const [s, p] = await Promise.all([api.stats(), api.listProjects()])
  stats.value = s
  projects.value = p
  await loadEntries()
})
</script>

<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-bold">Entries</h1>
      <button
        @click="selectMode ? exitSelectMode() : (selectMode = true)"
        class="text-xs px-3 py-1 rounded-lg border transition-colors"
        :class="selectMode ? 'border-sky-500 text-sky-400' : 'border-gray-700 text-gray-500 hover:text-gray-300'"
      >{{ selectMode ? 'Cancel' : 'Select' }}</button>
    </div>

    <!-- Category tabs -->
    <div class="flex gap-2 flex-wrap mb-6">
      <button
        @click="setCategory('')"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === '' ? 'bg-sky-500 text-gray-950 border-sky-500 font-semibold' : 'border-gray-700 text-gray-400 hover:border-sky-600 hover:text-gray-200'"
      >
        All
      </button>
      <button
        v-for="cat in categories"
        :key="cat"
        @click="setCategory(cat)"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === cat ? 'bg-sky-500 text-gray-950 border-sky-500 font-semibold' : 'border-gray-700 text-gray-400 hover:border-sky-600 hover:text-gray-200'"
      >
        {{ cat }}
        <span v-if="stats?.categories[cat]" class="ml-1 text-xs opacity-70">({{ stats.categories[cat] }})</span>
      </button>
      <button
        @click="setCategory('review')"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === 'review' ? 'bg-amber-500 text-gray-950 border-amber-500 font-semibold' : 'border-gray-700 text-amber-400 hover:border-amber-500'"
      >
        ⚠ Review
      </button>
      <button
        @click="setCategory('unassigned')"
        class="px-3 py-1.5 rounded-lg text-sm border transition-colors"
        :class="activeCategory === 'unassigned' ? 'bg-indigo-500 text-gray-950 border-indigo-500 font-semibold' : 'border-gray-700 text-indigo-400 hover:border-indigo-500'"
      >
        📂 Unassigned
        <span v-if="stats?.unassigned_count" class="ml-1 text-xs opacity-70">({{ stats.unassigned_count }})</span>
      </button>
    </div>

    <!-- Entry list -->
    <div v-if="loading" class="text-center py-8 text-gray-500">Loading...</div>
    <div v-else-if="visibleEntries.length === 0" class="text-center py-12 text-gray-600">
      No entries{{ activeCategory ? ` in "${activeCategory}"` : '' }}{{ hiddenCount > 0 ? ` (${hiddenCount} hidden by status)` : '' }}.
    </div>
    <div v-else class="space-y-2">
      <!-- Status filter affordance -->
      <div v-if="hiddenCount > 0 || showParked" class="flex items-center justify-end px-1 pb-1">
        <button
          @click="setShowParked(!showParked)"
          class="text-xs text-gray-500 hover:text-gray-300 transition-colors"
          :title="`${hiddenBreakdown.parked} parked (someday/archived) + ${hiddenBreakdown.staleDone} done >30d`"
        >
          <span v-if="!showParked">{{ hiddenCount }} hidden by status · show all</span>
          <span v-else>showing all · hide parked</span>
        </button>
      </div>
      <!-- Select all toggle -->
      <div v-if="selectMode" class="flex items-center gap-2 px-4 py-1 text-xs text-gray-500">
        <input type="checkbox" :checked="selectedIds.size === visibleEntries.length && visibleEntries.length > 0" @change="toggleSelectAll" class="accent-sky-500 w-3.5 h-3.5">
        <span>{{ selectedIds.size === visibleEntries.length ? 'Deselect all' : 'Select all' }}</span>
      </div>
      <div
        v-for="entry in visibleEntries"
        :key="entry.id"
        class="flex items-start gap-2"
      >
        <label v-if="selectMode" class="pt-3.5 pl-1 cursor-pointer shrink-0">
          <input type="checkbox" :checked="selectedIds.has(entry.id)" @change="toggleSelect(entry.id)" class="accent-sky-500 w-4 h-4">
        </label>
        <RouterLink
          :to="`/entries/${entry.id}`"
          class="flex-1 block bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 hover:border-sky-600 transition-colors"
          :class="{ 'opacity-60': entry.notebook }"
        >
          <div class="flex items-center justify-between mb-1">
            <div class="flex items-center gap-2 min-w-0">
              <span
                v-if="(entry.category === 'actions' && entry.action_done) || (entry.category === 'projects' && entry.status === 'done')"
                class="shrink-0 w-5 h-5 rounded-full bg-emerald-500 text-white flex items-center justify-center text-xs"
              >✓</span>
              <span class="font-medium text-sm truncate"
                :class="{ 'line-through text-gray-500': (entry.category === 'actions' && entry.action_done) || (entry.category === 'projects' && entry.status === 'done') }"
              >{{ entry.title }}</span>
            </div>
            <div class="flex items-center gap-1.5 shrink-0">
              <span v-if="entry.notebook" class="text-xs px-2 py-0.5 rounded-full bg-amber-900 text-amber-400">📓</span>
              <span v-if="entry.status" class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-amber-400">{{ entry.status }}</span>
              <span class="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-sky-400">{{ entry.category }}</span>
            </div>
          </div>
        <div class="text-sm text-gray-500 line-clamp-2">{{ entry.body?.slice(0, 200) }}</div>
        <div class="flex items-center gap-2 mt-1">
          <span
            v-if="entry.project_id"
            class="text-xs px-1.5 py-0.5 rounded-full bg-indigo-900 text-indigo-300"
          >{{ projects.find(p => p.id === entry.project_id)?.emoji }} {{ projects.find(p => p.id === entry.project_id)?.name }}</span>
          <span v-if="entry.due_date" class="text-xs text-amber-400">📅 {{ entry.due_date }}</span>
          <span
            v-for="tag in (entry.tags || []).slice(0, 5)"
            :key="tag"
            class="text-xs px-1.5 py-0.5 rounded-full border border-gray-700 text-gray-500"
          >
            {{ tag }}
          </span>
          <span class="text-xs text-gray-600 ml-auto">
            {{ new Date(entry.created_at).toLocaleDateString() }}
          </span>
        </div>
      </RouterLink>
      </div>
    </div>

    <!-- Bulk action bar -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-all duration-200"
        leave-active-class="transition-all duration-150"
        enter-from-class="translate-y-full opacity-0"
        leave-to-class="translate-y-full opacity-0"
      >
        <div v-if="selectMode && selectedCount > 0" class="fixed bottom-6 left-1/2 -translate-x-1/2 bg-gray-900 border border-gray-700 rounded-xl px-5 py-3 shadow-2xl flex items-center gap-4 z-50">
          <span class="text-sm text-gray-300">{{ selectedCount }} selected</span>
          <button
            @click="bulkNotebook(true)"
            :disabled="bulkWorking"
            class="text-sm bg-amber-600 text-white px-3 py-1.5 rounded-lg hover:bg-amber-500 disabled:opacity-40 transition-colors"
          >📓 Move to Notebook</button>
          <button
            @click="bulkNotebook(false)"
            :disabled="bulkWorking"
            class="text-sm bg-gray-700 text-gray-300 px-3 py-1.5 rounded-lg hover:bg-gray-600 disabled:opacity-40 transition-colors"
          >🔄 Back to Pipeline</button>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>
