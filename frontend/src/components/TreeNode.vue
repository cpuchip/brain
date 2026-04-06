<script setup lang="ts">
import { computed } from 'vue'
import type { FileTreeNode } from '../api'

const props = defineProps<{
  node: FileTreeNode
  depth: number
  expandedDirs: Set<string>
  currentPath: string
}>()

const emit = defineEmits<{
  'toggle-dir': [path: string]
  'open-file': [path: string]
}>()

const isExpanded = computed(() => props.expandedDirs.has(props.node.path))
const isCurrent = computed(() => props.node.path === props.currentPath)
const indent = computed(() => `${props.depth * 12 + 8}px`)

const displayName = computed(() => {
  const name = props.node.name
  return name.endsWith('.md') ? name.slice(0, -3) : name
})
</script>

<template>
  <div>
    <!-- Directory -->
    <button
      v-if="node.is_dir"
      @click="emit('toggle-dir', node.path)"
      class="w-full flex items-center gap-1.5 px-2 py-1 text-sm text-gray-400 hover:text-gray-200 hover:bg-gray-800/50 rounded transition-colors"
      :style="{ paddingLeft: indent }"
    >
      <span class="text-[10px] transition-transform" :class="isExpanded ? 'rotate-90' : ''">▶</span>
      <span class="font-medium truncate">{{ node.name }}</span>
    </button>

    <!-- File -->
    <button
      v-else
      @click="emit('open-file', node.path)"
      class="w-full flex items-center gap-1.5 px-2 py-1 text-sm rounded transition-colors truncate"
      :class="isCurrent
        ? 'bg-sky-900/40 text-sky-300 font-medium'
        : 'text-gray-400 hover:text-gray-200 hover:bg-gray-800/50'"
      :style="{ paddingLeft: indent }"
    >
      <span class="text-[10px]" :class="isCurrent ? 'text-sky-400' : 'text-gray-600'">●</span>
      <span class="truncate">{{ displayName }}</span>
    </button>

    <!-- Children -->
    <template v-if="node.is_dir && isExpanded && node.children">
      <TreeNode
        v-for="child in node.children"
        :key="child.path"
        :node="child"
        :depth="depth + 1"
        :expanded-dirs="expandedDirs"
        :current-path="currentPath"
        @toggle-dir="emit('toggle-dir', $event)"
        @open-file="emit('open-file', $event)"
      />
    </template>
  </div>
</template>
