import { ref } from 'vue'

const filePanelOpen = ref(false)
const wideLayout = ref(false)

export function useFilePanel() {
  return { filePanelOpen, wideLayout }
}
