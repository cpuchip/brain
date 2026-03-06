package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
	"gopkg.in/yaml.v3"
)

// Store manages reading and writing entries using SQLite (structured data)
// and chromem-go (semantic search). Replaces the previous filesystem+git backend.
type Store struct {
	db  *DB
	vec *VecStore
	git *Git // optional — for archive export only
}

// New creates a new Store backed by SQLite + chromem-go.
// The git parameter is optional — pass nil to disable archive export.
func New(db *DB, vec *VecStore, git *Git) *Store {
	return &Store{
		db:  db,
		vec: vec,
		git: git,
	}
}

// DB returns the underlying SQLite database.
func (s *Store) DB() *DB {
	return s.db
}

// Vec returns the underlying vector store.
func (s *Store) Vec() *VecStore {
	return s.vec
}

// Close closes the SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Save writes a classified result to SQLite and embeds it in the vector store.
// Returns the entry ID (previously returned a file path).
// The source parameter identifies the origin (relay, discord, cli, web, app).
func (s *Store) Save(result *classifier.Result, rawText string, needsReview bool, source string) (string, error) {
	now := time.Now().UTC()

	if source == "" {
		source = "unknown"
	}

	entry := &Entry{
		Title:      result.Title,
		Category:   result.Category,
		Created:    now,
		Updated:    now,
		Tags:       result.Tags,
		Confidence: result.Confidence,
		Source:     source,
		// People
		Name:      result.Fields.Name,
		Context:   result.Fields.Context,
		FollowUps: result.Fields.FollowUps,
		// Projects
		Status:     result.Fields.Status,
		NextAction: result.Fields.NextAction,
		// Ideas
		OneLiner: result.Fields.OneLiner,
		// Actions
		DueDate: result.Fields.DueDate,
		// Study
		References: result.Fields.References,
		Insight:    result.Fields.Insight,
		// Journal
		Mood:      result.Fields.Mood,
		Gratitude: result.Fields.Gratitude,
		// Review
		NeedsReview: needsReview,
		// Body
		Body: rawText,
	}

	if needsReview {
		entry.Category = "inbox"
	}

	id, err := s.db.InsertEntry(entry)
	if err != nil {
		return "", fmt.Errorf("inserting entry: %w", err)
	}
	entry.ID = id

	// Embed in vector store (async-safe, non-blocking intent)
	if s.vec != nil && s.vec.Enabled() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.vec.Embed(ctx, entry); err != nil {
				log.Printf("warning: embedding failed for %s: %v", id, err)
				_ = s.db.SetEmbeddingStatus(id, s.vec.Model(), time.Time{}, err.Error())
			} else {
				_ = s.db.SetEmbeddingStatus(id, s.vec.Model(), time.Now().UTC(), "")
			}
		}()
	}

	return id, nil
}

// SaveAudit writes an audit log entry.
// The record's FilePath field is used as the entry ID (Save() now returns UUIDs).
func (s *Store) SaveAudit(record *AuditRecord) error {
	return s.db.InsertAudit(record.FilePath, record)
}

// SetIbecomeTaskID links a brain entry to its corresponding ibecome task.
func (s *Store) SetIbecomeTaskID(entryID string, taskID int64) error {
	return s.db.SetIbecomeTaskID(entryID, taskID)
}

// Reclassify moves an entry from one category to another.
// The currentPath parameter is now an entry ID (for backward compat with relay/discord,
// which pass the return value of Save()).
func (s *Store) Reclassify(currentPath, newCategory string) (string, error) {
	entryID := currentPath // Save() now returns an ID, so this is the ID

	if err := s.db.Reclassify(entryID, newCategory); err != nil {
		return "", fmt.Errorf("reclassifying: %w", err)
	}

	// Re-embed with updated category
	if s.vec != nil && s.vec.Enabled() {
		go func() {
			entry, err := s.db.GetEntry(entryID)
			if err != nil {
				log.Printf("warning: could not re-embed after reclassify: %v", err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.vec.ReEmbed(ctx, entry); err != nil {
				log.Printf("warning: re-embed failed for %s: %v", entryID, err)
			}
		}()
	}

	return entryID, nil
}

// ListCategory returns entry IDs for a given category.
// (Previously returned file paths — relay/discord use these as opaque strings.)
func (s *Store) ListCategory(category string) ([]string, error) {
	entries, err := s.db.ListCategory(category)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	return ids, nil
}

// ReadEntry retrieves an entry by ID.
func (s *Store) ReadEntry(id string) (*Entry, error) {
	return s.db.GetEntry(id)
}

// --- Archive export (renders entries to markdown for private-brain) ---

// ExportToMarkdown renders an entry as YAML front matter + markdown body,
// suitable for writing to the private-brain archive.
func ExportToMarkdown(e *Entry) (string, error) {
	return renderEntry(e)
}

// ExportEntry writes a single entry as a markdown file to the given base directory.
func (s *Store) ExportEntry(baseDir string, e *Entry) (string, error) {
	content, err := renderEntry(e)
	if err != nil {
		return "", fmt.Errorf("rendering entry: %w", err)
	}

	dir := e.Category
	slug := slugify(e.Title)
	filename := fmt.Sprintf("%s-%s.md", e.Created.Format("2006-01-02"), slug)
	relPath := filepath.Join(dir, filename)
	absPath := filepath.Join(baseDir, relPath)

	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	// Optionally git commit
	if s.git != nil {
		commitMsg := fmt.Sprintf("archive %s → %s", truncate(e.Title, 50), dir)
		if err := s.git.CommitFile(relPath, commitMsg); err != nil {
			log.Printf("warning: git commit failed: %v", err)
		}
	}

	return relPath, nil
}

// renderEntry creates the markdown file content with YAML front matter.
func renderEntry(e *Entry) (string, error) {
	fm := make(map[string]any)
	fm["title"] = e.Title
	fm["category"] = e.Category
	fm["created"] = e.Created.Format(time.RFC3339)
	fm["updated"] = e.Updated.Format(time.RFC3339)
	fm["confidence"] = e.Confidence

	if len(e.Tags) > 0 {
		fm["tags"] = e.Tags
	}
	if e.NeedsReview {
		fm["needs_review"] = true
	}

	// Category-specific fields
	setIfNotEmpty(fm, "name", e.Name)
	setIfNotEmpty(fm, "context", e.Context)
	setIfNotEmpty(fm, "follow_ups", e.FollowUps)
	setIfNotEmpty(fm, "status", e.Status)
	setIfNotEmpty(fm, "next_action", e.NextAction)
	setIfNotEmpty(fm, "one_liner", e.OneLiner)
	setIfNotEmpty(fm, "due_date", e.DueDate)
	setIfNotEmpty(fm, "references", e.References)
	setIfNotEmpty(fm, "insight", e.Insight)
	setIfNotEmpty(fm, "mood", e.Mood)
	setIfNotEmpty(fm, "gratitude", e.Gratitude)

	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(yamlBytes)
	sb.WriteString("---\n\n")
	sb.WriteString("# ")
	sb.WriteString(e.Title)
	sb.WriteString("\n\n")
	sb.WriteString(e.Body)
	sb.WriteString("\n")

	return sb.String(), nil
}

// parseEntry reads a markdown file with YAML front matter (used by migration).
func parseEntry(content []byte) (*Entry, error) {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("missing YAML front matter")
	}

	parts := strings.SplitN(s[4:], "\n---\n", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed front matter")
	}

	var entry Entry
	if err := yaml.Unmarshal([]byte(parts[0]), &entry); err != nil {
		return nil, fmt.Errorf("parsing front matter: %w", err)
	}

	entry.Body = strings.TrimSpace(parts[1])
	// Strip the h1 title line from body if present
	if strings.HasPrefix(entry.Body, "# ") {
		if idx := strings.Index(entry.Body, "\n"); idx != -1 {
			entry.Body = strings.TrimSpace(entry.Body[idx+1:])
		}
	}

	return &entry, nil
}

func setIfNotEmpty(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
