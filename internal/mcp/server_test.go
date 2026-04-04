package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/brain/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
)

func setupTestStore(t *testing.T) (*store.Store, *store.DB) {
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

func callTool(t *testing.T, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

func TestHandleAdvanceHappyPath(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Test idea", "ideas")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":     id,
		"action": "advance",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if text == "" {
		t.Error("expected non-empty result text")
	}

	// Verify entry advanced
	entry, _ := db.GetEntry(id)
	if entry.Maturity != "planned" {
		t.Errorf("maturity = %q, want %q", entry.Maturity, "planned")
	}
}

func TestHandleAdvanceReject(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Bad idea", "ideas")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":     id,
		"action": "reject",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	entry, _ := db.GetEntry(id)
	if entry.Maturity != "raw" {
		t.Errorf("maturity = %q, want %q", entry.Maturity, "raw")
	}
}

func TestHandleAdvanceInvalidAction(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Test", "ideas")

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":     id,
		"action": "yolo",
	})

	if !result.IsError {
		t.Error("expected error for invalid action")
	}
}

func TestHandleAdvanceNonPipeline(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Journal entry", "journal")

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":     id,
		"action": "advance",
	})

	if !result.IsError {
		t.Error("expected error for non-pipeline category")
	}
}

func TestHandleReviewBasic(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Reviewable idea", "ideas")
	if err := db.SetMaturity(id, "researched", "some notes"); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleReview, map[string]any{
		"id": id,
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "" {
		t.Error("expected non-empty review text")
	}
	// Should contain the title, maturity, notes
	for _, want := range []string{"Reviewable idea", "researched", "some notes"} {
		if !contains(text, want) {
			t.Errorf("review text missing %q", want)
		}
	}
}

func TestHandleReviewWithScratchFile(t *testing.T) {
	st, db := setupTestStore(t)
	tmpDir := t.TempDir()
	srv := New(st, tmpDir)

	id := insertEntry(t, db, "Idea with scratch", "ideas")
	scratchPath := filepath.Join(".spec", "scratch", "idea-with-scratch", "main.md")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetScratchPath(id, scratchPath); err != nil {
		t.Fatal(err)
	}

	// Create the scratch file
	absPath := filepath.Join(tmpDir, scratchPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte("# Research Findings\n\nSome findings here."), 0644); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleReview, map[string]any{
		"id": id,
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !contains(text, "Research Findings") {
		t.Error("review should include scratch file contents")
	}
	if !contains(text, "Some findings here.") {
		t.Error("review should include scratch file body")
	}
}

func TestHandleAdvanceDefer(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Defer this", "projects")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":       id,
		"action":   "defer",
		"feedback": "not ready yet",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	entry, _ := db.GetEntry(id)
	if entry.Maturity != "researched" {
		t.Errorf("maturity = %q, want %q (should stay the same)", entry.Maturity, "researched")
	}
	if entry.MaturityNotes == "" {
		t.Error("expected defer notes to be recorded")
	}
}

func TestHandleAdvanceReviseNeedsFeedback(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Revise me", "study")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":     id,
		"action": "revise",
	})

	if !result.IsError {
		t.Error("expected error when revising without feedback")
	}

	// With feedback should succeed
	result = callTool(t, srv.handleAdvance, map[string]any{
		"id":       id,
		"action":   "revise",
		"feedback": "needs more scriptural depth",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	entry, _ := db.GetEntry(id)
	if !contains(entry.MaturityNotes, "needs more scriptural depth") {
		t.Errorf("notes = %q, should contain feedback", entry.MaturityNotes)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || findSubstring(haystack, needle))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestHandleAdvancePlannedToSpeccedRequiresScenarios(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Needs scenarios", "ideas")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	// Without scenarios should fail
	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":     id,
		"action": "advance",
	})
	if !result.IsError {
		t.Error("expected error when advancing planned→specced without scenarios")
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !contains(text, "scenarios") {
		t.Errorf("error should mention scenarios, got: %s", text)
	}
}

func TestHandleAdvancePlannedToSpeccedWithScenarios(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Ready to spec", "projects")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":        id,
		"action":    "advance",
		"scenarios": "Happy path works\nError returns 400\nEdge case handled",
	})
	if result.IsError {
		text := result.Content[0].(mcp.TextContent).Text
		t.Fatalf("unexpected error: %s", text)
	}

	entry, _ := db.GetEntry(id)
	if entry.Maturity != "specced" {
		t.Errorf("maturity = %q, want %q", entry.Maturity, "specced")
	}
	if !contains(entry.Scenarios, "Happy path works") {
		t.Errorf("scenarios = %q, should contain 'Happy path works'", entry.Scenarios)
	}
	if !contains(entry.Scenarios, "Error returns 400") {
		t.Errorf("scenarios = %q, should contain 'Error returns 400'", entry.Scenarios)
	}
}

func TestHandleAdvancePlannedToSpeccedEmptyScenarios(t *testing.T) {
	st, db := setupTestStore(t)
	srv := New(st)

	id := insertEntry(t, db, "Empty scenarios", "ideas")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	// Empty string should fail
	result := callTool(t, srv.handleAdvance, map[string]any{
		"id":        id,
		"action":    "advance",
		"scenarios": "",
	})
	if !result.IsError {
		t.Error("expected error for empty scenarios string")
	}

	// Whitespace-only lines should fail
	result = callTool(t, srv.handleAdvance, map[string]any{
		"id":        id,
		"action":    "advance",
		"scenarios": "\n  \n\n",
	})
	if !result.IsError {
		t.Error("expected error for whitespace-only scenarios")
	}
}
