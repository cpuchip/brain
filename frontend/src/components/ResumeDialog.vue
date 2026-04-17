<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'

const props = defineProps<{
  show: boolean
  surfaceReason: string
}>()

const emit = defineEmits<{
  resume: [feedback: string]
  cancel: []
}>()

const feedback = ref('')
const textareaRef = ref<HTMLTextAreaElement | null>(null)

watch(() => props.show, async (val) => {
  if (val) {
    feedback.value = ''
    await nextTick()
    textareaRef.value?.focus()
  }
})

function onResume() {
  emit('resume', feedback.value.trim())
}

function onResumeQuick() {
  emit('resume', '')
}

function onCancel() {
  emit('cancel')
}
</script>

<template>
  <Teleport to="body">
    <div v-if="show" class="fixed inset-0 z-40 flex items-center justify-center bg-black/60" @click.self="onCancel" @keydown.escape="onCancel">
      <div role="dialog" aria-modal="true" class="w-full max-w-lg bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl text-gray-100 space-y-4">
        <h2 class="text-lg font-semibold text-amber-400">▶ Resume Commission</h2>

        <div v-if="surfaceReason" class="bg-gray-800 border border-gray-700 rounded-lg p-3 text-sm text-gray-300">
          <p class="text-xs text-gray-500 mb-1 font-medium">Steward's concern:</p>
          <p class="whitespace-pre-wrap">{{ surfaceReason }}</p>
        </div>

        <div>
          <label for="resume-feedback" class="block text-sm text-gray-400 mb-1">Direction or answers (optional)</label>
          <textarea
            id="resume-feedback"
            ref="textareaRef"
            v-model="feedback"
            rows="4"
            placeholder="Provide feedback, answer questions, or leave blank to resume as-is..."
            class="w-full rounded-lg border border-gray-700 bg-gray-800 text-gray-100 px-3 py-2 text-sm placeholder-gray-600 focus:border-amber-500 focus:ring-1 focus:ring-amber-500 outline-none resize-y"
          />
        </div>

        <div class="flex items-center justify-end gap-2">
          <button
            @click="onCancel"
            class="px-3 py-1.5 text-sm text-gray-400 hover:text-gray-200 transition-colors"
          >Cancel</button>
          <button
            @click="onResumeQuick"
            class="px-3 py-1.5 text-sm bg-gray-800 text-gray-300 border border-gray-700 rounded-lg hover:bg-gray-700 transition-colors"
          >Resume as-is</button>
          <button
            v-if="feedback.trim()"
            @click="onResume"
            class="px-4 py-1.5 text-sm bg-amber-600 text-white rounded-lg hover:bg-amber-500 transition-colors font-medium"
          >Resume with Feedback</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
