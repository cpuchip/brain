<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { api, type Project } from '../api'

const projects = ref<Project[]>([])
const loading = ref(true)
const showCreate = ref(false)
const newName = ref('')
const newDesc = ref('')
const newEmoji = ref('')
const creating = ref(false)

async function loadProjects() {
  loading.value = true
  try {
    projects.value = await api.listProjects()
  } finally {
    loading.value = false
  }
}

async function createProject() {
  if (!newName.value.trim()) return
  creating.value = true
  try {
    await api.createProject({
      name: newName.value.trim(),
      description: newDesc.value.trim() || undefined,
      emoji: newEmoji.value.trim() || undefined,
    })
    newName.value = ''
    newDesc.value = ''
    newEmoji.value = ''
    showCreate.value = false
    await loadProjects()
  } finally {
    creating.value = false
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

onMounted(loadProjects)
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-bold">Projects</h1>
      <button
        @click="showCreate = !showCreate"
        class="px-4 py-2 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-500 transition-colors"
      >
        {{ showCreate ? 'Cancel' : '+ New Project' }}
      </button>
    </div>

    <!-- Create form -->
    <Transition
      enter-active-class="transition-all duration-200"
      leave-active-class="transition-all duration-150"
      enter-from-class="opacity-0 -translate-y-2"
      leave-to-class="opacity-0 -translate-y-2"
    >
      <form v-if="showCreate" @submit.prevent="createProject" class="bg-gray-900 border border-gray-800 rounded-lg p-4 space-y-3">
        <div class="flex gap-3">
          <input
            v-model="newEmoji"
            placeholder="📋"
            class="w-14 bg-gray-950 border border-gray-700 rounded-lg px-2 py-2 text-center text-lg focus:outline-none focus:ring-2 focus:ring-sky-500"
            maxlength="4"
          />
          <input
            v-model="newName"
            placeholder="Project name"
            class="flex-1 bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500"
            autofocus
          />
        </div>
        <textarea
          v-model="newDesc"
          placeholder="Description (optional)"
          rows="2"
          class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-sky-500 resize-none"
        />
        <div class="flex justify-end">
          <button
            type="submit"
            :disabled="!newName.trim() || creating"
            class="px-4 py-2 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-500 transition-colors disabled:opacity-40"
          >Create</button>
        </div>
      </form>
    </Transition>

    <!-- Loading -->
    <div v-if="loading" class="text-center py-12 text-gray-500">Loading projects...</div>

    <!-- Empty state -->
    <div v-else-if="projects.length === 0" class="text-center py-12">
      <div class="text-gray-600 mb-2">No projects yet</div>
      <div class="text-sm text-gray-500">Create your first project to organize brain entries by goal.</div>
    </div>

    <!-- Project grid -->
    <div v-else class="grid gap-3">
      <RouterLink
        v-for="project in projects"
        :key="project.id"
        :to="`/projects/${project.id}`"
        class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-4 hover:border-gray-700 transition-colors block"
      >
        <div class="flex items-start justify-between mb-2">
          <div class="flex items-center gap-2">
            <span v-if="project.emoji" class="text-lg">{{ project.emoji }}</span>
            <h2 class="font-semibold text-gray-100">{{ project.name }}</h2>
          </div>
          <span :class="['px-2 py-0.5 text-xs rounded-full', statusColor(project.status)]">
            {{ project.status }}
          </span>
        </div>
        <p v-if="project.description" class="text-sm text-gray-400 mb-3 line-clamp-2">{{ project.description }}</p>
        <div class="text-xs text-gray-500">
          {{ project.entry_count || 0 }} {{ (project.entry_count || 0) === 1 ? 'entry' : 'entries' }}
        </div>
      </RouterLink>
    </div>
  </div>
</template>
