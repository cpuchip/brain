package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

func setupTestDB(t *testing.T) (*store.Store, *store.DB) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := store.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db, nil, nil)
	return st, db
}

func insertEntry(t *testing.T, db *store.DB, title, category string) string {
	t.Helper()
	entry := &store.Entry{
		Title:    title,
		Category: category,
		Body:     "Test body for " + title,
		Source:   "test",
	}
	id, err := db.InsertEntry(entry)
	if err != nil {
		t.Fatalf("InsertEntry: %v", err)
	}
	return id
}

func TestAdvanceReject(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Test idea", "ideas")

	// Set maturity to "researched" first
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	// Reject should go back to raw
	result, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionReject,
	})
	if err != nil {
		t.Fatalf("Advance(reject): %v", err)
	}
	if result.NewMaturity != "raw" {
		t.Errorf("NewMaturity = %q, want %q", result.NewMaturity, "raw")
	}

	// Verify in DB
	entry, _ := db.GetEntry(id)
	if entry.Maturity != "raw" {
		t.Errorf("DB maturity = %q, want %q", entry.Maturity, "raw")
	}
}

func TestAdvanceDefer(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Test project", "projects")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	result, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionDefer,
	})
	if err != nil {
		t.Fatalf("Advance(defer): %v", err)
	}
	if result.NewMaturity != "planned" {
		t.Errorf("NewMaturity = %q, want %q", result.NewMaturity, "planned")
	}

	// Verify notes recorded
	entry, _ := db.GetEntry(id)
	if entry.MaturityNotes == "" {
		t.Error("expected maturity notes to be set")
	}
}

func TestAdvanceResearchedToPlanned(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Study topic", "study")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	result, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionAdvance,
	})
	if err != nil {
		t.Fatalf("Advance(researched→planned): %v", err)
	}
	if result.NewMaturity != "planned" {
		t.Errorf("NewMaturity = %q, want %q", result.NewMaturity, "planned")
	}
}

func TestAdvancePlannedToSpecced(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Build feature X", "projects")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	// Should fail without scenarios
	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionAdvance,
	})
	if err == nil {
		t.Fatal("expected error when advancing to specced without scenarios")
	}

	// Should succeed with scenarios
	result, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID:   id,
		Action:    ActionAdvance,
		Scenarios: []string{"Happy path", "Error case", "Edge case"},
	})
	if err != nil {
		t.Fatalf("Advance(planned→specced): %v", err)
	}
	if result.NewMaturity != "specced" {
		t.Errorf("NewMaturity = %q, want %q", result.NewMaturity, "specced")
	}
}

func TestAdvanceNonPipelineCategory(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Buy groceries", "actions")

	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionAdvance,
	})
	if err == nil {
		t.Fatal("expected error for non-pipeline category")
	}
}

func TestAdvanceRawRequiresPool(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Research this idea", "ideas")
	// raw → researched needs pool for research pass

	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionAdvance,
	})
	if err == nil {
		t.Fatal("expected error when pool is nil for raw→researched")
	}
}

func TestReviseRequiresFeedback(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Revise me", "ideas")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	// revise without feedback should fail
	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionRevise,
	})
	if err == nil {
		t.Fatal("expected error when revising without feedback")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Build MCP Server for Brain", "build-mcp-server-for-brain"},
		{"Hello, World!", "hello-world"},
		{"  spaces  and  tabs  ", "spaces-and-tabs"},
		{"", "untitled"},
		{"UPPER CASE", "upper-case"},
	}
	for _, tc := range tests {
		got := slugify(tc.input)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
