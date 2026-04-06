<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { renderMarkdown } from '../composables/useMarkdown'

const props = defineProps<{
  open: boolean
  filePath: string
}>()

const emit = defineEmits<{
  close: []
}>()

const router = useRouter()
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

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}

function openInReader() {
  emit('close')
  router.push({ path: '/library', query: { file: props.filePath } })
}

function handleContentClick(e: MouseEvent) {
  const target = e.target as HTMLElement
  if (target.classList.contains('file-link') && target.dataset.filePath) {
    e.preventDefault()
    // Navigate to the file in the full reader instead of trying to load in sidebar
    emit('close')
    router.push({ path: '/library', query: { file: target.dataset.filePath } })
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition-transform duration-200 ease-out"
      leave-active-class="transition-transform duration-150 ease-in"
      enter-from-class="translate-x-full"
      leave-to-class="translate-x-full"
    >
      <div
        v-if="open"
        class="fixed top-0 right-0 h-full w-[45vw] min-w-[400px] max-w-[800px] z-40 flex flex-col bg-gray-900 border-l border-gray-700 shadow-2xl"
        @keydown="onKeydown"
        tabindex="-1"
      >
        <!-- Header -->
        <div class="flex items-center justify-between px-4 py-3 border-b border-gray-700 shrink-0">
          <span class="text-sm text-gray-300 font-mono truncate mr-4">{{ filePath }}</span>
          <div class="flex items-center gap-2 shrink-0">
            <button
              @click="openInReader"
              class="text-xs text-sky-400 hover:text-sky-300 px-2 py-1 rounded hover:bg-gray-800 transition-colors"
              title="Open in Library reader"
            >Open in Reader →</button>
            <button
              @click="emit('close')"
              class="text-gray-500 hover:text-gray-300 text-lg leading-none px-2 py-1 rounded hover:bg-gray-800 transition-colors"
              aria-label="Close"
            >✕</button>
          </div>
        </div>

        <!-- Content -->
        <div class="flex-1 overflow-auto p-6" @click="handleContentClick">
          <div v-if="loading" class="text-gray-500 text-sm">Loading...</div>
          <div v-else-if="error" class="text-red-400 text-sm">{{ error }}</div>
          <div
            v-else
            class="prose prose-invert prose-sm max-w-none"
            v-html="renderMarkdown(content)"
          />
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
