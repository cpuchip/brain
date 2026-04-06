package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/pipeline"
	"github.com/cpuchip/brain/internal/store"
)

// Server serves the brain web UI and REST API.
type Server struct {
	store      *store.Store
	cfg        *config.Config
	classify   *classifier.Classifier
	pool       *ai.AgentPool
	pipeline   *pipeline.Pipeline
	wc         config.WorkspaceConfig
	mux        *http.ServeMux
	srv        *http.Server
	frontendFS fs.FS
	shutdownCh chan<- struct{}
}

// NewServer creates a new web server.
func NewServer(st *store.Store, cfg *config.Config, cl *classifier.Classifier, pool *ai.AgentPool, wc config.WorkspaceConfig, frontendFS fs.FS, shutdownCh chan<- struct{}) *Server {
	s := &Server{
		store:      st,
		cfg:        cfg,
		classify:   cl,
		pool:       pool,
		wc:         wc,
		mux:        http.NewServeMux(),
		frontendFS: frontendFS,
		shutdownCh: shutdownCh,
	}
	if pool != nil {
		s.pipeline = pipeline.New(st, pool, cfg, wc)
		s.pipeline.StartReviewLoop(pipeline.DefaultReviewConfig())
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
	if s.pipeline != nil {
		s.pipeline.Stop()
	}
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

	// Entry version history
	s.mux.HandleFunc("GET /api/entries/{id}/versions", s.cors(s.handleEntryVersions))

	// Flutter app compatibility endpoints
	s.mux.HandleFunc("GET /api/brain/history", s.cors(s.handleBrainHistory))
	s.mux.HandleFunc("GET /api/brain/status", s.cors(s.handleBrainStatus))

	// Model profiles
	s.mux.HandleFunc("GET /api/models", s.cors(s.handleListModels))
	s.mux.HandleFunc("GET /api/models/active", s.cors(s.handleActiveModel))

	// Agent (Copilot SDK + MCP tools)
	s.mux.HandleFunc("POST /api/agent/ask", s.cors(s.handleAgentAsk))
	s.mux.HandleFunc("POST /api/agent/reset", s.cors(s.handleAgentReset))
	s.mux.HandleFunc("GET /api/agent/sessions", s.cors(s.handleAgentSessions))
	s.mux.HandleFunc("POST /api/agent/route", s.cors(s.handleAgentRoute))
	s.mux.HandleFunc("GET /api/agent/routable", s.cors(s.handleAgentRoutable))
	s.mux.HandleFunc("GET /api/agent/running", s.cors(s.handleAgentRunning))
	s.mux.HandleFunc("GET /api/agent/review", s.cors(s.handleAgentReviewQueue))
	s.mux.HandleFunc("POST /api/agent/review/{id}", s.cors(s.handleAgentReviewAction))

	// Pipeline maturity
	s.mux.HandleFunc("POST /api/pipeline/advance", s.cors(s.handlePipelineAdvance))
	s.mux.HandleFunc("GET /api/pipeline/review/{id}", s.cors(s.handlePipelineReview))

	// Execution gate (Phase 4e)
	s.mux.HandleFunc("POST /api/entries/{id}/execute", s.cors(s.handleExecute))
	s.mux.HandleFunc("POST /api/entries/{id}/verify", s.cors(s.handleVerify))
	s.mux.HandleFunc("GET /api/entries/{id}/execution-context", s.cors(s.handleExecutionContext))

	// Projects
	s.mux.HandleFunc("GET /api/projects", s.cors(s.handleListProjects))
	s.mux.HandleFunc("POST /api/projects", s.cors(s.handleCreateProject))
	s.mux.HandleFunc("GET /api/projects/{id}", s.cors(s.handleGetProject))
	s.mux.HandleFunc("PUT /api/projects/{id}", s.cors(s.handleUpdateProject))
	s.mux.HandleFunc("DELETE /api/projects/{id}", s.cors(s.handleDeleteProject))
	s.mux.HandleFunc("GET /api/projects/{id}/entries", s.cors(s.handleProjectEntries))
	s.mux.HandleFunc("GET /api/projects/{id}/stats", s.cors(s.handleProjectStats))
	s.mux.HandleFunc("PUT /api/entries/{id}/project", s.cors(s.handleSetEntryProject))

	// Session messages (iterative turns)
	s.mux.HandleFunc("GET /api/entries/{id}/messages", s.cors(s.handleListMessages))
	s.mux.HandleFunc("POST /api/entries/{id}/reply", s.cors(s.handleReply))
	s.mux.HandleFunc("POST /api/entries/{id}/complete", s.cors(s.handleMarkComplete))
	s.mux.HandleFunc("GET /api/entries/your-turn", s.cors(s.handleYourTurn))
	s.mux.HandleFunc("GET /api/entries/{id}/context", s.cors(s.handleEntryContext))

	// Scheduled tasks
	s.mux.HandleFunc("GET /api/scheduled", s.cors(s.handleListScheduledTasks))
	s.mux.HandleFunc("POST /api/scheduled", s.cors(s.handleCreateScheduledTask))
	s.mux.HandleFunc("GET /api/scheduled/{id}", s.cors(s.handleGetScheduledTask))
	s.mux.HandleFunc("PUT /api/scheduled/{id}", s.cors(s.handleUpdateScheduledTask))
	s.mux.HandleFunc("DELETE /api/scheduled/{id}", s.cors(s.handleDeleteScheduledTask))
	s.mux.HandleFunc("GET /api/scheduled/{id}/runs", s.cors(s.handleListTaskRuns))
	s.mux.HandleFunc("POST /api/scheduled/{id}/run", s.cors(s.handleTriggerTaskRun))

	// Library (agents, skills, docs)
	s.mux.HandleFunc("GET /api/library/agents", s.cors(s.handleLibraryAgents))
	s.mux.HandleFunc("GET /api/library/skills", s.cors(s.handleLibrarySkills))
	s.mux.HandleFunc("GET /api/library/memory", s.cors(s.handleLibraryMemory))

	// Activity feed
	s.mux.HandleFunc("GET /api/activity", s.cors(s.handleActivity))

	// Dashboard operations
	s.mux.HandleFunc("POST /api/entries/{id}/dismiss-route", s.cors(s.handleDismissRoute))
	s.mux.HandleFunc("POST /api/shutdown", s.cors(s.handleShutdown))

	// File serving (workspace file viewer)
	s.mux.HandleFunc("GET /api/files/read", s.cors(s.handleFileRead))

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
	} else if r.URL.Query().Get("unassigned") == "true" {
		entries, err = s.store.DB().ListUnassigned(limit)
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
	// Preserve raw input text — never modified by classification.
	if entry.Body != "" {
		entry.OriginalBody = entry.Body
	} else {
		entry.OriginalBody = entry.Title
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
	if v, ok := updates["project_id"]; ok {
		if v == nil {
			existing.ProjectID = nil
		} else if pid, ok := v.(float64); ok {
			id := int(pid)
			existing.ProjectID = &id
		}
	}

	// Pipeline fields — update via dedicated DB methods after the main update
	var setMaturity, setMaturityNotes string
	var hasMaturity bool
	if v, ok := updates["maturity"].(string); ok {
		setMaturity = v
		hasMaturity = true
	}
	if v, ok := updates["maturity_notes"].(string); ok {
		setMaturityNotes = v
	}
	var setRouteStatus string
	var hasRouteStatus bool
	if v, ok := updates["route_status"].(string); ok {
		setRouteStatus = v
		hasRouteStatus = true
	}

	if err := s.store.DB().UpdateEntry(existing); err != nil {
		jsonError(w, "updating entry", err, http.StatusInternalServerError)
		return
	}

	if hasMaturity {
		if err := s.store.DB().SetMaturity(id, setMaturity, setMaturityNotes); err != nil {
			jsonError(w, "setting maturity", err, http.StatusInternalServerError)
			return
		}
		existing.Maturity = setMaturity
		existing.MaturityNotes = setMaturityNotes
	}

	if hasRouteStatus {
		if err := s.store.DB().UpdateRouteStatus(id, setRouteStatus); err != nil {
			jsonError(w, "setting route status", err, http.StatusInternalServerError)
			return
		}
		existing.RouteStatus = setRouteStatus
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

	// Load active projects for auto-assignment
	projects, _ := s.store.DB().ListProjects()
	var projCtx []classifier.ProjectContext
	for _, p := range projects {
		if p.Status == "active" {
			projCtx = append(projCtx, classifier.ProjectContext{ID: p.ID, Name: p.Name, Description: p.Description})
		}
	}

	result, err := s.classify.Classify(classifyCtx, text, projCtx)
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
	// Preserve raw input: if body is empty but title carried the raw text,
	// move the original title into body before classification overwrites it.
	if entry.Body == "" && entry.Title != "" {
		entry.Body = entry.Title
	}
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

	// Auto-assign project if classifier suggested one and entry has no project yet
	if result.ProjectID != nil && entry.ProjectID == nil {
		entry.ProjectID = result.ProjectID
	}

	if err := s.store.DB().UpdateEntry(entry); err != nil {
		jsonError(w, "updating entry after classify", err, http.StatusInternalServerError)
		return
	}

	// Auto-annotate routing: if this category has an agent route, decide whether to
	// auto-route immediately or just mark as suggested for manual triggering.
	route := ai.LookupRoute(entry.Category)
	if route.AgentName != "" && route.Mode != ai.RouteModeNone {
		if s.pool != nil && s.cfg.AutoRouteEnabled && route.Mode == ai.RouteModeAuto {
			// Auto-route: mark the agent and kick off routing immediately
			_ = s.store.SetAgentRoute(entry.ID, route.AgentName, ai.RouteStatusPending)
			entry.AgentRoute = route.AgentName
			entry.RouteStatus = ai.RouteStatusPending
			s.routeEntry(entry, route)
			log.Printf("Auto-routed entry %s to agent %s", entry.ID, route.AgentName)
		} else {
			// Suggest mode or auto-route disabled: mark for manual trigger
			_ = s.store.SetAgentRoute(entry.ID, route.AgentName, ai.RouteStatusSuggested)
			entry.AgentRoute = route.AgentName
			entry.RouteStatus = ai.RouteStatusSuggested
		}
	}

	// Post-classification maturity assessment for pipeline categories
	maturity := classifier.AssessMaturity(result)
	if maturity != "" {
		entry.Maturity = string(maturity)
		if err := s.store.DB().SetMaturity(entry.ID, string(maturity), ""); err != nil {
			log.Printf("warning: maturity assessment failed for %s: %v", id, err)
		}
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
		"categories":       stats,
		"total":            total,
		"unassigned_count": s.store.DB().CountUnassigned(),
		"vec_count":        vecCount,
		"vec_enabled":      s.store.Vec() != nil && s.store.Vec().Enabled(),
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

func (s *Server) handleEntryVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	versions, err := s.store.DB().ListVersions(id)
	if err != nil {
		jsonError(w, "listing versions", err, http.StatusInternalServerError)
		return
	}
	if versions == nil {
		versions = []map[string]any{}
	}
	jsonResponse(w, versions)
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
	if s.pool == nil {
		jsonError(w, "agent not available", nil, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
		Agent  string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", err, http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		jsonError(w, "prompt is required", nil, http.StatusBadRequest)
		return
	}

	agent := s.pool.GetOrCreate(req.Agent, s.wc)
	response, err := agent.Ask(r.Context(), req.Prompt)
	if err != nil {
		jsonError(w, "agent error", err, http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"response": response})
}

func (s *Server) handleAgentReset(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		jsonError(w, "agent not available", nil, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Agent string `json:"agent"`
	}
	// Body is optional — if empty, reset all
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Agent != "" {
		s.pool.Reset(req.Agent)
		jsonResponse(w, map[string]string{"status": "ok", "reset": req.Agent})
	} else {
		s.pool.ResetAll()
		jsonResponse(w, map[string]string{"status": "ok", "reset": "all"})
	}
}

func (s *Server) handleAgentSessions(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		jsonResponse(w, map[string]any{"sessions": []string{}, "details": []any{}})
		return
	}

	sessions := s.pool.ActiveSessions()
	details := s.pool.SessionSummaries()
	jsonResponse(w, map[string]any{
		"sessions": sessions,
		"details":  details,
		"budgets": map[string]int64{
			"warning": s.cfg.AgentTokenWarning,
		},
	})
}

func (s *Server) handleAgentRoute(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		jsonError(w, "agent not available", nil, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		EntryID string `json:"entry_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", err, http.StatusBadRequest)
		return
	}
	if req.EntryID == "" {
		jsonError(w, "entry_id is required", nil, http.StatusBadRequest)
		return
	}

	// Load the entry
	entry, err := s.store.DB().GetEntry(req.EntryID)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	// Check if there's a route for this entry
	route := ai.LookupRoute(entry.Category)
	if route.AgentName == "" || route.Mode == ai.RouteModeNone {
		jsonError(w, "no agent route for category: "+entry.Category, nil, http.StatusBadRequest)
		return
	}

	s.routeEntry(entry, route)

	jsonResponse(w, map[string]string{
		"status":   "routed",
		"agent":    route.AgentName,
		"entry_id": req.EntryID,
	})
}

// routeEntry runs agent routing for an entry in the background.
// It marks the entry as pending, then spawns a goroutine to run the agent.
func (s *Server) routeEntry(entry *store.Entry, route ai.RouteRule) {
	_ = s.store.UpdateRouteStatus(entry.ID, ai.RouteStatusPending)

	go func() {
		ctx := s.pool.StartTask(entry.ID, route.AgentName)
		defer s.pool.FinishTask(entry.ID)

		agent := s.pool.GetOrCreate(route.AgentName, s.wc)

		// Build project context if entry belongs to a project
		projectCtx := ""
		if s.pipeline != nil {
			projectCtx = pipeline.FormatProjectContext(s.pipeline.BuildProjectContext(entry))
		}

		prompt := route.RenderPrompt(ai.RoutePromptData{
			Title:          entry.Title,
			Body:           entry.Body,
			ProjectContext: projectCtx,
		})

		_ = s.store.UpdateRouteStatus(entry.ID, ai.RouteStatusRunning)

		response, err := agent.Ask(ctx, prompt)
		if err != nil {
			log.Printf("Agent route failed for entry %s: %v", entry.ID, err)
			_ = s.store.UpdateRouteStatus(entry.ID, ai.RouteStatusFailed)
			return
		}

		_ = s.store.SetAgentOutput(entry.ID, response, 0)
		log.Printf("Agent route complete for entry %s (agent: %s, %d chars)", entry.ID, route.AgentName, len(response))
	}()
}

// CreateAndRouteEntry creates a new entry and routes it to an agent. Used by the scheduler.
func (s *Server) CreateAndRouteEntry(title, body, category, agentName string, projectID *int) (string, error) {
	entry := &store.Entry{
		Title:        title,
		Category:     category,
		Body:         body,
		OriginalBody: body,
		Source:       "scheduler",
		ProjectID:    projectID,
	}
	id, err := s.store.DB().InsertEntry(entry)
	if err != nil {
		return "", fmt.Errorf("creating entry: %w", err)
	}
	entry.ID = id

	// Route to the specified agent
	if s.pool != nil && agentName != "" {
		route := ai.RouteRule{
			AgentName:      agentName,
			Mode:           ai.RouteModeSuggest,
			PromptTemplate: "{{.Body}}",
		}
		s.routeEntry(entry, route)
	}

	return id, nil
}

func (s *Server) handleAgentRoutable(w http.ResponseWriter, r *http.Request) {
	type routableEntry struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Category  string `json:"category"`
		AgentName string `json:"agent_name"`
	}

	var result []routableEntry
	for category, route := range ai.DefaultRoutes {
		if route.AgentName == "" || route.Mode == ai.RouteModeNone {
			continue
		}
		entries, err := s.store.DB().ListCategory(category)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// Skip entries already routed or dismissed
			if e.RouteStatus == ai.RouteStatusComplete || e.RouteStatus == ai.RouteStatusRunning || e.RouteStatus == ai.RouteStatusPending || e.RouteStatus == ai.RouteStatusDismissed {
				continue
			}
			result = append(result, routableEntry{
				ID:        e.ID,
				Title:     e.Title,
				Category:  e.Category,
				AgentName: route.AgentName,
			})
		}
	}

	jsonResponse(w, map[string]any{"entries": result})
}

func (s *Server) handleAgentRunning(w http.ResponseWriter, r *http.Request) {
	type runningEntry struct {
		EntryID   string `json:"entry_id"`
		AgentName string `json:"agent_name"`
	}

	var result []runningEntry
	if s.pool != nil {
		for _, t := range s.pool.RunningTasks() {
			result = append(result, runningEntry{
				EntryID:   t.EntryID,
				AgentName: t.AgentName,
			})
		}
	}
	if result == nil {
		result = []runningEntry{}
	}
	jsonResponse(w, map[string]any{"entries": result})
}

func (s *Server) handleDismissRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.UpdateRouteStatus(id, ai.RouteStatusDismissed); err != nil {
		jsonError(w, "dismissing route", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "dismissed", "entry_id": id})
}

func (s *Server) handleAgentReviewQueue(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListByRouteStatus(ai.RouteStatusComplete)
	if err != nil {
		jsonError(w, "listing review queue", err, http.StatusInternalServerError)
		return
	}

	type reviewEntry struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Category    string `json:"category"`
		AgentRoute  string `json:"agent_route"`
		AgentOutput string `json:"agent_output"`
		TokensUsed  int64  `json:"tokens_used"`
		Body        string `json:"body"`
		Updated     string `json:"updated_at"`
	}
	result := make([]reviewEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, reviewEntry{
			ID:          e.ID,
			Title:       e.Title,
			Category:    e.Category,
			AgentRoute:  e.AgentRoute,
			AgentOutput: e.AgentOutput,
			TokensUsed:  e.TokensUsed,
			Body:        e.Body,
			Updated:     e.Updated.Format(time.RFC3339),
		})
	}
	jsonResponse(w, map[string]any{"entries": result})
}

func (s *Server) handleAgentReviewAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Action string `json:"action"` // "accept" or "reject"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", err, http.StatusBadRequest)
		return
	}

	var newStatus string
	switch req.Action {
	case "accept":
		newStatus = ai.RouteStatusAccepted
	case "reject":
		newStatus = ai.RouteStatusRejected
	default:
		jsonError(w, "action must be 'accept' or 'reject'", nil, http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateRouteStatus(id, newStatus); err != nil {
		jsonError(w, "updating review status", err, http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": newStatus, "entry_id": id})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if s.pool != nil {
		s.pool.CancelAll()
	}

	jsonResponse(w, map[string]string{"status": "shutting_down"})

	// Signal the main goroutine to shut down after response flushes
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.shutdownCh <- struct{}{}
	}()
}

// handleFileRead serves workspace files with path traversal protection.
func (s *Server) handleFileRead(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}

	// Normalize path separators for cross-platform safety
	relPath = filepath.FromSlash(relPath)

	// Security: clean the path
	cleaned := filepath.Clean(relPath)

	// Reject absolute paths
	if filepath.IsAbs(cleaned) {
		http.Error(w, "invalid path", http.StatusForbidden)
		return
	}

	// Reject path traversal
	if strings.Contains(cleaned, "..") {
		http.Error(w, "invalid path", http.StatusForbidden)
		return
	}

	// Compute workspace root from BrainCodeDir (scripts/brain → scripture-study)
	workspaceRoot := ""
	if s.cfg.BrainCodeDir != "" {
		scriptsDir := filepath.Dir(s.cfg.BrainCodeDir)
		workspaceRoot = filepath.Dir(scriptsDir)
	}
	if workspaceRoot == "" {
		http.Error(w, "workspace root not configured", http.StatusInternalServerError)
		return
	}

	fullPath := filepath.Join(workspaceRoot, cleaned)

	// Double-check resolved path is under workspace root
	absRoot, _ := filepath.Abs(workspaceRoot)
	absPath, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else {
			http.Error(w, "read error", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
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

// --- Pipeline Handlers ---

func (s *Server) handlePipelineAdvance(w http.ResponseWriter, r *http.Request) {
	var req pipeline.AdvanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", err, http.StatusBadRequest)
		return
	}

	if req.EntryID == "" {
		jsonError(w, "id is required", nil, http.StatusBadRequest)
		return
	}
	if req.Action == "" {
		jsonError(w, "action is required", nil, http.StatusBadRequest)
		return
	}

	if s.pipeline == nil {
		jsonError(w, "pipeline not available (no agent pool)", nil, http.StatusServiceUnavailable)
		return
	}

	result, err := s.pipeline.Advance(r.Context(), req)
	if err != nil {
		jsonError(w, "pipeline advance failed", err, http.StatusBadRequest)
		return
	}

	jsonResponse(w, result)
}

func (s *Server) handlePipelineReview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	entry, err := s.store.ReadEntry(id)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}

	review := map[string]any{
		"id":             entry.ID,
		"title":          entry.Title,
		"category":       entry.Category,
		"body":           entry.Body,
		"maturity":       maturity,
		"maturity_notes": entry.MaturityNotes,
		"scratch_path":   entry.ScratchPath,
		"scenarios":      entry.Scenarios,
		"tags":           entry.Tags,
		"created_at":     entry.Created,
		"updated_at":     entry.Updated,
	}

	jsonResponse(w, review)
}

// --- Project handlers ---

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.DB().ListProjects()
	if err != nil {
		jsonError(w, "listing projects", err, http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []*store.Project{}
	}
	jsonResponse(w, projects)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Emoji       string `json:"emoji"`
		ContextFile string `json:"context_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		jsonError(w, "name is required", nil, http.StatusBadRequest)
		return
	}

	p := &store.Project{
		Name:        req.Name,
		Description: req.Description,
		Status:      "active",
		Emoji:       req.Emoji,
		ContextFile: req.ContextFile,
	}
	id, err := s.store.DB().CreateProject(p)
	if err != nil {
		jsonError(w, "creating project", err, http.StatusInternalServerError)
		return
	}
	p.ID = id
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, p)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid project id", err, http.StatusBadRequest)
		return
	}
	p, err := s.store.DB().GetProject(id)
	if err != nil {
		jsonError(w, "project not found", err, http.StatusNotFound)
		return
	}
	jsonResponse(w, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid project id", err, http.StatusBadRequest)
		return
	}

	existing, err := s.store.DB().GetProject(id)
	if err != nil {
		jsonError(w, "project not found", err, http.StatusNotFound)
		return
	}

	// Partial update — read-modify-write
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	if v, ok := updates["name"].(string); ok {
		existing.Name = v
	}
	if v, ok := updates["description"].(string); ok {
		existing.Description = v
	}
	if v, ok := updates["status"].(string); ok {
		existing.Status = v
	}
	if v, ok := updates["emoji"].(string); ok {
		existing.Emoji = v
	}
	if v, ok := updates["context_file"].(string); ok {
		existing.ContextFile = v
	}

	if err := s.store.DB().UpdateProject(existing); err != nil {
		jsonError(w, "updating project", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, existing)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid project id", err, http.StatusBadRequest)
		return
	}
	if err := s.store.DB().DeleteProject(id); err != nil {
		jsonError(w, "deleting project", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProjectEntries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid project id", err, http.StatusBadRequest)
		return
	}
	entries, err := s.store.DB().ListEntriesByProject(id)
	if err != nil {
		jsonError(w, "listing project entries", err, http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []*store.Entry{}
	}
	jsonResponse(w, entries)
}

func (s *Server) handleProjectStats(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid project id", err, http.StatusBadRequest)
		return
	}
	stats, err := s.store.DB().GetProjectStats(id)
	if err != nil {
		jsonError(w, "getting project stats", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, stats)
}

func (s *Server) handleSetEntryProject(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	var req struct {
		ProjectID *int `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	if err := s.store.DB().SetEntryProject(entryID, req.ProjectID); err != nil {
		jsonError(w, "setting entry project", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"entry_id": entryID, "project_id": req.ProjectID})
}

// --- Session Messages (Iterative Turns) ---

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	msgs, err := s.store.DB().ListSessionMessages(entryID)
	if err != nil {
		jsonError(w, "listing messages", err, http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = []*store.SessionMessage{}
	}
	jsonResponse(w, msgs)
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		jsonError(w, "content is required", nil, http.StatusBadRequest)
		return
	}

	id, err := s.store.DB().AddSessionMessage(entryID, "human", req.Content)
	if err != nil {
		jsonError(w, "adding reply", err, http.StatusInternalServerError)
		return
	}

	// Set status to your_turn — the human has responded, awaiting next action
	if err := s.store.DB().UpdateRouteStatus(entryID, "your_turn"); err != nil {
		jsonError(w, "updating route status", err, http.StatusInternalServerError)
		return
	}

	// Auto-advance: if replying to a review-nudged pipeline entry with substance,
	// trigger the next pipeline stage asynchronously
	if s.pipeline != nil {
		go s.tryReplyAutoAdvance(entryID, req.Content)
	}

	jsonResponse(w, map[string]any{"id": id, "entry_id": entryID, "role": "human"})
}

// tryReplyAutoAdvance checks if a reply to a review-nudged entry should trigger
// an automatic pipeline advance (research pass for raw, plan pass for researched).
func (s *Server) tryReplyAutoAdvance(entryID, replyContent string) {
	entry, err := s.store.DB().GetEntry(entryID)
	if err != nil {
		return
	}

	// Only auto-advance entries that were pushed back by the review agent
	if entry.AgentRoute != "review" {
		return
	}

	// Only pipeline categories qualify
	if !classifier.PipelineCategories[entry.Category] {
		return
	}

	// Need substantive reply (>50 chars) to justify burning a premium request
	if len(strings.TrimSpace(replyContent)) < 50 {
		return
	}

	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}

	var action pipeline.AdvanceAction
	switch maturity {
	case "raw":
		action = pipeline.ActionAdvance // raw → researched
	case "researched":
		action = pipeline.ActionAdvance // researched → planned
	default:
		return // planned/specced don't auto-advance from reply
	}

	log.Printf("Reply auto-advance: triggering %s for entry %s (%s, %s maturity)",
		action, entryID, entry.Title, maturity)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := s.pipeline.Advance(ctx, pipeline.AdvanceRequest{
		EntryID:  entryID,
		Action:   action,
		Feedback: replyContent,
	})
	if err != nil {
		log.Printf("Reply auto-advance failed for %s: %v", entryID, err)
		// Post failure as session message so the user sees what happened
		s.store.DB().AddSessionMessage(entryID, "agent",
			fmt.Sprintf("Auto-advance failed: %v. You can advance manually from the pipeline controls.", err))
		return
	}

	// Post success message
	s.store.DB().AddSessionMessage(entryID, "agent",
		fmt.Sprintf("Auto-advanced: %s → %s. %s", result.OldMaturity, result.NewMaturity, result.Message))
	log.Printf("Reply auto-advance: %s advanced %s → %s", entryID, result.OldMaturity, result.NewMaturity)
}

func (s *Server) handleMarkComplete(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	if err := s.store.DB().UpdateRouteStatus(entryID, "complete"); err != nil {
		jsonError(w, "marking complete", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"entry_id": entryID, "status": "complete"})
}

func (s *Server) handleEntryContext(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	entry, err := s.store.DB().GetEntry(entryID)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	result := map[string]any{
		"entry_id": entryID,
		"title":    entry.Title,
		"category": entry.Category,
		"maturity": entry.Maturity,
	}

	if s.pipeline != nil {
		ctx := s.pipeline.BuildProjectContext(entry)
		if ctx != nil {
			result["project"] = map[string]any{
				"name":        ctx.ProjectName,
				"description": ctx.Description,
				"siblings":    ctx.SiblingEntries,
				"context_doc": ctx.ContextDoc != "",
			}
			result["formatted"] = pipeline.FormatProjectContext(ctx)
		}
	}

	jsonResponse(w, result)
}

// --- Execution Gate Handlers (Phase 4e) ---

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	if s.pipeline == nil {
		jsonError(w, "pipeline not available (no agent pool)", nil, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Feedback string `json:"feedback,omitempty"`
	}
	// Body is optional — allow empty
	_ = json.NewDecoder(r.Body).Decode(&req)

	result, err := s.pipeline.Execute(r.Context(), pipeline.ExecuteRequest{
		EntryID:  entryID,
		Feedback: req.Feedback,
	})
	if err != nil {
		jsonError(w, "execution failed to start", err, http.StatusBadRequest)
		return
	}

	jsonResponse(w, result)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	if s.pipeline == nil {
		jsonError(w, "pipeline not available", nil, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Results []pipeline.ScenarioResult `json:"results"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", err, http.StatusBadRequest)
		return
	}

	result, err := s.pipeline.Verify(pipeline.VerifyRequest{
		EntryID: entryID,
		Results: req.Results,
	})
	if err != nil {
		jsonError(w, "verification failed", err, http.StatusBadRequest)
		return
	}

	jsonResponse(w, result)
}

func (s *Server) handleExecutionContext(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")

	entry, err := s.store.DB().GetEntry(entryID)
	if err != nil {
		jsonError(w, "entry not found", err, http.StatusNotFound)
		return
	}

	if entry.Maturity != "specced" && entry.Maturity != "executing" {
		jsonError(w, "entry must be specced or executing to preview execution context", nil, http.StatusBadRequest)
		return
	}

	prompt := ""
	if s.pipeline != nil {
		prompt = s.pipeline.BuildExecutionContext(entry, "")
	}

	// Parse scenarios into a list for the frontend
	scenarios := parseScenarios(entry.Scenarios)

	jsonResponse(w, map[string]any{
		"entry_id":    entryID,
		"title":       entry.Title,
		"maturity":    entry.Maturity,
		"scenarios":   scenarios,
		"model":       pipeline.ExecuteModel,
		"cost":        1.0, // Sonnet premium request cost
		"prompt":      prompt,
		"has_scratch": entry.ScratchPath != "",
	})
}

// parseScenarios splits the scenarios string into individual scenario lines.
func parseScenarios(raw string) []string {
	if raw == "" {
		return nil
	}
	var scenarios []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip leading bullet/number
		if strings.HasPrefix(line, "- ") {
			line = line[2:]
		} else if strings.HasPrefix(line, "• ") {
			line = strings.TrimPrefix(line, "• ")
		} else if len(line) > 2 && line[0] >= '1' && line[0] <= '9' && line[1] == '.' {
			line = strings.TrimSpace(line[2:])
		}
		if line != "" {
			scenarios = append(scenarios, line)
		}
	}
	return scenarios
}

func (s *Server) handleYourTurn(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListByRouteStatus(ai.RouteStatusYourTurn)
	if err != nil {
		jsonError(w, "listing your-turn entries", err, http.StatusInternalServerError)
		return
	}

	type turnEntry struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Category   string `json:"category"`
		AgentRoute string `json:"agent_route"`
		Body       string `json:"body"`
		UpdatedAt  string `json:"updated_at"`
	}

	result := make([]turnEntry, 0, len(entries))
	for _, e := range entries {
		bodyPreview := e.Body
		if len(bodyPreview) > 200 {
			bodyPreview = bodyPreview[:200]
		}
		result = append(result, turnEntry{
			ID:         e.ID,
			Title:      e.Title,
			Category:   e.Category,
			AgentRoute: e.AgentRoute,
			Body:       bodyPreview,
			UpdatedAt:  e.Updated.Format(time.RFC3339),
		})
	}
	jsonResponse(w, map[string]any{"entries": result})
}

// --- Scheduled Tasks Handlers ---

func (s *Server) handleListScheduledTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.store.DB().ListScheduledTasks()
	if err != nil {
		jsonError(w, "listing scheduled tasks", err, http.StatusInternalServerError)
		return
	}
	if tasks == nil {
		tasks = []*store.ScheduledTask{}
	}
	jsonResponse(w, tasks)
}

func (s *Server) handleCreateScheduledTask(w http.ResponseWriter, r *http.Request) {
	var req store.ScheduledTask
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Schedule == "" || req.AgentName == "" || req.Prompt == "" {
		jsonError(w, "name, schedule, agent_name, and prompt are required", nil, http.StatusBadRequest)
		return
	}
	task, err := s.store.DB().CreateScheduledTask(&req)
	if err != nil {
		jsonError(w, "creating scheduled task", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, task)
}

func (s *Server) handleGetScheduledTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid id", err, http.StatusBadRequest)
		return
	}
	task, err := s.store.DB().GetScheduledTask(id)
	if err != nil {
		jsonError(w, "task not found", err, http.StatusNotFound)
		return
	}
	jsonResponse(w, task)
}

func (s *Server) handleUpdateScheduledTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid id", err, http.StatusBadRequest)
		return
	}

	existing, err := s.store.DB().GetScheduledTask(id)
	if err != nil {
		jsonError(w, "task not found", err, http.StatusNotFound)
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		jsonError(w, "invalid JSON", err, http.StatusBadRequest)
		return
	}

	name := existing.Name
	description := existing.Description
	schedule := existing.Schedule
	agentName := existing.AgentName
	prompt := existing.Prompt
	status := existing.Status
	projectID := existing.ProjectID

	if v, ok := updates["name"].(string); ok {
		name = v
	}
	if v, ok := updates["description"].(string); ok {
		description = v
	}
	if v, ok := updates["schedule"].(string); ok {
		schedule = v
	}
	if v, ok := updates["agent_name"].(string); ok {
		agentName = v
	}
	if v, ok := updates["prompt"].(string); ok {
		prompt = v
	}
	if v, ok := updates["status"].(string); ok {
		status = v
	}
	if v, ok := updates["project_id"]; ok {
		if v == nil {
			projectID = nil
		} else if f, ok := v.(float64); ok {
			pid := int(f)
			projectID = &pid
		}
	}

	task, err := s.store.DB().UpdateScheduledTask(id, name, description, schedule, agentName, prompt, status, projectID)
	if err != nil {
		jsonError(w, "updating task", err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, task)
}

func (s *Server) handleDeleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid id", err, http.StatusBadRequest)
		return
	}
	if err := s.store.DB().DeleteScheduledTask(id); err != nil {
		jsonError(w, "deleting task", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTaskRuns(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid id", err, http.StatusBadRequest)
		return
	}
	runs, err := s.store.DB().ListTaskRuns(id, 20)
	if err != nil {
		jsonError(w, "listing runs", err, http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []*store.TaskRun{}
	}
	jsonResponse(w, runs)
}

func (s *Server) handleTriggerTaskRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonError(w, "invalid id", err, http.StatusBadRequest)
		return
	}
	task, err := s.store.DB().GetScheduledTask(id)
	if err != nil {
		jsonError(w, "task not found", err, http.StatusNotFound)
		return
	}

	// Create run record
	run, err := s.store.DB().CreateTaskRun(task.ID)
	if err != nil {
		jsonError(w, "creating run", err, http.StatusInternalServerError)
		return
	}

	// Execute in background
	go func() {
		title := fmt.Sprintf("[Scheduled] %s", task.Name)
		entryID, err := s.CreateAndRouteEntry(title, task.Prompt, "ideas", task.AgentName, task.ProjectID)
		if err != nil {
			_ = s.store.DB().CompleteTaskRun(run.ID, "failed", "", "", err.Error())
			return
		}
		_ = s.store.DB().CompleteTaskRun(run.ID, "complete", entryID, "Entry created and routed", "")

		now := time.Now().UTC()
		_ = s.store.DB().SetTaskLastRun(task.ID, now, now.Add(24*time.Hour))
	}()

	jsonResponse(w, map[string]any{"run_id": run.ID, "status": "started"})
}

// --- Library Handlers ---

func (s *Server) handleLibraryAgents(w http.ResponseWriter, r *http.Request) {
	type agentInfo struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}

	var agents []agentInfo
	for name, def := range s.wc.Agents {
		agents = append(agents, agentInfo{
			Name:        name,
			Description: def.Description,
		})
	}
	if agents == nil {
		agents = []agentInfo{}
	}
	jsonResponse(w, agents)
}

func (s *Server) handleLibrarySkills(w http.ResponseWriter, r *http.Request) {
	type skillInfo struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}

	var skills []skillInfo
	if s.wc.SkillsDir != "" {
		entries, err := os.ReadDir(s.wc.SkillsDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				si := skillInfo{Name: entry.Name()}
				// Try to read SKILL.md for description
				skillFile := filepath.Join(s.wc.SkillsDir, entry.Name(), "SKILL.md")
				if data, err := os.ReadFile(skillFile); err == nil {
					content := string(data)
					// Extract first non-heading paragraph line
					lines := strings.Split(content, "\n")
					for _, line := range lines {
						trimmed := strings.TrimSpace(line)
						if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
							continue
						}
						si.Description = trimmed
						if len(si.Description) > 200 {
							si.Description = si.Description[:200] + "…"
						}
						break
					}
				}
				skills = append(skills, si)
			}
		}
	}
	if skills == nil {
		skills = []skillInfo{}
	}
	jsonResponse(w, skills)
}

func (s *Server) handleLibraryMemory(w http.ResponseWriter, r *http.Request) {
	type memoryFile struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}

	var files []memoryFile

	// Look for .spec/memory/ in the workspace
	if s.cfg.BrainCodeDir != "" {
		scriptsDir := filepath.Dir(s.cfg.BrainCodeDir)
		workspaceDir := filepath.Dir(scriptsDir)
		memDir := filepath.Join(workspaceDir, ".spec", "memory")
		entries, err := os.ReadDir(memDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				files = append(files, memoryFile{
					Name: entry.Name(),
					Path: filepath.Join(".spec", "memory", entry.Name()),
					Size: info.Size(),
				})
			}
		}
	}
	if files == nil {
		files = []memoryFile{}
	}
	jsonResponse(w, files)
}

// --- Activity Feed ---

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	events, err := s.store.DB().RecentActivity(limit)
	if err != nil {
		jsonError(w, "loading activity", err, http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []*store.ActivityEvent{}
	}
	jsonResponse(w, events)
}
