import { ref, onUnmounted } from 'vue'

export interface WsEvent {
  type: string
  entry_id?: string
  data?: any
}

type Handler = (evt: WsEvent) => void

const handlers = new Map<string, Set<Handler>>()
let ws: WebSocket | null = null
let reconnectDelay = 1000

function getWsUrl(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws`
}

function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return
  }

  ws = new WebSocket(getWsUrl())

  ws.onopen = () => {
    console.log('[ws] connected')
    reconnectDelay = 1000
  }

  ws.onmessage = (msg) => {
    try {
      const evt: WsEvent = JSON.parse(msg.data)
      const typeHandlers = handlers.get(evt.type)
      if (typeHandlers) {
        typeHandlers.forEach(h => h(evt))
      }
      // Also fire wildcard handlers
      const wildcardHandlers = handlers.get('*')
      if (wildcardHandlers) {
        wildcardHandlers.forEach(h => h(evt))
      }
    } catch (e) {
      console.warn('[ws] bad message:', e)
    }
  }

  ws.onclose = () => {
    console.log('[ws] disconnected, reconnecting in', reconnectDelay, 'ms')
    ws = null
    setTimeout(() => {
      reconnectDelay = Math.min(reconnectDelay * 2, 30000)
      connect()
    }, reconnectDelay)
  }

  ws.onerror = () => {
    ws?.close()
  }
}

/** Subscribe to a WebSocket event type. Use '*' for all events. Returns unsubscribe fn. */
function on(type: string, handler: Handler): () => void {
  if (!handlers.has(type)) {
    handlers.set(type, new Set())
  }
  handlers.get(type)!.add(handler)

  // Ensure connection is live
  connect()

  return () => {
    handlers.get(type)?.delete(handler)
  }
}

/** Composable for Vue components — auto-cleans subscriptions on unmount. */
export function useWebSocket() {
  const connected = ref(false)

  // Track connection state
  const checkState = setInterval(() => {
    connected.value = ws?.readyState === WebSocket.OPEN
  }, 2000)

  connect()
  connected.value = ws?.readyState === WebSocket.OPEN

  const unsubs: (() => void)[] = []

  function subscribe(type: string, handler: Handler) {
    unsubs.push(on(type, handler))
  }

  onUnmounted(() => {
    clearInterval(checkState)
    unsubs.forEach(fn => fn())
  })

  return { connected, subscribe }
}
