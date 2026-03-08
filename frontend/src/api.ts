export interface SubTask {
  id: string
  entry_id: string
  text: string
  done: boolean
  sort_order: number
  created_at?: string
  updated_at?: string
}

export interface Entry {
  id: string
  title: string
  category: string
  body: string
  tags: string[]
  source: string
  confidence: number
  needs_review: boolean
  ibecome_task_id?: number
  created_at: string
  updated_at: string
  subtasks?: SubTask[]
  // Category-specific fields
  status?: string
  action_done?: boolean
  due_date?: string
  next_action?: string
}

export interface Stats {
  categories: Record<string, number>
  total: number
  vec_count: number
  vec_enabled: boolean
}

export interface SearchResult {
  entry_id: string
  similarity: number
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || res.statusText)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

export const api = {
  listEntries(params?: { category?: string; limit?: number; offset?: number; needs_review?: boolean }) {
    const q = new URLSearchParams()
    if (params?.category) q.set('category', params.category)
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    if (params?.needs_review) q.set('needs_review', 'true')
    const qs = q.toString()
    return request<Entry[]>(`/entries${qs ? '?' + qs : ''}`)
  },

  getEntry(id: string) {
    return request<Entry>(`/entries/${encodeURIComponent(id)}`)
  },

  createEntry(data: { title: string; body: string; category?: string; tags?: string[]; source?: string }) {
    return request<Entry>('/entries', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  updateEntry(id: string, updates: Partial<Pick<Entry, 'title' | 'category' | 'body' | 'tags' | 'status' | 'action_done' | 'due_date'>>) {
    return request<Entry>(`/entries/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(updates),
    })
  },

  deleteEntry(id: string) {
    return request<void>(`/entries/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  reclassify(id: string, category: string) {
    return request<{ id: string; category: string }>(`/entries/${encodeURIComponent(id)}/reclassify`, {
      method: 'POST',
      body: JSON.stringify({ category }),
    })
  },

  search(q: string, limit?: number) {
    const params = new URLSearchParams({ q })
    if (limit) params.set('limit', String(limit))
    return request<Entry[]>(`/search?${params}`)
  },

  semanticSearch(q: string, limit?: number, category?: string) {
    const params = new URLSearchParams({ q })
    if (limit) params.set('limit', String(limit))
    if (category) params.set('category', category)
    return request<SearchResult[]>(`/search/semantic?${params}`)
  },

  stats() {
    return request<Stats>('/stats')
  },

  tags() {
    return request<string[]>('/tags')
  },

  classify(id: string) {
    return request<Entry>(`/entries/${encodeURIComponent(id)}/classify`, { method: 'POST' })
  },

  // Subtask CRUD
  createSubTask(entryId: string, text: string) {
    return request<SubTask>(`/entries/${encodeURIComponent(entryId)}/subtasks`, {
      method: 'POST',
      body: JSON.stringify({ text }),
    })
  },

  updateSubTask(entryId: string, subtaskId: string, updates: Partial<Pick<SubTask, 'text' | 'done'>>) {
    return request<SubTask>(`/entries/${encodeURIComponent(entryId)}/subtasks/${encodeURIComponent(subtaskId)}`, {
      method: 'PUT',
      body: JSON.stringify(updates),
    })
  },

  deleteSubTask(entryId: string, subtaskId: string) {
    return request<void>(`/entries/${encodeURIComponent(entryId)}/subtasks/${encodeURIComponent(subtaskId)}`, {
      method: 'DELETE',
    })
  },
}
