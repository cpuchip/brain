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

	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

// Server serves the brain web UI and REST API.
type Server struct {
	store      *store.Store
	cfg        *config.Config
	mux        *http.ServeMux
	srv        *http.Server
	frontendFS fs.FS
}

// NewServer creates a new web server.
func NewServer(st *store.Store, cfg *config.Config, frontendFS fs.FS) *Server {
	s := &Server{
		store:      st,
		cfg:        cfg,
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
	s.mux.HandleFunc("GET /api/search", s.cors(s.handleSearch))
	s.mux.HandleFunc("GET /api/search/semantic", s.cors(s.handleSemanticSearch))
	s.mux.HandleFunc("GET /api/stats", s.cors(s.handleStats))
	s.mux.HandleFunc("GET /api/tags", s.cors(s.handleTags))
	s.mux.HandleFunc("POST /api/archive", s.cors(s.handleArchive))

	// Flutter app compatibility endpoints
	s.mux.HandleFunc("GET /api/brain/history", s.cors(s.handleBrainHistory))
	s.mux.HandleFunc("GET /api/brain/status", s.cors(s.handleBrainStatus))

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
		ID         string  `json:"id"`
		Text       string  `json:"text"`
		Category   string  `json:"category"`
		Title      string  `json:"title"`
		Confidence float64 `json:"confidence"`
		CreatedAt  string  `json:"created_at"`
		Processed  bool    `json:"processed"`
	}

	messages := make([]historyMsg, 0, len(entries))
	for _, e := range entries {
		messages = append(messages, historyMsg{
			ID:         e.ID,
			Text:       e.Body,
			Category:   e.Category,
			Title:      e.Title,
			Confidence: e.Confidence,
			CreatedAt:  e.Created.Format("2006-01-02T15:04:05Z"),
			Processed:  !e.NeedsReview,
		})
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

