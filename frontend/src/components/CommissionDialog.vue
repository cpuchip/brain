<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type Commission } from '../api'

const props = defineProps<{
  open: boolean
  entryId: string
  entryTitle: string
}>()

const emit = defineEmits<{
  close: []
  commissioned: [commission: Commission]
}>()

const intent = ref('')
const showAdvanced = ref(false)
const authority = ref('advance_and_execute')
const model = ref('claude-opus-4.6')
const maxCost = ref(50)
const submitting = ref(false)
const error = ref('')

// Reset form when dialog opens
watch(() => props.open, (isOpen) => {
  if (isOpen) {
    intent.value = ''
    showAdvanced.value = false
    authority.value = 'advance_and_execute'
    model.value = 'claude-opus-4.6'
    maxCost.value = 50
    submitting.value = false
    error.value = ''
  }
})

async function submit() {
  if (!intent.value.trim()) return
  submitting.value = true
  error.value = ''
  try {
    const commission = await api.createCommission({
      entry_id: props.entryId,
      intent: intent.value.trim(),
      authority: authority.value,
      model: model.value,
      max_cost: maxCost.value,
    })
    emit('commissioned', commission)
  } catch (e: any) {
    error.value = e.message || 'Failed to create commission'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      role="dialog"
      aria-modal="true"
      class="fixed inset-0 z-40 flex items-center justify-center text-gray-100"
      @keydown.escape="emit('close')"
    >
      <div class="fixed inset-0 bg-black/50" @click="emit('close')" />
      <div class="relative bg-gray-900 border border-gray-700 rounded-xl p-6 shadow-xl max-w-lg w-full">
        <h3 class="font-semibold mb-1 text-amber-400">📜 Commission Steward</h3>
        <p class="text-sm text-gray-400 mb-4 truncate" :title="entryTitle">for: {{ entryTitle }}</p>

        <!-- Intent -->
        <label class="block text-sm text-gray-300 mb-1">What should the steward accomplish?</label>
        <textarea
          v-model="intent"
          placeholder="Build the clock display with..."
          rows="3"
          class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-amber-500 resize-none mb-4"
          :disabled="submitting"
          @keydown.ctrl.enter="submit"
        />

        <!-- Advanced options toggle -->
        <button
          @click="showAdvanced = !showAdvanced"
          class="text-sm text-gray-500 hover:text-gray-300 mb-3 flex items-center gap-1"
        >
          <span class="text-xs">{{ showAdvanced ? '▾' : '▸' }}</span>
          Advanced options
        </button>

        <!-- Advanced options -->
        <div v-if="showAdvanced" class="space-y-3 mb-4 pl-2 border-l-2 border-gray-800">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Authority</label>
            <select
              v-model="authority"
              class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-amber-500"
              :disabled="submitting"
            >
              <option value="advance_and_execute">Advance & Execute</option>
              <option value="advance_only">Advance Only (stops before execution)</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Model</label>
            <select
              v-model="model"
              class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-amber-500"
              :disabled="submitting"
            >
              <option value="claude-opus-4.6">Claude Opus 4.6 (3.0×)</option>
              <option value="claude-sonnet-4">Claude Sonnet 4 (1.0×)</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Budget (premium requests)</label>
            <input
              v-model.number="maxCost"
              type="number"
              min="1"
              max="500"
              class="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-amber-500"
              :disabled="submitting"
            />
          </div>
        </div>

        <!-- Error -->
        <div v-if="error" class="text-sm text-red-400 mb-3">{{ error }}</div>

        <!-- Actions -->
        <div class="flex justify-end gap-2">
          <button
            @click="emit('close')"
            class="px-3 py-1.5 text-sm text-gray-400 hover:text-white"
            :disabled="submitting"
          >Cancel</button>
          <button
            @click="submit"
            :disabled="!intent.trim() || submitting"
            class="px-4 py-2 text-sm bg-amber-600 text-white rounded-lg hover:bg-amber-500 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {{ submitting ? 'Commissioning...' : 'Commission' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
