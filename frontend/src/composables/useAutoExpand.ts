import { type Ref, onMounted, nextTick } from 'vue'

export function useAutoExpand(el: Ref<HTMLTextAreaElement | null>, maxHeight = 300) {
  function resize() {
    if (!el.value) return
    el.value.style.height = 'auto'
    el.value.style.height = Math.min(el.value.scrollHeight, maxHeight) + 'px'
    el.value.style.overflowY = el.value.scrollHeight > maxHeight ? 'auto' : 'hidden'
  }

  onMounted(() => {
    nextTick(resize)
  })

  return { resize }
}
