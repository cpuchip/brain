import { ref } from 'vue'
import { api, type ModelCatalogEntry } from '../api'

// Module-level state — one fetch per app lifetime, shared across consumers.
const models = ref<ModelCatalogEntry[]>([])
const stageDefaults = ref<Record<string, string> | null>(null)
const loaded = ref(false)
let inFlight: Promise<void> | null = null

async function load(): Promise<void> {
  if (loaded.value) return
  if (inFlight) return inFlight
  inFlight = (async () => {
    try {
      const res = await api.listModels()
      models.value = res.models
      stageDefaults.value = res.stage_defaults
      loaded.value = true
    } catch (e) {
      console.error('Failed to load model catalog', e)
    } finally {
      inFlight = null
    }
  })()
  return inFlight
}

export function useModelCatalog() {
  return { models, stageDefaults, loaded, load }
}
