import { ref } from 'vue'

const filePanelOpen = ref(false)

export function useFilePanel() {
  return { filePanelOpen }
}
