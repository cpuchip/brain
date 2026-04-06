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
  project_id?: number | null
  created_at: string
  updated_at: string
  subtasks?: SubTask[]
  // Category-specific fields
  status?: string
  action_done?: boolean
  due_date?: string
  next_action?: string
  // Agent routing
  agent_route?: string
  route_status?: string
  agent_output?: string
  tokens_used?: number
  // Pipeline maturity
  maturity?: string
}

export interface Project {
  id: number
  name: string
  description?: string
  status: string
  emoji?: string
  context_file?: string
  entry_count?: number
  created_at: string
  updated_at: string
}

export interface SessionMessage {
  id: number
  entry_id: string
  role: 'human' | 'agent'
  content: string
  created_at: string
}

export interface ScheduledTask {
  id: number
  name: string
  description?: string
  schedule: string
  project_id?: number | null
  agent_name: string
  prompt: string
  status: string
  last_run_at?: string | null
  next_run_at?: string | null
  created_at: string
  updated_at: string
}

export interface TaskRun {
  id: number
  task_id: number
  status: string
  entry_id?: string
  output?: string
  error?: string
  started_at: string
  ended_at?: string | null
}

export interface ActivityEvent {
  id: string
  type: string
  title: string
  entry_id?: string
  project_id?: number | null
  timestamp: string
}

export interface AgentInfo {
  name: string
  description?: string
}

export interface SkillInfo {
  name: string
  description?: string
}

export interface MemoryFile {
  name: string
  path: string
  size: number
}

export interface FileTreeNode {
  name: string
  path: string
  is_dir: boolean
  children?: FileTreeNode[]
}

export interface Stats {
  categories: Record<string, number>
  total: number
  unassigned_count: number
  vec_count: number
  vec_enabled: boolean
}

export interface SearchResult {
  entry_id: string
  similarity: number
}

export interface RoutableEntry {
  id: string
  title: string
  category: string
  agent_name: string
}

export interface RunningEntry {
  entry_id: string
  agent_name: string
}

export interface BrainStatus {
  agent_online: boolean
  queued_count: number
  model: string
  total_entries: number
  categories: Record<string, number>
}

export interface ReviewEntry {
  id: string
  title: string
  category: string
  agent_route: string
  agent_output: string
  tokens_used: number
  body: string
  updated_at: string
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
  listEntries(params?: { category?: string; limit?: number; offset?: number; needs_review?: boolean; unassigned?: boolean }) {
    const q = new URLSearchParams()
    if (params?.category) q.set('category', params.category)
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    if (params?.needs_review) q.set('needs_review', 'true')
    if (params?.unassigned) q.set('unassigned', 'true')
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

  updateEntry(id: string, updates: Partial<Pick<Entry, 'title' | 'category' | 'body' | 'tags' | 'status' | 'action_done' | 'due_date' | 'project_id'>>) {
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

  // Agent & Dashboard
  agentSessions() {
    return request<{ sessions: string[] }>('/agent/sessions')
  },

  agentRoutable() {
    return request<{ entries: RoutableEntry[] }>('/agent/routable')
  },

  agentRoute(entryId: string) {
    return request<{ status: string; agent: string; entry_id: string }>('/agent/route', {
      method: 'POST',
      body: JSON.stringify({ entry_id: entryId }),
    })
  },

  agentRunning() {
    return request<{ entries: RunningEntry[] }>('/agent/running')
  },

  dismissRoute(entryId: string) {
    return request<{ status: string; entry_id: string }>(`/entries/${encodeURIComponent(entryId)}/dismiss-route`, {
      method: 'POST',
    })
  },

  brainStatus() {
    return request<BrainStatus>('/brain/status')
  },

  // Review queue — completed agent work awaiting human review
  reviewQueue() {
    return request<{ entries: ReviewEntry[] }>('/agent/review')
  },

  reviewAction(entryId: string, action: 'accept' | 'reject') {
    return request<{ status: string; entry_id: string }>(`/agent/review/${encodeURIComponent(entryId)}`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    })
  },

  shutdown() {
    return request<{ status: string }>('/shutdown', { method: 'POST' })
  },

  // Projects
  listProjects() {
    return request<Project[]>('/projects')
  },

  createProject(data: { name: string; description?: string; emoji?: string }) {
    return request<Project>('/projects', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  getProject(id: number) {
    return request<Project>(`/projects/${id}`)
  },

  updateProject(id: number, updates: Partial<Pick<Project, 'name' | 'description' | 'status' | 'emoji' | 'context_file'>>) {
    return request<Project>(`/projects/${id}`, {
      method: 'PUT',
      body: JSON.stringify(updates),
    })
  },

  deleteProject(id: number) {
    return request<void>(`/projects/${id}`, { method: 'DELETE' })
  },

  projectEntries(id: number) {
    return request<Entry[]>(`/projects/${id}/entries`)
  },

  projectStats(id: number) {
    return request<{ maturity_counts: Record<string, number>; your_turn_count: number; running_count: number; total_entries: number }>(`/projects/${id}/stats`)
  },

  setEntryProject(entryId: string, projectId: number | null) {
    return request<{ entry_id: string; project_id: number | null }>(`/entries/${encodeURIComponent(entryId)}/project`, {
      method: 'PUT',
      body: JSON.stringify({ project_id: projectId }),
    })
  },

  // Session messages (iterative turns)
  listMessages(entryId: string) {
    return request<SessionMessage[]>(`/entries/${encodeURIComponent(entryId)}/messages`)
  },

  reply(entryId: string, content: string) {
    return request<{ id: number; entry_id: string; role: string }>(`/entries/${encodeURIComponent(entryId)}/reply`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    })
  },

  markComplete(entryId: string) {
    return request<{ entry_id: string; status: string }>(`/entries/${encodeURIComponent(entryId)}/complete`, {
      method: 'POST',
    })
  },

  entryContext(entryId: string) {
    return request<{ entry_id: string; title: string; category: string; maturity: string; project?: { name: string; description: string; siblings: { Title: string; Maturity: string; RouteStatus: string }[]; context_doc: boolean }; formatted?: string }>(`/entries/${encodeURIComponent(entryId)}/context`)
  },

  yourTurn() {
    return request<{ entries: { id: string; title: string; category: string; agent_route: string; body: string; updated_at: string }[] }>('/entries/your-turn')
  },

  // Scheduled tasks
  listScheduledTasks() {
    return request<ScheduledTask[]>('/scheduled')
  },

  createScheduledTask(data: { name: string; description?: string; schedule: string; agent_name: string; prompt: string; project_id?: number | null }) {
    return request<ScheduledTask>('/scheduled', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  getScheduledTask(id: number) {
    return request<ScheduledTask>(`/scheduled/${id}`)
  },

  updateScheduledTask(id: number, updates: Partial<Pick<ScheduledTask, 'name' | 'description' | 'schedule' | 'agent_name' | 'prompt' | 'status' | 'project_id'>>) {
    return request<ScheduledTask>(`/scheduled/${id}`, {
      method: 'PUT',
      body: JSON.stringify(updates),
    })
  },

  deleteScheduledTask(id: number) {
    return request<void>(`/scheduled/${id}`, { method: 'DELETE' })
  },

  listTaskRuns(taskId: number) {
    return request<TaskRun[]>(`/scheduled/${taskId}/runs`)
  },

  triggerTaskRun(taskId: number) {
    return request<{ run_id: number; status: string }>(`/scheduled/${taskId}/run`, { method: 'POST' })
  },

  // Library
  libraryAgents() {
    return request<AgentInfo[]>('/library/agents')
  },

  librarySkills() {
    return request<SkillInfo[]>('/library/skills')
  },

  libraryMemory() {
    return request<MemoryFile[]>('/library/memory')
  },

  // Activity feed
  activity(limit?: number) {
    const params = limit ? `?limit=${limit}` : ''
    return request<ActivityEvent[]>(`/activity${params}`)
  },

  // Pipeline maturity
  pipelineAdvance(entryId: string, action: 'advance' | 'revise' | 'reject' | 'defer', feedback?: string, scenarios?: string[]) {
    return request<{ id: string; old_maturity: string; new_maturity: string; scratch_path?: string; message: string }>('/pipeline/advance', {
      method: 'POST',
      body: JSON.stringify({ id: entryId, action, feedback, scenarios }),
    })
  },

  pipelineReview(entryId: string) {
    return request<{ id: string; title: string; category: string; body: string; maturity: string; maturity_notes: string; scratch_path: string; scenarios: string; tags: string[] }>(`/pipeline/review/${encodeURIComponent(entryId)}`)
  },

  // Execution gate (Phase 4e)
  executeEntry(entryId: string, feedback?: string) {
    return request<{ id: string; message: string }>(`/entries/${encodeURIComponent(entryId)}/execute`, {
      method: 'POST',
      body: JSON.stringify({ feedback }),
    })
  },

  verifyEntry(entryId: string, results: { scenario: string; passed: boolean; notes?: string }[]) {
    return request<{ id: string; all_passed: boolean; new_maturity: string; message: string }>(`/entries/${encodeURIComponent(entryId)}/verify`, {
      method: 'POST',
      body: JSON.stringify({ results }),
    })
  },

  executionContext(entryId: string) {
    return request<{ entry_id: string; title: string; maturity: string; scenarios: string[]; model: string; cost: number; prompt: string; has_scratch: boolean }>(`/entries/${encodeURIComponent(entryId)}/execution-context`)
  },

  async readFile(path: string): Promise<string> {
    const res = await fetch(`/api/files/read?path=${encodeURIComponent(path)}`)
    if (!res.ok) throw new Error(`${res.status}: ${res.statusText}`)
    return res.text()
  },

  async fileTree(root: string = '.'): Promise<FileTreeNode[]> {
    return request<FileTreeNode[]>(`/files/tree?root=${encodeURIComponent(root)}`)
  },
}
