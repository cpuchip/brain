<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { api, type AgentInfo, type SkillInfo, type MemoryFile, type FileTreeNode } from '../api'
import { renderMarkdown } from '../composables/useMarkdown'
import { useFilePanel } from '../composables/useFilePanel'
import TreeNode from '../components/TreeNode.vue'

const { wideLayout } = useFilePanel()

type Tab = 'files' | 'agents' | 'skills' | 'memory'

const activeTab = ref<Tab>('files')
watch(activeTab, (tab) => { wideLayout.value = tab === 'files' }, { immediate: true })
onUnmounted(() => { wideLayout.value = false })
const agents = ref<AgentInfo[]>([])
const skills = ref<SkillInfo[]>([])
const memory = ref<MemoryFile[]>([])
const loading = ref(true)
const error = ref('')

// File browser state
const fileTree = ref<FileTreeNode[]>([])
const fileTreeLoading = ref(false)
const expandedDirs = ref(new Set<string>())
const currentFilePath = ref('')
const fileContent = ref('')
const fileLoading = ref(false)
const fileError = ref('')
const searchFilter = ref('')

const filteredTree = computed(() => {
  if (!searchFilter.value) return fileTree.value
  const q = searchFilter.value.toLowerCase()
  function filterNodes(nodes: FileTreeNode[]): FileTreeNode[] {
    const result: FileTreeNode[] = []
    for (const node of nodes) {
      if (node.is_dir && node.children) {
        const filtered = filterNodes(node.children)
        if (filtered.length > 0) {
          result.push({ ...node, children: filtered })
        }
      } else if (node.name.toLowerCase().includes(q)) {
        result.push(node)
      }
    }
    return result
  }
  return filterNodes(fileTree.value)
})

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

async function loadFileTree() {
  fileTreeLoading.value = true
  try {
    fileTree.value = await api.fileTree('.')
    // Auto-expand .spec on first load
    expandedDirs.value.add('.')
    expandedDirs.value.add('.spec')
    expandedDirs.value.add('.spec/scratch')
  } catch {
    // non-critical
  } finally {
    fileTreeLoading.value = false
  }
}

function toggleDir(path: string) {
  const dirs = expandedDirs.value
  if (dirs.has(path)) {
    dirs.delete(path)
  } else {
    dirs.add(path)
  }
}

async function openFile(path: string) {
  currentFilePath.value = path
  fileLoading.value = true
  fileError.value = ''
  fileContent.value = ''
  try {
    fileContent.value = await api.readFile(path)
  } catch (e: any) {
    fileError.value = e.message || 'Failed to load file'
  } finally {
    fileLoading.value = false
  }
}

onMounted(() => {
  loadAll()
  loadFileTree()
})
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
        v-for="tab in (['files', 'agents', 'skills', 'memory'] as Tab[])"
        :key="tab"
        @click="activeTab = tab"
        class="flex-1 px-3 py-1.5 text-sm rounded-md transition-colors capitalize"
        :class="activeTab === tab ? 'bg-gray-800 text-white' : 'text-gray-500 hover:text-gray-300'"
      >
        {{ tab }}
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading && activeTab !== 'files'" class="text-gray-500 text-sm">Loading library...</div>

    <!-- Files tab -->
    <div v-if="activeTab === 'files'" class="flex gap-4" style="height: calc(100vh - 200px);">
      <!-- Sidebar tree -->
      <div class="w-64 shrink-0 flex flex-col bg-gray-900 border border-gray-800 rounded-lg overflow-hidden">
        <div class="p-2 border-b border-gray-800">
          <input
            v-model="searchFilter"
            type="text"
            placeholder="Filter files..."
            class="w-full bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm text-gray-200 focus:outline-none focus:border-sky-500"
          />
        </div>
        <div class="flex-1 overflow-auto py-1">
          <div v-if="fileTreeLoading" class="text-gray-500 text-sm px-3 py-2">Loading...</div>
          <div v-else-if="filteredTree.length === 0" class="text-gray-600 text-sm px-3 py-2">No files found</div>
          <template v-else>
            <TreeNode
              v-for="node in filteredTree"
              :key="node.path"
              :node="node"
              :depth="0"
              :expanded-dirs="expandedDirs"
              :current-path="currentFilePath"
              @toggle-dir="toggleDir"
              @open-file="openFile"
            />
          </template>
        </div>
      </div>

      <!-- Content area -->
      <div class="flex-1 bg-gray-900 border border-gray-800 rounded-lg overflow-hidden flex flex-col min-w-0">
        <div v-if="!currentFilePath" class="flex-1 flex items-center justify-center text-gray-600 text-sm">
          Select a file to view
        </div>
        <template v-else>
          <div class="px-4 py-2 border-b border-gray-800 shrink-0">
            <span class="text-xs text-gray-400 font-mono">{{ currentFilePath }}</span>
          </div>
          <div class="flex-1 overflow-auto p-6">
            <div v-if="fileLoading" class="text-gray-500 text-sm">Loading...</div>
            <div v-else-if="fileError" class="text-red-400 text-sm">{{ fileError }}</div>
            <div
              v-else
              class="prose prose-invert prose-sm max-w-none"
              v-html="renderMarkdown(fileContent)"
            />
          </div>
        </template>
      </div>
    </div>

    <!-- Agents tab -->
    <div v-if="activeTab === 'agents'" class="space-y-2">
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
