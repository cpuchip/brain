package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDBCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Insert
	entry := &Entry{
		Title:      "Test Entry",
		Category:   "ideas",
		Body:       "This is a test thought",
		Confidence: 0.85,
		Source:     "test",
		Tags:       []string{"testing", "first"},
	}

	id, err := db.InsertEntry(entry)
	if err != nil {
		t.Fatalf("InsertEntry: %v", err)
	}
	if id == "" {
		t.Fatal("InsertEntry returned empty ID")
	}

	// Read
	got, err := db.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if got.Title != "Test Entry" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Entry")
	}
	if got.Category != "ideas" {
		t.Errorf("Category = %q, want %q", got.Category, "ideas")
	}
	if got.Body != "This is a test thought" {
		t.Errorf("Body = %q, want %q", got.Body, "This is a test thought")
	}
	if got.Confidence != 0.85 {
		t.Errorf("Confidence = %f, want 0.85", got.Confidence)
	}
	if len(got.Tags) != 2 {
		t.Errorf("Tags count = %d, want 2", len(got.Tags))
	}

	// Update
	got.Title = "Updated Title"
	got.Category = "projects"
	got.Tags = []string{"updated"}
	if err := db.UpdateEntry(got); err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}

	updated, err := db.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry after update: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("Updated Title = %q, want %q", updated.Title, "Updated Title")
	}
	if updated.Category != "projects" {
		t.Errorf("Updated Category = %q, want %q", updated.Category, "projects")
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "updated" {
		t.Errorf("Updated Tags = %v, want [updated]", updated.Tags)
	}

	// ListCategory
	entries, err := db.ListCategory("projects")
	if err != nil {
		t.Fatalf("ListCategory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("ListCategory count = %d, want 1", len(entries))
	}

	// Stats
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats["projects"] != 1 {
		t.Errorf("Stats[projects] = %d, want 1", stats["projects"])
	}

	// SearchText
	results, err := db.SearchText("test thought", 10)
	if err != nil {
		t.Fatalf("SearchText: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("SearchText count = %d, want 1", len(results))
	}

	// Tags
	tags, err := db.ListTags()
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if tags["updated"] != 1 {
		t.Errorf("Tags[updated] = %d, want 1", tags["updated"])
	}

	// Reclassify
	if err := db.Reclassify(id, "study"); err != nil {
		t.Fatalf("Reclassify: %v", err)
	}
	reclassified, err := db.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry after reclassify: %v", err)
	}
	if reclassified.Category != "study" {
		t.Errorf("Reclassified Category = %q, want %q", reclassified.Category, "study")
	}
	if reclassified.NeedsReview {
		t.Error("Reclassified entry should not need review")
	}

	// Delete
	if err := db.DeleteEntry(id); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	_, err = db.GetEntry(id)
	if err == nil {
		t.Error("GetEntry after delete should return error")
	}
}

func TestDBAudit(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	record := &AuditRecord{
		Timestamp:   time.Now(),
		RawText:     "some raw text",
		Category:    "ideas",
		Title:       "Test Audit",
		Confidence:  0.9,
		NeedsReview: false,
		Source:      "test",
	}

	if err := db.InsertAudit("", record); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
}

func TestDBVersioning(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Insert and update to create a version
	entry := &Entry{
		Title:      "Original Title",
		Category:   "ideas",
		Body:       "Original body",
		Confidence: 0.8,
		Source:     "test",
	}

	id, err := db.InsertEntry(entry)
	if err != nil {
		t.Fatalf("InsertEntry: %v", err)
	}

	got, _ := db.GetEntry(id)
	got.Title = "Modified Title"
	got.Body = "Modified body"
	if err := db.UpdateEntry(got); err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}

	// Check that a version was created
	var count int
	err = db.db.QueryRow(`SELECT COUNT(*) FROM entry_versions WHERE entry_id = ?`, id).Scan(&count)
	if err != nil {
		t.Fatalf("counting versions: %v", err)
	}
	if count != 1 {
		t.Errorf("version count = %d, want 1", count)
	}

	// Verify the version has the original data
	var title, body string
	err = db.db.QueryRow(`SELECT title, body FROM entry_versions WHERE entry_id = ? ORDER BY id ASC LIMIT 1`, id).Scan(&title, &body)
	if err != nil {
		t.Fatalf("reading version: %v", err)
	}
	if title != "Original Title" {
		t.Errorf("version title = %q, want %q", title, "Original Title")
	}
	if body != "Original body" {
		t.Errorf("version body = %q, want %q", body, "Original body")
	}
}

func TestDBFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	db.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file should exist after OpenDB")
	}
}
