package store

import (
	"path/filepath"
	"testing"

	"github.com/cpuchip/brain/internal/classifier"
)

func TestStoreSaveAndReclassify(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// No vec store, no git — minimal setup
	st := New(db, nil, nil)

	result := &classifier.Result{
		Category:   "ideas",
		Confidence: 0.85,
		Title:      "Build a second brain",
		Fields: classifier.Fields{
			OneLiner: "SQLite + chromem-go replaces git-backed markdown",
		},
		Tags: []string{"brain", "architecture"},
	}

	id, err := st.Save(result, "I think we should build a second brain using SQLite and chromem-go", false, "test")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id == "" {
		t.Fatal("Save returned empty ID")
	}

	// Verify saved entry
	entry, err := st.ReadEntry(id)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if entry.Title != "Build a second brain" {
		t.Errorf("Title = %q, want %q", entry.Title, "Build a second brain")
	}
	if entry.Category != "ideas" {
		t.Errorf("Category = %q, want %q", entry.Category, "ideas")
	}
	if entry.OneLiner != "SQLite + chromem-go replaces git-backed markdown" {
		t.Errorf("OneLiner = %q", entry.OneLiner)
	}

	// Reclassify
	newID, err := st.Reclassify(id, "projects")
	if err != nil {
		t.Fatalf("Reclassify: %v", err)
	}
	if newID != id {
		t.Errorf("Reclassify ID = %q, want %q", newID, id)
	}

	reclassified, err := st.ReadEntry(id)
	if err != nil {
		t.Fatalf("ReadEntry after reclassify: %v", err)
	}
	if reclassified.Category != "projects" {
		t.Errorf("Reclassified Category = %q, want %q", reclassified.Category, "projects")
	}

	// ListCategory
	ids, err := st.ListCategory("projects")
	if err != nil {
		t.Fatalf("ListCategory: %v", err)
	}
	if len(ids) != 1 || ids[0] != id {
		t.Errorf("ListCategory = %v, want [%s]", ids, id)
	}
}

func TestStoreSaveNeedsReview(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	st := New(db, nil, nil)

	result := &classifier.Result{
		Category:   "ideas",
		Confidence: 0.3, // Low confidence
		Title:      "Uncertain thought",
	}

	id, err := st.Save(result, "Not sure what this is about", true, "test")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	entry, err := st.ReadEntry(id)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	// Should be in inbox when needsReview=true
	if entry.Category != "inbox" {
		t.Errorf("Category = %q, want %q", entry.Category, "inbox")
	}
	if !entry.NeedsReview {
		t.Error("NeedsReview should be true")
	}
}

func TestStoreSaveAudit(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	st := New(db, nil, nil)

	record := &AuditRecord{
		RawText:     "some text",
		Category:    "ideas",
		Title:       "Audit Test",
		Confidence:  0.9,
		NeedsReview: false,
		Source:      "test",
	}

	if err := st.SaveAudit(record); err != nil {
		t.Fatalf("SaveAudit: %v", err)
	}
}

func TestStoreExportToMarkdown(t *testing.T) {
	entry := &Entry{
		Title:    "Test Export",
		Category: "ideas",
		Body:     "This should be exported as markdown",
		Tags:     []string{"export", "test"},
	}

	md, err := ExportToMarkdown(entry)
	if err != nil {
		t.Fatalf("ExportToMarkdown: %v", err)
	}

	if len(md) == 0 {
		t.Error("ExportToMarkdown returned empty string")
	}
	if md[:4] != "---\n" {
		t.Error("Markdown should start with YAML front matter")
	}
}
