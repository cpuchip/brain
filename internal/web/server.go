package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

// Server serves the brain web UI and REST API.
type Server struct {
	store *store.Store
	cfg   *config.Config
	mux   *http.ServeMux
	srv   *http.Server
}

// NewServer creates a new web server.
func NewServer(st *store.Store, cfg *config.Config) *Server {
	s := &Server{
		store: st,
		cfg:   cfg,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

// ListenAndServe starts the web server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	s.srv = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s.srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
}

func (s *Server) routes() {
	// API routes
	s.mux.HandleFunc("GET /api/entries", s.handleListEntries)
	s.mux.HandleFunc("GET /api/entries/{id}", s.handleGetEntry)
	s.mux.HandleFunc("POST /api/entries", s.handleCreateEntry)
	s.mux.HandleFunc("PUT /api/entries/{id}", s.handleUpdateEntry)
	s.mux.HandleFunc("DELETE /api/entries/{id}", s.handleDeleteEntry)
	s.mux.HandleFunc("POST /api/entries/{id}/reclassify", s.handleReclassify)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/search/semantic", s.handleSemanticSearch)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/tags", s.handleTags)
	s.mux.HandleFunc("POST /api/archive", s.handleArchive)

	// Frontend — serve embedded SPA (or a simple HTML page for now)
	s.mux.HandleFunc("GET /", s.handleIndex)
}

// --- API Handlers ---

func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	offset := 0
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}
	if offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			offset = v
		}
	}

	var entries []*store.Entry
	var err error
	if category != "" {
		entries, err = s.store.DB().ListCategory(category)
	} else if r.URL.Query().Get("needs_review") == "true" {
		entries, err = s.store.DB().NeedsReviewEntries()
	} else {
		entries, err = s.store.DB().ListAll(limit, offset)
	}

	if err != nil {
		jsonError(w, "listing entries", err, http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []*store.Entry{}
	}
	jsonResponse(w, entries)
}

func (s *Server) handleGetEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, err := s.store.DB().GetEntry(id)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}
	jsonResponse(w, entry)
}

type createEntryRequest struct {
	Title    string   `json:"title"`
	Category string   `json:"category"`
	Body     string   `json:"body"`
	Tags     []string `json:"tags"`
	Source   string   `json:"source"`
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	var req createEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	if req.Title == "" || req.Body == "" {
		jsonError(w, "title and body are required", nil, http.StatusBadRequest)
		return
	}
	if req.Category == "" {
		req.Category = "inbox"
	}
	if req.Source == "" {
		req.Source = "web"
	}

	entry := &store.Entry{
		Title:    req.Title,
		Category: req.Category,
		Body:     req.Body,
		Tags:     req.Tags,
		Source:   req.Source,
	}

	id, err := s.store.DB().InsertEntry(entry)
	if err != nil {
		jsonError(w, "creating entry", err, http.StatusInternalServerError)
		return
	}
	entry.ID = id

	// Embed in vector store
	if s.store.Vec() != nil && s.store.Vec().Enabled() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.store.Vec().Embed(ctx, entry); err != nil {
				log.Printf("warning: embedding failed for %s: %v", id, err)
			}
		}()
	}

	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, entry)
}

func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Get existing entry
	existing, err := s.store.DB().GetEntry(id)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	// Decode partial update
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	// Apply updates to existing entry
	if v, ok := updates["title"].(string); ok {
		existing.Title = v
	}
	if v, ok := updates["category"].(string); ok {
		existing.Category = v
	}
	if v, ok := updates["body"].(string); ok {
		existing.Body = v
	}
	if v, ok := updates["tags"]; ok {
		if tags, ok := v.([]interface{}); ok {
			existing.Tags = nil
			for _, t := range tags {
				if tag, ok := t.(string); ok {
					existing.Tags = append(existing.Tags, tag)
				}
			}
		}
	}

	if err := s.store.DB().UpdateEntry(existing); err != nil {
		jsonError(w, "updating entry", err, http.StatusInternalServerError)
		return
	}

	// Re-embed
	if s.store.Vec() != nil && s.store.Vec().Enabled() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.store.Vec().ReEmbed(ctx, existing); err != nil {
				log.Printf("warning: re-embed failed for %s: %v", id, err)
			}
		}()
	}

	jsonResponse(w, existing)
}

func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := s.store.DB().DeleteEntry(id); err != nil {
		jsonError(w, "deleting entry", err, http.StatusInternalServerError)
		return
	}

	// Remove from vector store
	if s.store.Vec() != nil && s.store.Vec().Enabled() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = s.store.Vec().Remove(ctx, id)
		}()
	}

	w.WriteHeader(http.StatusNoContent)
}

type reclassifyRequest struct {
	Category string `json:"category"`
}

func (s *Server) handleReclassify(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req reclassifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}
	if req.Category == "" {
		jsonError(w, "category is required", nil, http.StatusBadRequest)
		return
	}

	newID, err := s.store.Reclassify(id, req.Category)
	if err != nil {
		jsonError(w, "reclassifying", err, http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"id": newID, "category": req.Category})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, "q parameter is required", nil, http.StatusBadRequest)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	entries, err := s.store.DB().SearchText(query, limit)
	if err != nil {
		jsonError(w, "searching", err, http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []*store.Entry{}
	}
	jsonResponse(w, entries)
}

func (s *Server) handleSemanticSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, "q parameter is required", nil, http.StatusBadRequest)
		return
	}

	if s.store.Vec() == nil || !s.store.Vec().Enabled() {
		jsonError(w, "semantic search not available (no embedding backend configured)", nil, http.StatusServiceUnavailable)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 10
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	category := r.URL.Query().Get("category")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var results []store.SearchResult
	var err error
	if category != "" {
		results, err = s.store.Vec().SearchWithCategory(ctx, query, category, limit)
	} else {
		results, err = s.store.Vec().Search(ctx, query, limit)
	}

	if err != nil {
		jsonError(w, "semantic search", err, http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []store.SearchResult{}
	}
	jsonResponse(w, results)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.DB().Stats()
	if err != nil {
		jsonError(w, "getting stats", err, http.StatusInternalServerError)
		return
	}

	total := 0
	for _, count := range stats {
		total += count
	}

	vecCount := 0
	if s.store.Vec() != nil && s.store.Vec().Enabled() {
		vecCount = s.store.Vec().Count(r.Context())
	}

	jsonResponse(w, map[string]interface{}{
		"categories":  stats,
		"total":       total,
		"vec_count":   vecCount,
		"vec_enabled": s.store.Vec() != nil && s.store.Vec().Enabled(),
	})
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.store.DB().ListTags()
	if err != nil {
		jsonError(w, "listing tags", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, tags)
}

type archiveRequest struct {
	EntryIDs []string `json:"entry_ids"`
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ArchiveDir == "" {
		jsonError(w, "archive not configured (set BRAIN_ARCHIVE_DIR)", nil, http.StatusBadRequest)
		return
	}

	var req archiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	var exported []string
	for _, id := range req.EntryIDs {
		entry, err := s.store.DB().GetEntry(id)
		if err != nil {
			log.Printf("warning: skipping entry %s: %v", id, err)
			continue
		}
		relPath, err := s.store.ExportEntry(s.cfg.ArchiveDir, entry)
		if err != nil {
			log.Printf("warning: export failed for %s: %v", id, err)
			continue
		}
		exported = append(exported, relPath)
	}

	jsonResponse(w, map[string]interface{}{
		"exported": exported,
		"count":    len(exported),
	})
}

// --- Frontend ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// For now, serve a simple HTML page. Later this will be an embedded SPA.
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

// --- Helpers ---

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("warning: encoding response: %v", err)
	}
}

func jsonError(w http.ResponseWriter, msg string, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	detail := msg
	if err != nil {
		detail = fmt.Sprintf("%s: %v", msg, err)
	}
	json.NewEncoder(w).Encode(map[string]string{"error": detail})
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Brain</title>
<style>
  :root { --bg: #0f172a; --card: #1e293b; --text: #e2e8f0; --accent: #38bdf8; --dim: #64748b; --border: #334155; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, -apple-system, sans-serif; background: var(--bg); color: var(--text); min-height: 100vh; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
  h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
  .subtitle { color: var(--dim); margin-bottom: 2rem; }
  .stats { display: flex; gap: 1rem; flex-wrap: wrap; margin-bottom: 2rem; }
  .stat { background: var(--card); border: 1px solid var(--border); border-radius: 0.5rem; padding: 1rem 1.25rem; min-width: 120px; }
  .stat .number { font-size: 1.5rem; font-weight: 700; color: var(--accent); }
  .stat .label { font-size: 0.75rem; color: var(--dim); text-transform: uppercase; letter-spacing: 0.05em; }
  .search-bar { display: flex; gap: 0.5rem; margin-bottom: 2rem; }
  .search-bar input { flex: 1; padding: 0.75rem 1rem; border-radius: 0.5rem; border: 1px solid var(--border); background: var(--card); color: var(--text); font-size: 1rem; }
  .search-bar input:focus { outline: none; border-color: var(--accent); }
  .search-bar button { padding: 0.75rem 1.25rem; border-radius: 0.5rem; border: none; background: var(--accent); color: var(--bg); font-weight: 600; cursor: pointer; }
  .search-bar button:hover { opacity: 0.9; }
  .entries { display: flex; flex-direction: column; gap: 0.75rem; }
  .entry { background: var(--card); border: 1px solid var(--border); border-radius: 0.5rem; padding: 1rem 1.25rem; cursor: pointer; transition: border-color 0.15s; }
  .entry:hover { border-color: var(--accent); }
  .entry-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
  .entry-title { font-weight: 600; }
  .entry-category { font-size: 0.75rem; padding: 0.2rem 0.5rem; border-radius: 9999px; background: var(--bg); color: var(--accent); }
  .entry-body { font-size: 0.875rem; color: var(--dim); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .entry-meta { font-size: 0.75rem; color: var(--dim); margin-top: 0.5rem; }
  .tags { display: flex; gap: 0.25rem; margin-top: 0.5rem; }
  .tag { font-size: 0.7rem; padding: 0.1rem 0.4rem; border-radius: 9999px; background: var(--bg); color: var(--dim); border: 1px solid var(--border); }
  .capture { margin-bottom: 2rem; }
  .capture textarea { width: 100%; padding: 0.75rem 1rem; border-radius: 0.5rem; border: 1px solid var(--border); background: var(--card); color: var(--text); font-size: 1rem; resize: vertical; min-height: 80px; }
  .capture textarea:focus { outline: none; border-color: var(--accent); }
  .capture button { margin-top: 0.5rem; padding: 0.5rem 1rem; border-radius: 0.5rem; border: none; background: var(--accent); color: var(--bg); font-weight: 600; cursor: pointer; }
  .empty { text-align: center; padding: 3rem; color: var(--dim); }
  .tabs { display: flex; gap: 0.5rem; margin-bottom: 1.5rem; flex-wrap: wrap; }
  .tab { padding: 0.5rem 1rem; border-radius: 0.5rem; border: 1px solid var(--border); background: transparent; color: var(--dim); cursor: pointer; font-size: 0.875rem; }
  .tab.active { background: var(--accent); color: var(--bg); border-color: var(--accent); font-weight: 600; }
  .tab:hover:not(.active) { border-color: var(--accent); color: var(--text); }

  /* Modal */
  .modal-backdrop { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.6); z-index: 40; align-items: center; justify-content: center; }
  .modal-backdrop.open { display: flex; }
  .modal { background: var(--card); border: 1px solid var(--border); border-radius: 0.75rem; padding: 1.5rem; width: 90%; max-width: 600px; max-height: 80vh; overflow-y: auto; }
  .modal h2 { margin-bottom: 1rem; }
  .modal label { display: block; font-size: 0.875rem; color: var(--dim); margin-bottom: 0.25rem; margin-top: 0.75rem; }
  .modal input, .modal textarea, .modal select { width: 100%; padding: 0.5rem; border-radius: 0.375rem; border: 1px solid var(--border); background: var(--bg); color: var(--text); font-size: 0.875rem; }
  .modal textarea { min-height: 120px; resize: vertical; }
  .modal-actions { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 1rem; }
  .modal-actions button { padding: 0.5rem 1rem; border-radius: 0.375rem; border: none; cursor: pointer; font-weight: 600; }
  .btn-primary { background: var(--accent); color: var(--bg); }
  .btn-danger { background: #ef4444; color: white; }
  .btn-secondary { background: var(--border); color: var(--text); }
</style>
</head>
<body>
<div class="container">
  <h1>🧠 Brain</h1>
  <p class="subtitle">Your second brain — SQLite + semantic search</p>

  <div id="stats" class="stats"></div>

  <div class="capture">
    <textarea id="captureText" placeholder="Capture a thought..."></textarea>
    <button onclick="captureThought()">Save</button>
  </div>

  <div class="search-bar">
    <input type="text" id="searchInput" placeholder="Search thoughts..." onkeydown="if(event.key==='Enter')search()">
    <button onclick="search()">Search</button>
    <button onclick="semanticSearch()" title="Semantic search">🔮</button>
  </div>

  <div class="tabs" id="tabs"></div>
  <div class="entries" id="entries"></div>
</div>

<!-- Detail Modal -->
<div class="modal-backdrop" id="detailModal">
  <div class="modal">
    <h2 id="modalTitle"></h2>
    <label>Category</label>
    <select id="modalCategory">
      <option>people</option><option>projects</option><option>ideas</option>
      <option>actions</option><option>study</option><option>journal</option><option>inbox</option>
    </select>
    <label>Body</label>
    <textarea id="modalBody"></textarea>
    <label>Tags (comma-separated)</label>
    <input id="modalTags" type="text">
    <div class="entry-meta" id="modalMeta"></div>
    <div class="modal-actions">
      <button class="btn-danger" onclick="deleteEntry()">Delete</button>
      <button class="btn-secondary" onclick="closeModal()">Cancel</button>
      <button class="btn-primary" onclick="saveEntry()">Save</button>
    </div>
  </div>
</div>

<script>
let currentEntryId = null;
let currentFilter = '';

async function loadStats() {
  const res = await fetch('/api/stats');
  const data = await res.json();
  const el = document.getElementById('stats');
  el.innerHTML = '<div class="stat"><div class="number">' + data.total + '</div><div class="label">Total</div></div>';
  if (data.vec_enabled) {
    el.innerHTML += '<div class="stat"><div class="number">' + data.vec_count + '</div><div class="label">Embedded</div></div>';
  }
  const cats = data.categories || {};
  for (const [cat, count] of Object.entries(cats)) {
    el.innerHTML += '<div class="stat"><div class="number">' + count + '</div><div class="label">' + cat + '</div></div>';
  }

  // Build tabs
  const tabs = document.getElementById('tabs');
  tabs.innerHTML = '<button class="tab' + (currentFilter === '' ? ' active' : '') + '" onclick="filterCategory(\'\')">All</button>';
  for (const cat of Object.keys(cats).sort()) {
    tabs.innerHTML += '<button class="tab' + (currentFilter === cat ? ' active' : '') + '" onclick="filterCategory(\'' + cat + '\')">' + cat + '</button>';
  }
  tabs.innerHTML += '<button class="tab' + (currentFilter === 'review' ? ' active' : '') + '" onclick="filterCategory(\'review\')">⚠ Review</button>';
}

async function loadEntries(params) {
  const url = '/api/entries' + (params ? '?' + params : '');
  const res = await fetch(url);
  const entries = await res.json();
  renderEntries(entries);
}

function renderEntries(entries) {
  const el = document.getElementById('entries');
  if (!entries || entries.length === 0) {
    el.innerHTML = '<div class="empty">No thoughts yet. Capture one above.</div>';
    return;
  }
  el.innerHTML = entries.map(e => {
    const date = new Date(e.created_at).toLocaleDateString();
    const tags = (e.tags || []).map(t => '<span class="tag">' + t + '</span>').join('');
    const body = (e.body || '').substring(0, 120);
    return '<div class="entry" onclick="openEntry(\'' + e.id + '\')">' +
      '<div class="entry-header"><span class="entry-title">' + esc(e.title) + '</span>' +
      '<span class="entry-category">' + e.category + '</span></div>' +
      '<div class="entry-body">' + esc(body) + '</div>' +
      (tags ? '<div class="tags">' + tags + '</div>' : '') +
      '<div class="entry-meta">' + date + ' · ' + (e.source || 'unknown') + ' · ' + Math.round((e.confidence || 0) * 100) + '%</div>' +
      '</div>';
  }).join('');
}

function filterCategory(cat) {
  currentFilter = cat;
  if (cat === 'review') {
    loadEntries('needs_review=true');
  } else if (cat) {
    loadEntries('category=' + cat);
  } else {
    loadEntries('');
  }
  loadStats();
}

async function search() {
  const q = document.getElementById('searchInput').value.trim();
  if (!q) { loadEntries(''); return; }
  const res = await fetch('/api/search?q=' + encodeURIComponent(q));
  const entries = await res.json();
  renderEntries(entries);
}

async function semanticSearch() {
  const q = document.getElementById('searchInput').value.trim();
  if (!q) return;
  const res = await fetch('/api/search/semantic?q=' + encodeURIComponent(q));
  if (!res.ok) { alert('Semantic search unavailable'); return; }
  const results = await res.json();
  // Load full entries for results
  const entries = [];
  for (const r of results) {
    const eres = await fetch('/api/entries/' + r.entry_id);
    if (eres.ok) entries.push(await eres.json());
  }
  renderEntries(entries);
}

async function captureThought() {
  const text = document.getElementById('captureText').value.trim();
  if (!text) return;
  await fetch('/api/entries', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: text.substring(0, 60), body: text, category: 'inbox', source: 'web' })
  });
  document.getElementById('captureText').value = '';
  loadStats();
  loadEntries('');
}

async function openEntry(id) {
  const res = await fetch('/api/entries/' + id);
  if (!res.ok) return;
  const e = await res.json();
  currentEntryId = id;
  document.getElementById('modalTitle').textContent = e.title;
  document.getElementById('modalCategory').value = e.category;
  document.getElementById('modalBody').value = e.body || '';
  document.getElementById('modalTags').value = (e.tags || []).join(', ');
  document.getElementById('modalMeta').textContent =
    'Created: ' + new Date(e.created_at).toLocaleString() +
    ' · Source: ' + (e.source || 'unknown') +
    ' · Confidence: ' + Math.round((e.confidence || 0) * 100) + '%';
  document.getElementById('detailModal').classList.add('open');
}

function closeModal() {
  document.getElementById('detailModal').classList.remove('open');
  currentEntryId = null;
}

async function saveEntry() {
  if (!currentEntryId) return;
  const tagsRaw = document.getElementById('modalTags').value;
  const tags = tagsRaw ? tagsRaw.split(',').map(t => t.trim()).filter(Boolean) : [];
  await fetch('/api/entries/' + currentEntryId, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      category: document.getElementById('modalCategory').value,
      body: document.getElementById('modalBody').value,
      tags: tags,
    })
  });
  closeModal();
  loadStats();
  filterCategory(currentFilter);
}

async function deleteEntry() {
  if (!currentEntryId || !confirm('Delete this entry?')) return;
  await fetch('/api/entries/' + currentEntryId, { method: 'DELETE' });
  closeModal();
  loadStats();
  filterCategory(currentFilter);
}

function esc(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

// Close modal on backdrop click
document.getElementById('detailModal').addEventListener('click', function(e) {
  if (e.target === this) closeModal();
});

// Close modal on Escape
document.addEventListener('keydown', function(e) {
  if (e.key === 'Escape') closeModal();
});

// Initial load
loadStats();
loadEntries('');
</script>
</body>
</html>`
