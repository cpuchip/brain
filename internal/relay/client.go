// Package relay implements the WebSocket client that connects brain.exe
// to the ibeco.me relay hub. It receives thoughts, classifies them,
// stores them to git, and sends results back.
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/ibecome"
	"github.com/cpuchip/brain/internal/store"
	"github.com/gorilla/websocket"
)

// Message types (must match ibeco.me hub protocol).
const (
	TypeAuth          = "auth"
	TypeAuthOK        = "auth_ok"
	TypeAuthErr       = "auth_error"
	TypeThought       = "thought"
	TypeResult        = "result"
	TypeFix           = "fix"
	TypeFixOK         = "fix_ok"
	TypeQueued        = "queued"
	TypeStatus        = "status"
	TypePing          = "ping"
	TypePong          = "pong"
	TypeTaskUpdated   = "task_updated"
	TypeEntriesSync   = "entries_sync"
	TypeEntryCreate   = "entry_create"
	TypeEntryCreated  = "entry_created"
	TypeEntryUpdate   = "entry_update"
	TypeEntryUpdated  = "entry_updated"
	TypeEntryDelete   = "entry_delete"
	TypeEntryClassify = "entry_classify"
	TypeSubTaskCreate = "subtask_create"
	TypeSubTaskUpdate = "subtask_update"
	TypeSubTaskDelete = "subtask_delete"
)

// ThoughtMessage from the app.
type ThoughtMessage struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	Source    string `json:"source,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// ResultMessage sent back to the app.
type ResultMessage struct {
	Type        string   `json:"type"`
	ThoughtID   string   `json:"thought_id"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Confidence  float64  `json:"confidence"`
	Tags        []string `json:"tags"`
	NeedsReview bool     `json:"needs_review"`
	FilePath    string   `json:"file_path"`
}

// FixMessage from the app.
type FixMessage struct {
	Type        string `json:"type"`
	ThoughtID   string `json:"thought_id"`
	NewCategory string `json:"new_category"`
}

// FixOKMessage sent back after reclassification.
type FixOKMessage struct {
	Type      string `json:"type"`
	ThoughtID string `json:"thought_id"`
	NewPath   string `json:"new_path"`
}

// TaskUpdatedMessage from the relay hub when an ibecome task status changes.
type TaskUpdatedMessage struct {
	Type         string `json:"type"`           // "task_updated"
	TaskID       int64  `json:"task_id"`        // ibecome task ID
	BrainEntryID string `json:"brain_entry_id"` // brain entry UUID
	Status       string `json:"status"`         // new ibecome status
	Title        string `json:"title"`
}

// Client manages the WebSocket connection to the ibeco.me relay hub.
type Client struct {
	url      string
	token    string
	classify *classifier.Classifier
	store    *store.Store
	ibecome  *ibecome.Client // optional: auto-create tasks in ibecome

	mu        sync.Mutex
	ws        *websocket.Conn
	lastPaths map[string]string // thoughtID -> file relPath
	done      chan struct{}
}

// NewClient creates a new relay client.
// If ibecomeClient is non-nil, tasks will be auto-created for actions/projects.
func NewClient(url, token string, classify *classifier.Classifier, st *store.Store, ibecomeClient *ibecome.Client) *Client {
	return &Client{
		url:       url,
		token:     token,
		classify:  classify,
		store:     st,
		ibecome:   ibecomeClient,
		lastPaths: make(map[string]string),
		done:      make(chan struct{}),
	}
}

// Run connects to the relay hub and processes messages until ctx is cancelled.
// It automatically reconnects with exponential backoff on disconnection.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Printf("[relay] shutting down")
			return
		default:
		}

		err := c.connect(ctx)
		if err != nil {
			log.Printf("[relay] connection error: %v", err)
		} else {
			backoff = time.Second // reset on clean disconnect
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			log.Printf("[relay] reconnecting in %v...", backoff)
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// connect establishes a single WebSocket connection and processes messages until it drops.
func (c *Client) connect(ctx context.Context) error {
	log.Printf("[relay] connecting to %s", c.url)

	ws, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()

	defer func() {
		ws.Close()
		c.mu.Lock()
		c.ws = nil
		c.mu.Unlock()
	}()

	// Authenticate
	authMsg, _ := json.Marshal(map[string]string{
		"type":  TypeAuth,
		"token": c.token,
		"role":  "agent",
	})
	if err := ws.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		return fmt.Errorf("sending auth: %w", err)
	}

	// Read auth response
	_, data, err := ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("reading auth response: %w", err)
	}

	var env struct {
		Type  string `json:"type"`
		Error string `json:"error,omitempty"`
	}
	json.Unmarshal(data, &env)

	if env.Type == TypeAuthErr {
		return fmt.Errorf("auth failed: %s", env.Error)
	}
	if env.Type != TypeAuthOK {
		return fmt.Errorf("unexpected auth response: %s", env.Type)
	}

	log.Printf("[relay] authenticated successfully")

	// Send initial status
	c.sendStatus(ws)

	// Send all entries for ibeco.me cache
	c.sendEntriesSync(ws)

	// When the hub sends a WS Ping, reset our read deadline and reply with Pong.
	// (SetPongHandler would only fire if *we* sent pings — but we don't initiate them.)
	ws.SetPingHandler(func(appData string) error {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		return ws.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	// Client-side ping sender — belt-and-suspenders keepalive in case
	// the hub's pings aren't enough (e.g. during long classifications).
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.mu.Lock()
				if c.ws != nil {
					c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				}
				c.mu.Unlock()
			}
		}
	}()
	defer close(pingDone)

	// Also listen for pong responses to our pings
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// Read loop
	for {
		select {
		case <-ctx.Done():
			ws.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return nil
		default:
		}

		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				return fmt.Errorf("read: %w", err)
			}
			return nil // normal close
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			log.Printf("[relay] invalid message: %v", err)
			continue
		}

		switch envelope.Type {
		case TypeThought:
			go c.handleThought(ctx, ws, data)
		case TypeFix:
			go c.handleFix(ws, data)
		case TypeQueued:
			c.handleQueued(ctx, ws, data)
		case TypeTaskUpdated:
			go c.handleTaskUpdated(ws, data)
		case TypeEntryCreate:
			go c.handleEntryCreate(ws, data)
		case TypeEntryUpdate:
			go c.handleEntryUpdate(ws, data)
		case TypeEntryDelete:
			go c.handleEntryDelete(data)
		case TypeEntryClassify:
			go c.handleEntryClassify(ws, data)
		case TypeSubTaskCreate:
			go c.handleSubTaskCreate(ws, data)
		case TypeSubTaskUpdate:
			go c.handleSubTaskUpdate(ws, data)
		case TypeSubTaskDelete:
			go c.handleSubTaskDelete(ws, data)
		case TypePing:
			pong, _ := json.Marshal(map[string]string{"type": TypePong})
			ws.WriteMessage(websocket.TextMessage, pong)
		default:
			log.Printf("[relay] unknown message type: %s", envelope.Type)
		}
	}
}

// handleThought classifies a thought and sends the result back.
func (c *Client) handleThought(ctx context.Context, ws *websocket.Conn, data []byte) {
	var thought ThoughtMessage
	if err := json.Unmarshal(data, &thought); err != nil {
		log.Printf("[relay] invalid thought: %v", err)
		return
	}

	log.Printf("[relay] classifying thought %s: %.50s...", thought.ID, thought.Text)

	classifyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Classify
	result, err := c.classify.Classify(classifyCtx, thought.Text)
	if err != nil {
		log.Printf("[relay] classification error: %v", err)
		result = &classifier.Result{
			Category:   "inbox",
			Confidence: 0,
			Title:      "unclassified-" + time.Now().Format("150405"),
			Fields:     classifier.Fields{Notes: thought.Text},
		}
	}

	needsReview := c.classify.NeedsReview(result)

	// Store
	relPath, err := c.store.Save(result, thought.Text, needsReview, "relay")
	if err != nil {
		log.Printf("[relay] store error: %v", err)
		return
	}

	// Track for fix command
	c.mu.Lock()
	c.lastPaths[thought.ID] = relPath
	c.mu.Unlock()

	// Auto-create task in ibecome for actions/projects
	if c.ibecome != nil && (result.Category == "actions" || result.Category == "projects") {
		go func() {
			taskCtx, taskCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer taskCancel()
			taskID, err := c.ibecome.CreateTaskFromResult(taskCtx, relPath, result, thought.Text)
			if err != nil {
				log.Printf("[relay] ibecome task creation failed: %v", err)
			} else if taskID > 0 {
				log.Printf("[relay] created ibecome task #%d for %s: %s", taskID, result.Category, result.Title)
				if err := c.store.SetIbecomeTaskID(relPath, taskID); err != nil {
					log.Printf("[relay] failed to save ibecome task link: %v", err)
				}
			}
		}()
	}

	// Audit
	auditRecord := &store.AuditRecord{
		Timestamp:   time.Now().UTC(),
		RawText:     thought.Text,
		Category:    result.Category,
		Title:       result.Title,
		Confidence:  result.Confidence,
		NeedsReview: needsReview,
		Source:      "relay",
		FilePath:    relPath,
		Tags:        result.Tags,
	}
	if err := c.store.SaveAudit(auditRecord); err != nil {
		log.Printf("[relay] audit error: %v", err)
	}

	// Send result back
	resultMsg, _ := json.Marshal(ResultMessage{
		Type:        TypeResult,
		ThoughtID:   thought.ID,
		Category:    result.Category,
		Title:       result.Title,
		Confidence:  result.Confidence,
		Tags:        result.Tags,
		NeedsReview: needsReview,
		FilePath:    relPath,
	})

	c.mu.Lock()
	ws.WriteMessage(websocket.TextMessage, resultMsg)
	c.mu.Unlock()

	// Push the new entry to ibeco.me cache
	c.sendEntryCreated(ws, relPath)

	log.Printf("[relay] classified %s -> %s (%s, %.0f%%)", thought.ID, result.Category, result.Title, result.Confidence*100)
}

// handleFix reclassifies a previously classified thought.
func (c *Client) handleFix(ws *websocket.Conn, data []byte) {
	var fix FixMessage
	if err := json.Unmarshal(data, &fix); err != nil {
		log.Printf("[relay] invalid fix: %v", err)
		return
	}

	c.mu.Lock()
	currentPath, ok := c.lastPaths[fix.ThoughtID]
	c.mu.Unlock()

	if !ok {
		log.Printf("[relay] no path found for thought %s to fix", fix.ThoughtID)
		return
	}

	newPath, err := c.store.Reclassify(currentPath, fix.NewCategory)
	if err != nil {
		log.Printf("[relay] reclassify error: %v", err)
		return
	}

	// Update tracked path
	c.mu.Lock()
	c.lastPaths[fix.ThoughtID] = newPath
	c.mu.Unlock()

	fixOK, _ := json.Marshal(FixOKMessage{
		Type:      TypeFixOK,
		ThoughtID: fix.ThoughtID,
		NewPath:   newPath,
	})

	c.mu.Lock()
	ws.WriteMessage(websocket.TextMessage, fixOK)
	c.mu.Unlock()

	log.Printf("[relay] reclassified %s -> %s: %s", fix.ThoughtID, fix.NewCategory, newPath)
}

// handleTaskUpdated processes a task status change from ibecome.
func (c *Client) handleTaskUpdated(ws *websocket.Conn, data []byte) {
	var msg TaskUpdatedMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid task_updated: %v", err)
		return
	}

	if msg.BrainEntryID == "" {
		log.Printf("[relay] task_updated has no brain_entry_id, skipping")
		return
	}

	// Map ibecome status → brain entry fields
	var status string
	var actionDone bool
	switch msg.Status {
	case "completed":
		status = "done"
		actionDone = true
	case "paused":
		status = "waiting"
	case "archived":
		status = "archived"
	default: // "active"
		status = ""
		actionDone = false
	}

	if err := c.store.UpdateEntryStatus(msg.BrainEntryID, status, actionDone); err != nil {
		log.Printf("[relay] failed to update entry %s status: %v", msg.BrainEntryID, err)
		return
	}

	log.Printf("[relay] synced task #%d → entry %s: status=%q done=%v", msg.TaskID, msg.BrainEntryID, status, actionDone)

	// Push the updated entry to ibeco.me cache
	c.sendEntryUpdated(ws, msg.BrainEntryID)
}

// handleQueued processes a batch of queued messages that were waiting while we were offline.
func (c *Client) handleQueued(ctx context.Context, ws *websocket.Conn, data []byte) {
	var queued struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &queued); err != nil {
		log.Printf("[relay] invalid queued bundle: %v", err)
		return
	}

	log.Printf("[relay] processing %d queued messages", len(queued.Messages))

	for _, raw := range queued.Messages {
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}

		switch env.Type {
		case TypeThought:
			c.handleThought(ctx, ws, raw)
		case TypeFix:
			c.handleFix(ws, raw)
		case TypeTaskUpdated:
			c.handleTaskUpdated(ws, raw)
		case TypeEntryCreate:
			c.handleEntryCreate(ws, raw)
		case TypeEntryUpdate:
			c.handleEntryUpdate(ws, raw)
		case TypeEntryDelete:
			c.handleEntryDelete(raw)
		case TypeEntryClassify:
			c.handleEntryClassify(ws, raw)
		case TypeSubTaskCreate:
			c.handleSubTaskCreate(ws, raw)
		case TypeSubTaskUpdate:
			c.handleSubTaskUpdate(ws, raw)
		case TypeSubTaskDelete:
			c.handleSubTaskDelete(ws, raw)
		}
	}
}

// sendEntriesSync sends all brain entries to the relay for ibeco.me caching.
func (c *Client) sendEntriesSync(ws *websocket.Conn) {
	entries, err := c.store.DB().ListAllForSync()
	if err != nil {
		log.Printf("[relay] error listing entries for sync: %v", err)
		return
	}

	type syncEntry struct {
		ID         string          `json:"id"`
		Title      string          `json:"title"`
		Category   string          `json:"category"`
		Body       string          `json:"body"`
		Status     string          `json:"status,omitempty"`
		ActionDone bool            `json:"action_done,omitempty"`
		DueDate    string          `json:"due_date,omitempty"`
		NextAction string          `json:"next_action,omitempty"`
		Tags       []string        `json:"tags,omitempty"`
		SubTasks   []store.SubTask `json:"subtasks,omitempty"`
		Source     string          `json:"source,omitempty"`
		CreatedAt  string          `json:"created_at"`
		UpdatedAt  string          `json:"updated_at"`
	}

	payload := make([]syncEntry, len(entries))
	for i, e := range entries {
		// Load subtasks for each entry
		subtasks, _ := c.store.DB().ListSubTasks(e.ID)
		payload[i] = syncEntry{
			ID:         e.ID,
			Title:      e.Title,
			Category:   e.Category,
			Body:       e.Body,
			Status:     e.Status,
			ActionDone: e.ActionDone,
			DueDate:    e.DueDate,
			NextAction: e.NextAction,
			Tags:       e.Tags,
			SubTasks:   subtasks,
			Source:     e.Source,
			CreatedAt:  e.Created.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:  e.Updated.Format("2006-01-02T15:04:05Z"),
		}
	}

	msg, _ := json.Marshal(map[string]any{
		"type":    TypeEntriesSync,
		"entries": payload,
	})

	c.mu.Lock()
	ws.WriteMessage(websocket.TextMessage, msg)
	c.mu.Unlock()

	log.Printf("[relay] synced %d entries to ibeco.me", len(entries))
}

// handleEntryUpdate processes an entry update request from ibeco.me.
func (c *Client) handleEntryUpdate(ws *websocket.Conn, data []byte) {
	var msg struct {
		EntryID string         `json:"entry_id"`
		Updates map[string]any `json:"updates"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid entry_update: %v", err)
		return
	}

	entry, err := c.store.DB().GetEntry(msg.EntryID)
	if err != nil {
		log.Printf("[relay] entry_update: entry %s not found: %v", msg.EntryID, err)
		return
	}

	if v, ok := msg.Updates["title"].(string); ok {
		entry.Title = v
	}
	if v, ok := msg.Updates["status"].(string); ok {
		entry.Status = v
	}
	if v, ok := msg.Updates["action_done"].(bool); ok {
		entry.ActionDone = v
	}
	if v, ok := msg.Updates["due_date"].(string); ok {
		entry.DueDate = v
	}
	if v, ok := msg.Updates["category"].(string); ok {
		entry.Category = v
	}
	if v, ok := msg.Updates["body"].(string); ok {
		entry.Body = v
	}
	if v, ok := msg.Updates["next_action"].(string); ok {
		entry.NextAction = v
	}

	if err := c.store.DB().UpdateEntry(entry); err != nil {
		log.Printf("[relay] entry_update failed for %s: %v", msg.EntryID, err)
		return
	}

	log.Printf("[relay] updated entry %s from ibeco.me", msg.EntryID)

	// Push the updated entry back to ibeco.me cache
	c.sendEntryUpdated(ws, msg.EntryID)
}

// handleEntryDelete processes an entry delete request from ibeco.me.
func (c *Client) handleEntryDelete(data []byte) {
	var msg struct {
		EntryID string `json:"entry_id"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid entry_delete: %v", err)
		return
	}

	if err := c.store.DB().DeleteEntry(msg.EntryID); err != nil {
		log.Printf("[relay] entry_delete failed for %s: %v", msg.EntryID, err)
		return
	}

	log.Printf("[relay] deleted entry %s from ibeco.me", msg.EntryID)
}

// handleEntryClassify processes a classify request from ibeco.me for an existing entry.
func (c *Client) handleEntryClassify(ws *websocket.Conn, data []byte) {
	var msg struct {
		EntryID string `json:"entry_id"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid entry_classify: %v", err)
		return
	}

	entry, err := c.store.DB().GetEntry(msg.EntryID)
	if err != nil {
		log.Printf("[relay] entry_classify: entry %s not found: %v", msg.EntryID, err)
		return
	}

	log.Printf("[relay] classify request for %s: %s/%s", msg.EntryID, entry.Category, entry.Title)
	c.autoClassifyEntry(ws, msg.EntryID, entry)
}

// handleEntryCreate processes a create request from ibeco.me and stores it locally.
func (c *Client) handleEntryCreate(ws *websocket.Conn, data []byte) {
	var msg struct {
		EntryID string         `json:"entry_id"`
		Fields  map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid entry_create: %v", err)
		return
	}

	entry := &store.Entry{
		ID:     msg.EntryID,
		Source: "web",
	}

	if v, ok := msg.Fields["title"].(string); ok {
		entry.Title = v
	}
	if v, ok := msg.Fields["category"].(string); ok && v != "" {
		entry.Category = v
	} else {
		entry.Category = "inbox"
	}
	if v, ok := msg.Fields["body"].(string); ok {
		entry.Body = v
	}
	if v, ok := msg.Fields["status"].(string); ok {
		entry.Status = v
	}
	if v, ok := msg.Fields["due_date"].(string); ok {
		entry.DueDate = v
	}
	if v, ok := msg.Fields["next_action"].(string); ok {
		entry.NextAction = v
	}
	if v, ok := msg.Fields["source"].(string); ok && v != "" {
		entry.Source = v
	}
	if tags, ok := msg.Fields["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				entry.Tags = append(entry.Tags, s)
			}
		}
	}

	id, err := c.store.DB().InsertEntry(entry)
	if err != nil {
		log.Printf("[relay] entry_create failed: %v", err)
		return
	}

	log.Printf("[relay] created entry %s from ibeco.me: %s/%s", id, entry.Category, entry.Title)

	// Push confirmed entry back to ibeco.me cache
	c.sendEntryCreated(ws, id)

	// Auto-classify inbox entries that arrived without AI classification
	if entry.Category == "inbox" && c.classify != nil {
		go c.autoClassifyEntry(ws, id, entry)
	}
}

// autoClassifyEntry runs the AI classifier on a newly created entry and syncs results back.
func (c *Client) autoClassifyEntry(ws *websocket.Conn, id string, entry *store.Entry) {
	text := entry.Body
	if text == "" {
		text = entry.Title
	}
	if text == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	log.Printf("[relay] auto-classifying entry %s: %.50s...", id, text)

	result, err := c.classify.Classify(ctx, text)
	if err != nil {
		log.Printf("[relay] auto-classify failed for %s: %v", id, err)
		return
	}

	needsReview := c.classify.NeedsReview(result)

	// Reclassify (move) if category changed
	currentID := id
	if result.Category != entry.Category {
		newID, err := c.store.Reclassify(id, result.Category)
		if err != nil {
			log.Printf("[relay] auto-classify reclassify failed for %s: %v", id, err)
			return
		}
		currentID = newID
	}

	// Re-fetch and update
	updated, err := c.store.DB().GetEntry(currentID)
	if err != nil {
		log.Printf("[relay] auto-classify GetEntry failed for %s: %v", currentID, err)
		return
	}

	updated.Title = result.Title
	updated.Confidence = result.Confidence
	updated.NeedsReview = needsReview
	if len(result.Tags) > 0 {
		updated.Tags = result.Tags
	}
	if result.Fields.DueDate != "" {
		updated.DueDate = result.Fields.DueDate
	}
	if result.Fields.NextAction != "" {
		updated.NextAction = result.Fields.NextAction
	}

	if err := c.store.DB().UpdateEntry(updated); err != nil {
		log.Printf("[relay] auto-classify update failed for %s: %v", currentID, err)
		return
	}

	log.Printf("[relay] auto-classified %s → %s/%s (%.0f%%)", currentID, result.Category, result.Title, result.Confidence*100)

	// Create subtasks from extracted list items
	if len(result.SubItems) > 0 {
		for i, text := range result.SubItems {
			st := &store.SubTask{
				EntryID:   currentID,
				Text:      text,
				SortOrder: i,
			}
			if err := c.store.DB().InsertSubTask(st); err != nil {
				log.Printf("[relay] auto-classify subtask creation failed for %s: %v", currentID, err)
			}
		}
		log.Printf("[relay] auto-classify created %d subtasks for %s", len(result.SubItems), currentID)
	}

	// Sync classified entry back to ibeco.me
	c.sendEntryUpdated(ws, currentID)

	// Auto-create task in ibecome for actions/projects
	if c.ibecome != nil && (result.Category == "actions" || result.Category == "projects") {
		taskCtx, taskCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer taskCancel()
		taskID, err := c.ibecome.CreateTaskFromResult(taskCtx, currentID, result, text)
		if err != nil {
			log.Printf("[relay] auto-classify ibecome task creation failed: %v", err)
		} else if taskID > 0 {
			log.Printf("[relay] auto-classify created ibecome task #%d for %s", taskID, currentID)
			if err := c.store.SetIbecomeTaskID(currentID, taskID); err != nil {
				log.Printf("[relay] auto-classify failed to save ibecome task link: %v", err)
			}
		}
	}
}

// sendEntryCreated sends an entry_created message to the hub with the full entry data.
func (c *Client) sendEntryCreated(ws *websocket.Conn, entryID string) {
	entry, err := c.store.DB().GetEntry(entryID)
	if err != nil {
		log.Printf("[relay] sendEntryCreated: entry %s not found: %v", entryID, err)
		return
	}

	msg, _ := json.Marshal(map[string]any{
		"type": TypeEntryCreated,
		"entry": map[string]any{
			"id":          entry.ID,
			"title":       entry.Title,
			"category":    entry.Category,
			"body":        entry.Body,
			"status":      entry.Status,
			"action_done": entry.ActionDone,
			"due_date":    entry.DueDate,
			"next_action": entry.NextAction,
			"tags":        entry.Tags,
			"subtasks":    entry.SubTasks,
			"source":      entry.Source,
			"created_at":  entry.Created.Format("2006-01-02T15:04:05Z"),
			"updated_at":  entry.Updated.Format("2006-01-02T15:04:05Z"),
		},
	})

	c.mu.Lock()
	ws.WriteMessage(websocket.TextMessage, msg)
	c.mu.Unlock()
}

// sendEntryUpdated sends an entry_updated message to the hub with the full entry data.
func (c *Client) sendEntryUpdated(ws *websocket.Conn, entryID string) {
	entry, err := c.store.DB().GetEntry(entryID)
	if err != nil {
		log.Printf("[relay] sendEntryUpdated: entry %s not found: %v", entryID, err)
		return
	}

	msg, _ := json.Marshal(map[string]any{
		"type": TypeEntryUpdated,
		"entry": map[string]any{
			"id":          entry.ID,
			"title":       entry.Title,
			"category":    entry.Category,
			"body":        entry.Body,
			"status":      entry.Status,
			"action_done": entry.ActionDone,
			"due_date":    entry.DueDate,
			"next_action": entry.NextAction,
			"tags":        entry.Tags,
			"subtasks":    entry.SubTasks,
			"source":      entry.Source,
			"created_at":  entry.Created.Format("2006-01-02T15:04:05Z"),
			"updated_at":  entry.Updated.Format("2006-01-02T15:04:05Z"),
		},
	})

	c.mu.Lock()
	ws.WriteMessage(websocket.TextMessage, msg)
	c.mu.Unlock()
}

// handleSubTaskCreate creates a subtask on a brain entry and syncs back.
func (c *Client) handleSubTaskCreate(ws *websocket.Conn, data []byte) {
	var msg struct {
		EntryID   string `json:"entry_id"`
		SubTaskID string `json:"subtask_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid subtask_create: %v", err)
		return
	}

	st := &store.SubTask{
		ID:      msg.SubTaskID,
		EntryID: msg.EntryID,
		Text:    msg.Text,
	}
	if err := c.store.DB().InsertSubTask(st); err != nil {
		log.Printf("[relay] subtask_create failed for entry %s: %v", msg.EntryID, err)
		return
	}

	log.Printf("[relay] created subtask %s on entry %s", msg.SubTaskID, msg.EntryID)
	c.sendEntryUpdated(ws, msg.EntryID)
}

// handleSubTaskUpdate updates a subtask (toggle done, edit text) and syncs back.
func (c *Client) handleSubTaskUpdate(ws *websocket.Conn, data []byte) {
	var msg struct {
		EntryID   string         `json:"entry_id"`
		SubTaskID string         `json:"subtask_id"`
		Updates   map[string]any `json:"updates"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid subtask_update: %v", err)
		return
	}

	st := &store.SubTask{ID: msg.SubTaskID, EntryID: msg.EntryID}
	// Load current values
	subtasks, _ := c.store.DB().ListSubTasks(msg.EntryID)
	for _, s := range subtasks {
		if s.ID == msg.SubTaskID {
			st.Text = s.Text
			st.Done = s.Done
			st.SortOrder = s.SortOrder
			break
		}
	}

	if v, ok := msg.Updates["text"].(string); ok {
		st.Text = v
	}
	if v, ok := msg.Updates["done"].(bool); ok {
		st.Done = v
	}

	if err := c.store.DB().UpdateSubTask(st); err != nil {
		log.Printf("[relay] subtask_update failed for %s: %v", msg.SubTaskID, err)
		return
	}

	log.Printf("[relay] updated subtask %s on entry %s", msg.SubTaskID, msg.EntryID)
	c.sendEntryUpdated(ws, msg.EntryID)
}

// handleSubTaskDelete deletes a subtask and syncs back.
func (c *Client) handleSubTaskDelete(ws *websocket.Conn, data []byte) {
	var msg struct {
		EntryID   string `json:"entry_id"`
		SubTaskID string `json:"subtask_id"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid subtask_delete: %v", err)
		return
	}

	if err := c.store.DB().DeleteSubTask(msg.EntryID, msg.SubTaskID); err != nil {
		log.Printf("[relay] subtask_delete failed for %s: %v", msg.SubTaskID, err)
		return
	}

	log.Printf("[relay] deleted subtask %s from entry %s", msg.SubTaskID, msg.EntryID)
	c.sendEntryUpdated(ws, msg.EntryID)
}

// sendStatus sends the brain's current status to the relay.
func (c *Client) sendStatus(ws *websocket.Conn) {
	categories := []string{"people", "projects", "ideas", "actions", "study", "journal", "inbox"}
	counts := make(map[string]int)
	for _, cat := range categories {
		entries, _ := c.store.ListCategory(cat)
		counts[cat] = len(entries)
	}

	status, _ := json.Marshal(map[string]any{
		"type":       TypeStatus,
		"model":      "", // filled by caller if needed
		"categories": counts,
	})

	ws.WriteMessage(websocket.TextMessage, status)
}

// Stop signals the client to shut down.
func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws != nil {
		c.ws.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.ws.Close()
	}
}
