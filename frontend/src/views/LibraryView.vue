<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { api, type AgentInfo, type SkillInfo, type MemoryFile } from '../api'

type Tab = 'agents' | 'skills' | 'memory'

const activeTab = ref<Tab>('agents')
const agents = ref<AgentInfo[]>([])
const skills = ref<SkillInfo[]>([])
const memory = ref<MemoryFile[]>([])
const loading = ref(true)
const error = ref('')

async function loadAll() {
  try {
    const [a, s, m] = await Promise.all([
      api.libraryAgents(),
      api.librarySkills(),
      api.libraryMemory(),
    ])
    agents.value = a.sort((x, y) => x.name.localeCompare(y.name))
    skills.value = s.sort((x, y) => x.name.localeCompare(y.name))
    memory.value = m
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
  } finally {
    loading.value = false
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

onMounted(loadAll)
</script>

<template>
  <div class="space-y-6">
    <!-- Header -->
    <div>
      <h1 class="text-xl font-bold text-gray-100">Library</h1>
      <p class="text-sm text-gray-500 mt-1">
        {{ agents.length }} agents, {{ skills.length }} skills, {{ memory.length }} memory files
      </p>
    </div>

    <!-- Error -->
    <div v-if="error" class="bg-red-900/50 text-red-300 px-4 py-2 rounded-lg text-sm">{{ error }}</div>

    <!-- Tabs -->
    <div class="flex gap-1 bg-gray-900 rounded-lg p-1">
      <button
        v-for="tab in (['agents', 'skills', 'memory'] as Tab[])"
        :key="tab"
        @click="activeTab = tab"
        class="flex-1 px-3 py-1.5 text-sm rounded-md transition-colors capitalize"
        :class="activeTab === tab ? 'bg-gray-800 text-white' : 'text-gray-500 hover:text-gray-300'"
      >
        {{ tab }}
        <span class="ml-1 text-xs opacity-60">
          ({{ tab === 'agents' ? agents.length : tab === 'skills' ? skills.length : memory.length }})
        </span>
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-gray-500 text-sm">Loading library...</div>

    <!-- Agents tab -->
    <div v-else-if="activeTab === 'agents'" class="space-y-2">
      <div v-if="agents.length === 0" class="text-gray-600 text-sm text-center py-8">
        No agents found. Make sure brain.exe can find the workspace .github/agents/ directory.
      </div>
      <div
        v-for="agent in agents"
        :key="agent.name"
        class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3"
      >
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium text-gray-200">{{ agent.name }}</span>
          <span class="text-xs px-2 py-0.5 rounded-full bg-purple-900 text-purple-300">agent</span>
        </div>
        <p v-if="agent.description" class="text-sm text-gray-500 mt-1">{{ agent.description }}</p>
        <p v-else class="text-sm text-gray-600 mt-1 italic">No description</p>
      </div>
    </div>

    <!-- Skills tab -->
    <div v-else-if="activeTab === 'skills'" class="space-y-2">
      <div v-if="skills.length === 0" class="text-gray-600 text-sm text-center py-8">
        No skills found. Make sure brain.exe can find the workspace .github/skills/ directory.
      </div>
      <div
        v-for="skill in skills"
        :key="skill.name"
        class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3"
      >
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium text-gray-200">{{ skill.name }}</span>
          <span class="text-xs px-2 py-0.5 rounded-full bg-emerald-900 text-emerald-300">skill</span>
        </div>
        <p v-if="skill.description" class="text-sm text-gray-500 mt-1 line-clamp-2">{{ skill.description }}</p>
        <p v-else class="text-sm text-gray-600 mt-1 italic">No description</p>
      </div>
    </div>

    <!-- Memory tab -->
    <div v-else-if="activeTab === 'memory'" class="space-y-2">
      <div v-if="memory.length === 0" class="text-gray-600 text-sm text-center py-8">
        No memory files found. Memory lives in .spec/memory/ in the workspace.
      </div>
      <div
        v-for="file in memory"
        :key="file.path"
        class="bg-gray-900 border border-gray-800 rounded-lg px-4 py-3 flex items-center justify-between"
      >
        <div>
          <span class="text-sm font-medium text-gray-200">{{ file.name }}</span>
          <span class="text-xs text-gray-600 ml-2">{{ file.path }}</span>
        </div>
        <span class="text-xs text-gray-500">{{ formatSize(file.size) }}</span>
      </div>
    </div>
  </div>
</template>
