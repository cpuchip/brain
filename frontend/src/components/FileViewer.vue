<script setup lang="ts">
import { ref, watch } from 'vue'
import { renderMarkdown } from '../composables/useMarkdown'

const props = defineProps<{
  open: boolean
  filePath: string
}>()

const emit = defineEmits<{
  close: []
}>()

const content = ref('')
const loading = ref(false)
const error = ref('')

watch(() => [props.open, props.filePath], async () => {
  if (!props.open || !props.filePath) return
  loading.value = true
  error.value = ''
  content.value = ''
  try {
    const res = await fetch(`/api/files/read?path=${encodeURIComponent(props.filePath)}`)
    if (!res.ok) {
      error.value = res.status === 403 ? 'Access denied' : res.status === 404 ? 'File not found' : `Error ${res.status}`
      return
    }
    content.value = await res.text()
  } catch (e) {
    error.value = 'Failed to load file'
  } finally {
    loading.value = false
  }
}, { immediate: true })

function onBackdropClick(e: MouseEvent) {
  if (e.target === e.currentTarget) emit('close')
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition-opacity duration-200"
      leave-active-class="transition-opacity duration-150"
      enter-from-class="opacity-0"
      leave-to-class="opacity-0"
    >
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
        @click="onBackdropClick"
        @keydown="onKeydown"
        tabindex="-1"
        ref="backdrop"
      >
        <div class="bg-gray-900 border border-gray-700 rounded-xl w-[80vw] h-[80vh] flex flex-col shadow-2xl">
          <!-- Header -->
          <div class="flex items-center justify-between px-4 py-3 border-b border-gray-700 shrink-0">
            <span class="text-sm text-gray-300 font-mono truncate mr-4">{{ filePath }}</span>
            <button
              @click="emit('close')"
              class="text-gray-500 hover:text-gray-300 text-lg leading-none px-1"
              aria-label="Close"
            >✕</button>
          </div>

          <!-- Content -->
          <div class="flex-1 overflow-auto p-6">
            <div v-if="loading" class="text-gray-500 text-sm">Loading...</div>
            <div v-else-if="error" class="text-red-400 text-sm">{{ error }}</div>
            <div
              v-else
              class="prose prose-invert prose-sm max-w-none"
              v-html="renderMarkdown(content)"
            />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
