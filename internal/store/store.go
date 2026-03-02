package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
	"gopkg.in/yaml.v3"
)

// Store manages reading and writing entries to the brain data directory.
type Store struct {
	dataDir string
	git     *Git
}

// New creates a new Store backed by the given data directory.
func New(dataDir string, git *Git) *Store {
	return &Store{
		dataDir: dataDir,
		git:     git,
	}
}

// Save writes a classified result to the appropriate directory as a markdown
// file with YAML front matter, then commits it.
func (s *Store) Save(result *classifier.Result, rawText string, needsReview bool) (string, error) {
	now := time.Now()

	entry := Entry{
		Title:      result.Title,
		Category:   result.Category,
		Created:    now,
		Updated:    now,
		Tags:       result.Tags,
		Confidence: result.Confidence,
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

	// Determine target directory
	dir := result.Category
	if needsReview {
		dir = "inbox"
	}

	// Generate filename: YYYY-MM-DD-slug.md
	slug := slugify(result.Title)
	filename := fmt.Sprintf("%s-%s.md", now.Format("2006-01-02"), slug)
	relPath := filepath.Join(dir, filename)
	absPath := filepath.Join(s.dataDir, relPath)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}

	// If file exists, add a numeric suffix
	absPath, relPath = s.deduplicatePath(absPath, relPath)

	// Write the file
	content, err := renderEntry(&entry)
	if err != nil {
		return "", fmt.Errorf("rendering entry: %w", err)
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	// Git commit
	commitMsg := fmt.Sprintf("classify %s → %s", truncate(rawText, 50), dir)
	if err := s.git.CommitFile(relPath, commitMsg); err != nil {
		// Log but don't fail — the file was written
		fmt.Fprintf(os.Stderr, "warning: git commit failed: %v\n", err)
	}

	return relPath, nil
}

// SaveAudit writes an audit log entry.
func (s *Store) SaveAudit(record *AuditRecord) error {
	now := record.Timestamp
	dir := filepath.Join(s.dataDir, ".brain", "audit-log")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating audit dir: %w", err)
	}

	// One file per day
	filename := fmt.Sprintf("%s.yaml", now.Format("2006-01-02"))
	absPath := filepath.Join(dir, filename)

	// Append to existing file or create new
	var records []AuditRecord
	if data, err := os.ReadFile(absPath); err == nil {
		if err := yaml.Unmarshal(data, &records); err != nil {
			// If corrupt, start fresh
			records = nil
		}
	}

	records = append(records, *record)

	data, err := yaml.Marshal(records)
	if err != nil {
		return fmt.Errorf("marshaling audit: %w", err)
	}

	if err := os.WriteFile(absPath, data, 0644); err != nil {
		return fmt.Errorf("writing audit: %w", err)
	}

	// Commit audit log
	relPath := filepath.Join(".brain", "audit-log", filename)
	_ = s.git.CommitFile(relPath, "audit log")

	return nil
}

// Reclassify moves an entry from one category to another.
func (s *Store) Reclassify(currentRelPath, newCategory string) (string, error) {
	currentAbs := filepath.Join(s.dataDir, currentRelPath)
	if _, err := os.Stat(currentAbs); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", currentRelPath)
	}

	// Read current content
	content, err := os.ReadFile(currentAbs)
	if err != nil {
		return "", err
	}

	// Update the category in front matter (simple replacement)
	updated := strings.Replace(string(content),
		fmt.Sprintf("category: %s", filepath.Dir(currentRelPath)),
		fmt.Sprintf("category: %s", newCategory),
		1)
	// Also clear needs_review
	updated = strings.Replace(updated, "needs_review: true", "needs_review: false", 1)

	// New path
	newRelPath := filepath.Join(newCategory, filepath.Base(currentRelPath))
	newAbs := filepath.Join(s.dataDir, newRelPath)

	// Ensure target dir exists
	if err := os.MkdirAll(filepath.Dir(newAbs), 0755); err != nil {
		return "", err
	}

	// Write to new location
	if err := os.WriteFile(newAbs, []byte(updated), 0644); err != nil {
		return "", err
	}

	// Remove old file
	if err := os.Remove(currentAbs); err != nil {
		return "", err
	}

	// Commit the move
	_ = s.git.CommitAll(fmt.Sprintf("reclassify %s → %s", filepath.Base(currentRelPath), newCategory))

	return newRelPath, nil
}

// ListCategory returns all entries in a category directory.
func (s *Store) ListCategory(category string) ([]string, error) {
	dir := filepath.Join(s.dataDir, category)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, filepath.Join(category, e.Name()))
		}
	}
	return files, nil
}

// ReadEntry reads and parses an entry file.
func (s *Store) ReadEntry(relPath string) (*Entry, error) {
	absPath := filepath.Join(s.dataDir, relPath)
	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	return parseEntry(content)
}

// renderEntry creates the markdown file content with YAML front matter.
func renderEntry(e *Entry) (string, error) {
	// Build front matter map to avoid empty fields
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

// parseEntry reads a markdown file with YAML front matter.
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

func (s *Store) deduplicatePath(absPath, relPath string) (string, string) {
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return absPath, relPath
	}
	// Add numeric suffix
	ext := filepath.Ext(absPath)
	base := strings.TrimSuffix(absPath, ext)
	baseRel := strings.TrimSuffix(relPath, ext)
	for i := 2; i < 100; i++ {
		newAbs := fmt.Sprintf("%s-%d%s", base, i, ext)
		newRel := fmt.Sprintf("%s-%d%s", baseRel, i, ext)
		if _, err := os.Stat(newAbs); os.IsNotExist(err) {
			return newAbs, newRel
		}
	}
	return absPath, relPath
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
