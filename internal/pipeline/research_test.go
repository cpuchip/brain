package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestAdvanceResearchedToPlannedRequiresPool(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Study topic", "study")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	// researched → planned now runs plan pass, which requires pool
	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionAdvance,
	})
	if err == nil {
		t.Fatal("expected error when pool is nil for researched→planned (plan pass)")
	}
	if !strings.Contains(err.Error(), "agent pool not available") {
		t.Errorf("expected pool error, got: %v", err)
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

func TestAdvanceResearchedRequiresPool(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Plan this idea", "ideas")
	if err := db.SetMaturity(id, "researched", ""); err != nil {
		t.Fatal(err)
	}

	// researched → planned needs pool for plan pass
	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID: id,
		Action:  ActionAdvance,
	})
	if err == nil {
		t.Fatal("expected error when pool is nil for researched→planned")
	}
	if !strings.Contains(err.Error(), "agent pool not available") {
		t.Errorf("expected pool error, got: %v", err)
	}
}

func TestRevisePlannedRequiresPool(t *testing.T) {
	st, db := setupTestDB(t)
	p := New(st, nil, &config.Config{}, config.WorkspaceConfig{})

	id := insertEntry(t, db, "Revise this plan", "projects")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	// revise planned re-runs plan pass — needs pool
	_, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID:  id,
		Action:   ActionRevise,
		Feedback: "need more detail on phases",
	})
	if err == nil {
		t.Fatal("expected error when pool is nil for plan revision")
	}
	if !strings.Contains(err.Error(), "agent pool not available") {
		t.Errorf("expected pool error, got: %v", err)
	}
}

func TestAdvancePlannedToSpeccedGeneratesProposal(t *testing.T) {
	st, db := setupTestDB(t)
	tmpDir := t.TempDir()
	p := New(st, nil, &config.Config{BrainCodeDir: filepath.Join(tmpDir, "scripts", "brain")}, config.WorkspaceConfig{})
	// Override workspace to tmpDir so proposal gets written there
	p.workspace = tmpDir

	id := insertEntry(t, db, "Build Feature X", "projects")
	if err := db.SetMaturity(id, "planned", ""); err != nil {
		t.Fatal(err)
	}

	scenarios := []string{"Happy path works", "Error returns 400", "Edge case handled"}
	result, err := p.Advance(context.Background(), AdvanceRequest{
		EntryID:   id,
		Action:    ActionAdvance,
		Scenarios: scenarios,
	})
	if err != nil {
		t.Fatalf("Advance(planned→specced): %v", err)
	}
	if result.NewMaturity != "specced" {
		t.Errorf("NewMaturity = %q, want %q", result.NewMaturity, "specced")
	}
	if !strings.Contains(result.Message, "Proposal:") {
		t.Errorf("expected proposal path in message, got: %s", result.Message)
	}

	// Verify proposal file was created
	proposalPath := filepath.Join(tmpDir, ".spec", "proposals", "build-feature-x.md")
	data, err := os.ReadFile(proposalPath)
	if err != nil {
		t.Fatalf("proposal file not found: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Build Feature X") {
		t.Error("proposal missing title")
	}
	if !strings.Contains(content, "Happy path works") {
		t.Error("proposal missing scenarios")
	}
}

func TestBuildPlanPrompt(t *testing.T) {
	entry := &store.Entry{
		ID:       "test-id",
		Title:    "Test Entry",
		Category: "ideas",
		Body:     "Some idea content",
		Tags:     []string{"brain", "pipeline"},
	}

	// Without existing scratch
	prompt := buildPlanPrompt(entry, entry.Body, "/tmp/scratch.md", "", "")
	if !strings.Contains(prompt, "Test Entry") {
		t.Error("prompt missing title")
	}
	if !strings.Contains(prompt, "brain, pipeline") {
		t.Error("prompt missing tags")
	}
	if !strings.Contains(prompt, "No existing scratch file") {
		t.Error("prompt should warn about missing scratch")
	}

	// With existing scratch
	prompt = buildPlanPrompt(entry, entry.Body, "/tmp/scratch.md", "## Research findings\nSome findings here", "focus on phase 1")
	if !strings.Contains(prompt, "Research findings") {
		t.Error("prompt should include existing scratch content")
	}
	if !strings.Contains(prompt, "focus on phase 1") {
		t.Error("prompt should include human guidance")
	}
}

func TestGenerateProposal(t *testing.T) {
	st, db := setupTestDB(t)
	tmpDir := t.TempDir()
	p := New(st, nil, &config.Config{BrainCodeDir: filepath.Join(tmpDir, "scripts", "brain")}, config.WorkspaceConfig{})
	p.workspace = tmpDir

	id := insertEntry(t, db, "My Cool Project", "projects")
	entry, _ := db.GetEntry(id)

	scenarios := []string{"Scenario 1", "Scenario 2"}
	proposalPath, err := p.generateProposal(entry, scenarios)
	if err != nil {
		t.Fatalf("generateProposal: %v", err)
	}

	if proposalPath != filepath.Join(".spec", "proposals", "my-cool-project.md") {
		t.Errorf("unexpected proposal path: %s", proposalPath)
	}

	absPath := filepath.Join(tmpDir, proposalPath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("reading proposal: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "My Cool Project") {
		t.Error("proposal missing title")
	}
	if !strings.Contains(content, "specced") {
		t.Error("proposal missing status")
	}
	if !strings.Contains(content, "Scenario 1") {
		t.Error("proposal missing scenarios")
	}
}
