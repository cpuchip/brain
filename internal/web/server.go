package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

// Server serves the brain web UI and REST API.
type Server struct {
	store      *store.Store
	cfg        *config.Config
	classify   *classifier.Classifier
	agent      *ai.Agent
	mux        *http.ServeMux
	srv        *http.Server
	frontendFS fs.FS
}

// NewServer creates a new web server.
func NewServer(st *store.Store, cfg *config.Config, cl *classifier.Classifier, agent *ai.Agent, frontendFS fs.FS) *Server {
	s := &Server{
		store:      st,
		cfg:        cfg,
		classify:   cl,
		agent:      agent,
		mux:        http.NewServeMux(),
		frontendFS: frontendFS,
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
	// API routes (wrapped with CORS)
	s.mux.HandleFunc("GET /api/entries", s.cors(s.handleListEntries))
	s.mux.HandleFunc("GET /api/entries/{id}", s.cors(s.handleGetEntry))
	s.mux.HandleFunc("POST /api/entries", s.cors(s.handleCreateEntry))
	s.mux.HandleFunc("PUT /api/entries/{id}", s.cors(s.handleUpdateEntry))
	s.mux.HandleFunc("DELETE /api/entries/{id}", s.cors(s.handleDeleteEntry))
	s.mux.HandleFunc("POST /api/entries/{id}/reclassify", s.cors(s.handleReclassify))
	s.mux.HandleFunc("POST /api/entries/{id}/classify", s.cors(s.handleClassify))
	s.mux.HandleFunc("POST /api/entries/{id}/subtasks", s.cors(s.handleCreateSubTask))
	s.mux.HandleFunc("PUT /api/entries/{id}/subtasks/{sid}", s.cors(s.handleUpdateSubTask))
	s.mux.HandleFunc("DELETE /api/entries/{id}/subtasks/{sid}", s.cors(s.handleDeleteSubTask))
	s.mux.HandleFunc("POST /api/entries/{id}/subtasks/reorder", s.cors(s.handleReorderSubTasks))
	s.mux.HandleFunc("GET /api/search", s.cors(s.handleSearch))
	s.mux.HandleFunc("GET /api/search/semantic", s.cors(s.handleSemanticSearch))
	s.mux.HandleFunc("GET /api/stats", s.cors(s.handleStats))
	s.mux.HandleFunc("GET /api/tags", s.cors(s.handleTags))
	s.mux.HandleFunc("POST /api/archive", s.cors(s.handleArchive))

	// Flutter app compatibility endpoints
	s.mux.HandleFunc("GET /api/brain/history", s.cors(s.handleBrainHistory))
	s.mux.HandleFunc("GET /api/brain/status", s.cors(s.handleBrainStatus))

	// Model profiles
	s.mux.HandleFunc("GET /api/models", s.cors(s.handleListModels))
	s.mux.HandleFunc("GET /api/models/active", s.cors(s.handleActiveModel))

	// Agent (Copilot SDK + MCP tools)
	s.mux.HandleFunc("POST /api/agent/ask", s.cors(s.handleAgentAsk))
	s.mux.HandleFunc("POST /api/agent/reset", s.cors(s.handleAgentReset))

	// CORS preflight
	s.mux.HandleFunc("OPTIONS /", s.handleCORSPreflight)

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
	if v, ok := updates["status"].(string); ok {
		existing.Status = v
	}
	if v, ok := updates["action_done"].(bool); ok {
		existing.ActionDone = v
	}
	if v, ok := updates["due_date"].(string); ok {
		existing.DueDate = v
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

// handleClassify runs AI classification on an existing entry's text.
// Returns the updated entry with new category, title, tags, etc.
func (s *Server) handleClassify(w http.ResponseWriter, r *http.Request) {
	if s.classify == nil {
		jsonError(w, "classifier not available", nil, http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	entry, err := s.store.DB().GetEntry(id)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	// Build text to classify from entry body (or title if body is empty)
	text := entry.Body
	if text == "" {
		text = entry.Title
	}

	classifyCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := s.classify.Classify(classifyCtx, text)
	if err != nil {
		jsonError(w, "classification failed", err, http.StatusInternalServerError)
		return
	}

	// If category changed, reclassify (moves the file in the store)
	if result.Category != entry.Category {
		newID, err := s.store.Reclassify(id, result.Category)
		if err != nil {
			jsonError(w, "reclassifying after AI classify", err, http.StatusInternalServerError)
			return
		}
		id = newID
		// Re-fetch after reclassify since ID may change
		entry, err = s.store.DB().GetEntry(id)
		if err != nil {
			jsonError(w, "entry not found after reclassify", err, http.StatusInternalServerError)
			return
		}
	}

	// Apply classification results to the entry
	entry.Title = result.Title
	entry.Confidence = result.Confidence
	entry.NeedsReview = s.classify.NeedsReview(result)
	if len(result.Tags) > 0 {
		entry.Tags = result.Tags
	}
	if result.Fields.DueDate != "" {
		entry.DueDate = result.Fields.DueDate
	}
	if result.Fields.NextAction != "" {
		entry.NextAction = result.Fields.NextAction
	}
	if result.Fields.Notes != "" && entry.Body == "" {
		entry.Body = result.Fields.Notes
	}

	if err := s.store.DB().UpdateEntry(entry); err != nil {
		jsonError(w, "updating entry after classify", err, http.StatusInternalServerError)
		return
	}

	// Create subtasks from extracted list items
	if len(result.SubItems) > 0 {
		for i, itemText := range result.SubItems {
			st := &store.SubTask{
				EntryID:   id,
				Text:      itemText,
				SortOrder: i,
			}
			if err := s.store.DB().InsertSubTask(st); err != nil {
				log.Printf("warning: subtask creation failed for %s: %v", id, err)
			}
		}
	}

	// Re-embed in vector store
	if s.store.Vec() != nil && s.store.Vec().Enabled() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.store.Vec().ReEmbed(ctx, entry); err != nil {
				log.Printf("warning: re-embed after classify failed for %s: %v", id, err)
			}
		}()
	}

	jsonResponse(w, entry)
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

// --- CORS ---

func (s *Server) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		next(w, r)
	}
}

func (s *Server) handleCORSPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// --- Flutter App Compatibility ---

// handleBrainHistory returns entries in the format the Flutter brain-app expects:
// {"messages": [{"id": "...", "text": "...", "category": "...", "title": "...", "confidence": 0.9, "created_at": "...", "processed": true}]}
func (s *Server) handleBrainHistory(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	entries, err := s.store.DB().ListAll(limit, 0)
	if err != nil {
		jsonError(w, "listing entries", err, http.StatusInternalServerError)
		return
	}

	type historyMsg struct {
		ID         string          `json:"id"`
		Text       string          `json:"text"`
		Category   string          `json:"category"`
		Title      string          `json:"title"`
		Confidence float64         `json:"confidence"`
		CreatedAt  string          `json:"created_at"`
		Processed  bool            `json:"processed"`
		ActionDone bool            `json:"action_done,omitempty"`
		Status     string          `json:"status,omitempty"`
		DueDate    string          `json:"due_date,omitempty"`
		NextAction string          `json:"next_action,omitempty"`
		Tags       []string        `json:"tags,omitempty"`
		SubTasks   []store.SubTask `json:"subtasks,omitempty"`
	}

	messages := make([]historyMsg, 0, len(entries))
	for _, e := range entries {
		// Load sub-tasks for each entry
		subtasks, _ := s.store.DB().ListSubTasks(e.ID)

		msg := historyMsg{
			ID:         e.ID,
			Text:       e.Body,
			Category:   e.Category,
			Title:      e.Title,
			Confidence: e.Confidence,
			CreatedAt:  e.Created.Format("2006-01-02T15:04:05Z"),
			Processed:  !e.NeedsReview,
			ActionDone: e.ActionDone,
			Status:     e.Status,
			DueDate:    e.DueDate,
			SubTasks:   subtasks,
		}
		messages = append(messages, msg)
	}

	jsonResponse(w, map[string]interface{}{
		"messages": messages,
	})
}

// handleBrainStatus returns brain status in the format the Flutter brain-app expects.
func (s *Server) handleBrainStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.DB().Stats()
	if err != nil {
		jsonError(w, "getting stats", err, http.StatusInternalServerError)
		return
	}

	total := 0
	for _, count := range stats {
		total += count
	}

	jsonResponse(w, map[string]interface{}{
		"agent_online":  true,
		"queued_count":  0,
		"model":         s.cfg.LMStudioModel,
		"total_entries": total,
		"categories":    stats,
	})
}

// --- Sub-task Handlers ---

func (s *Server) handleCreateSubTask(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	// Verify the entry exists
	if _, err := s.store.DB().GetEntry(entryID); err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	var req struct {
		Text      string `json:"text"`
		SortOrder int    `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		jsonError(w, "text is required", nil, http.StatusBadRequest)
		return
	}

	st := &store.SubTask{
		EntryID:   entryID,
		Text:      req.Text,
		SortOrder: req.SortOrder,
	}
	if err := s.store.DB().InsertSubTask(st); err != nil {
		jsonError(w, "creating subtask", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, st)
}

func (s *Server) handleUpdateSubTask(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	subtaskID := r.PathValue("sid")

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	// Load existing subtask list to find this one
	subtasks, err := s.store.DB().ListSubTasks(entryID)
	if err != nil {
		jsonError(w, "loading subtasks", err, http.StatusInternalServerError)
		return
	}
	var existing *store.SubTask
	for i := range subtasks {
		if subtasks[i].ID == subtaskID {
			existing = &subtasks[i]
			break
		}
	}
	if existing == nil {
		jsonError(w, "subtask not found", nil, http.StatusNotFound)
		return
	}

	// Apply partial updates
	if v, ok := updates["text"].(string); ok {
		existing.Text = v
	}
	if v, ok := updates["done"].(bool); ok {
		existing.Done = v
	}
	if v, ok := updates["sort_order"].(float64); ok {
		existing.SortOrder = int(v)
	}

	if err := s.store.DB().UpdateSubTask(existing); err != nil {
		jsonError(w, "updating subtask", err, http.StatusInternalServerError)
		return
	}

	jsonResponse(w, existing)
}

func (s *Server) handleDeleteSubTask(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	subtaskID := r.PathValue("sid")

	if err := s.store.DB().DeleteSubTask(entryID, subtaskID); err != nil {
		jsonError(w, "deleting subtask", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderSubTasks(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}
	if len(req.IDs) == 0 {
		jsonError(w, "ids array is required", nil, http.StatusBadRequest)
		return
	}

	if err := s.store.DB().ReorderSubTasks(entryID, req.IDs); err != nil {
		jsonError(w, "reordering subtasks", err, http.StatusInternalServerError)
		return
	}

	// Return updated list
	subtasks, err := s.store.DB().ListSubTasks(entryID)
	if err != nil {
		jsonError(w, "loading subtasks", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, subtasks)
}

// --- Frontend ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if s.frontendFS == nil {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Brain server running. Frontend not embedded."))
		return
	}

	fileServer := http.FileServer(http.FS(s.frontendFS))

	// Try serving the requested file; fall back to index.html for SPA routes
	path := r.URL.Path
	if path != "/" {
		f, err := s.frontendFS.Open(path[1:]) // strip leading /
		if err != nil {
			// Serve index.html for SPA routes
			r.URL.Path = "/"
		} else {
			f.Close()
		}
	}
	fileServer.ServeHTTP(w, r)
}

// --- Model Profile Handlers ---

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	type profileJSON struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Tasks       []string `json:"tasks"`
		Temperature float64  `json:"temperature"`
		Active      bool     `json:"active"`
	}

	activeModel := s.cfg.LMStudioModel
	profiles := classifier.ListProfiles()

	result := make([]profileJSON, 0, len(profiles))
	for _, p := range profiles {
		tasks := make([]string, len(p.Tasks))
		for i, t := range p.Tasks {
			tasks[i] = string(t)
		}
		result = append(result, profileJSON{
			ID:          p.ID,
			Name:        p.Name,
			Tasks:       tasks,
			Temperature: p.Temperature,
			Active:      p.ID == activeModel,
		})
	}

	jsonResponse(w, result)
}

func (s *Server) handleActiveModel(w http.ResponseWriter, r *http.Request) {
	active := s.cfg.LMStudioModel
	profile := classifier.LookupProfile(active)

	result := map[string]any{
		"model_id": active,
		"backend":  s.cfg.AIBackend,
	}
	if profile != nil {
		result["profile"] = profile.Name
		tasks := make([]string, len(profile.Tasks))
		for i, t := range profile.Tasks {
			tasks[i] = string(t)
		}
		result["tasks"] = tasks
	}

	jsonResponse(w, result)
}

// --- Agent Handlers ---

func (s *Server) handleAgentAsk(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		jsonError(w, "agent not available", nil, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", err, http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		jsonError(w, "prompt is required", nil, http.StatusBadRequest)
		return
	}

	response, err := s.agent.Ask(r.Context(), req.Prompt)
	if err != nil {
		jsonError(w, "agent error", err, http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"response": response})
}

func (s *Server) handleAgentReset(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		jsonError(w, "agent not available", nil, http.StatusServiceUnavailable)
		return
	}

	s.agent.Reset()
	jsonResponse(w, map[string]string{"status": "ok"})
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
