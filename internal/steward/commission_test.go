package steward

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cpuchip/brain/internal/store"
)

// mockCommissionRunner implements CommissionRunner for testing.
type mockCommissionRunner struct {
	mu              sync.Mutex
	advanceCalls    []string       // entry IDs
	advanceCount    map[string]int // how many times RetryAdvance called per entry
	executeCalls    []string
	gateResults     map[string]gateResult // keyed by entryID
	scenarioResults map[string][]string   // keyed by entryID
	verifyResults   map[string]verifyResult
	failAdvance     map[string]error
	failExecute     map[string]error
	failGate        map[string]error
	failScenarios   map[string]error
	failVerify      map[string]error
	advanceSequence map[string][]string // ordered list of maturities to set per entry
	db              *store.DB           // to update maturity after "advance"
}

type gateResult struct {
	action, reasoning, feedback string
}

type verifyResult struct {
	passed    bool
	reasoning string
}

func newMockCommissionRunner(db *store.DB) *mockCommissionRunner {
	return &mockCommissionRunner{
		advanceCount:    make(map[string]int),
		gateResults:     make(map[string]gateResult),
		scenarioResults: make(map[string][]string),
		verifyResults:   make(map[string]verifyResult),
		failAdvance:     make(map[string]error),
		failExecute:     make(map[string]error),
		failGate:        make(map[string]error),
		failScenarios:   make(map[string]error),
		failVerify:      make(map[string]error),
		advanceSequence: make(map[string][]string),
		db:              db,
	}
}

func (m *mockCommissionRunner) RetryAdvance(ctx context.Context, entryID, feedback, model string) error {
	m.mu.Lock()
	m.advanceCalls = append(m.advanceCalls, entryID)
	if err, ok := m.failAdvance[entryID]; ok {
		m.mu.Unlock()
		return err
	}
	// Use the sequence to set the right maturity for each call
	callIdx := m.advanceCount[entryID]
	m.advanceCount[entryID] = callIdx + 1
	seq := m.advanceSequence[entryID]
	m.mu.Unlock()

	if callIdx < len(seq) {
		m.db.SetMaturity(entryID, seq[callIdx], "")
	}
	return nil
}

func (m *mockCommissionRunner) RetryExecute(ctx context.Context, entryID, feedback, model string) error {
	m.mu.Lock()
	m.executeCalls = append(m.executeCalls, entryID)
	if err, ok := m.failExecute[entryID]; ok {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	// Simulate execution: set maturity to executing, then complete
	m.db.SetMaturity(entryID, "executing", "")
	m.db.UpdateRouteStatus(entryID, "your_turn") // execution done
	return nil
}

func (m *mockCommissionRunner) EvaluateGate(ctx context.Context, entryID, model string) (action, reasoning, feedback string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.failGate[entryID]; ok {
		return "", "", "", err
	}
	if r, ok := m.gateResults[entryID]; ok {
		return r.action, r.reasoning, r.feedback, nil
	}
	return "advance", "output looks good", "", nil
}

func (m *mockCommissionRunner) GenerateScenarios(ctx context.Context, entryID, model string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.failScenarios[entryID]; ok {
		return nil, err
	}
	if s, ok := m.scenarioResults[entryID]; ok {
		return s, nil
	}
	// Default: set scenarios and advance to specced
	scenarios := []string{"builds without errors", "UI renders correctly", "tests pass"}
	m.db.SetScenarios(entryID, "- builds without errors\n- UI renders correctly\n- tests pass")
	m.db.SetMaturity(entryID, "specced", "")
	return scenarios, nil
}

func (m *mockCommissionRunner) EvaluateAndVerify(ctx context.Context, entryID, model string) (passed bool, reasoning string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.failVerify[entryID]; ok {
		return false, "", err
	}
	if r, ok := m.verifyResults[entryID]; ok {
		if r.passed {
			m.db.SetMaturity(entryID, "verified", "All scenarios passed")
		} else {
			m.db.SetMaturity(entryID, "planned", "Verification failed")
		}
		return r.passed, r.reasoning, nil
	}
	// Default: pass and set verified
	m.db.SetMaturity(entryID, "verified", "All scenarios passed")
	return true, "all scenarios passed", nil
}

// --- DB CRUD Tests ---

func TestCreateCommission(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "Test Commission Entry")

	c := &store.Commission{
		EntryID:   entryID,
		Intent:    "Build the thing",
		Scope:     "single",
		Authority: "advance_and_execute",
		Model:     "claude-opus-4.7",
		MaxCost:   50.0,
		Status:    "active",
	}
	if err := st.DB().CreateCommission(c); err != nil {
		t.Fatalf("CreateCommission: %v", err)
	}
	if c.ID == "" {
		t.Fatal("expected commission ID to be set")
	}
}

func TestGetCommission(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "Test Get Commission")

	c := &store.Commission{
		EntryID:   entryID,
		Intent:    "Ship it",
		Scope:     "single",
		Authority: "advance_and_execute",
		Model:     "claude-opus-4.7",
		MaxCost:   25.0,
		Status:    "active",
	}
	st.DB().CreateCommission(c)

	got, err := st.DB().GetCommission(c.ID)
	if err != nil {
		t.Fatalf("GetCommission: %v", err)
	}
	if got.Intent != "Ship it" {
		t.Errorf("intent = %q, want %q", got.Intent, "Ship it")
	}
	if got.MaxCost != 25.0 {
		t.Errorf("max_cost = %f, want 25.0", got.MaxCost)
	}
}

func TestListCommissions(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "List Entry")

	for i := 0; i < 3; i++ {
		c := &store.Commission{
			EntryID: entryID,
			Intent:  fmt.Sprintf("Commission %d", i),
			Scope:   "single",
			Status:  "active",
		}
		st.DB().CreateCommission(c)
	}

	list, err := st.DB().ListCommissions()
	if err != nil {
		t.Fatalf("ListCommissions: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len = %d, want 3", len(list))
	}
}

func TestUpdateCommissionStatus(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "Status Entry")

	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	if err := st.DB().UpdateCommissionStatus(c.ID, "completed"); err != nil {
		t.Fatalf("UpdateCommissionStatus: %v", err)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "completed" {
		t.Errorf("status = %q, want %q", got.Status, "completed")
	}
}

func TestUpdateCommissionCost(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "Cost Entry")

	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	if err := st.DB().UpdateCommissionCost(c.ID, 12.5); err != nil {
		t.Fatalf("UpdateCommissionCost: %v", err)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.CostUsed != 12.5 {
		t.Errorf("cost_used = %f, want 12.5", got.CostUsed)
	}
}

func TestAddCommissionDecision(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "Decision Entry")

	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	dec := &store.CommissionDecision{
		CommissionID: c.ID,
		Timestamp:    time.Now(),
		EntryID:      entryID,
		Stage:        "research",
		Action:       "advance",
		Reasoning:    "Output looks good",
		Cost:         3.0,
	}
	if err := st.DB().AddCommissionDecision(dec); err != nil {
		t.Fatalf("AddCommissionDecision: %v", err)
	}

	// Verify via GetCommission (loads decisions)
	got, _ := st.DB().GetCommission(c.ID)
	if len(got.Decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(got.Decisions))
	}
	if got.Decisions[0].Action != "advance" {
		t.Errorf("decision action = %q, want %q", got.Decisions[0].Action, "advance")
	}
}

func TestGetActiveCommissionForEntry(t *testing.T) {
	st := setupTestStore(t)
	entryID := insertTestEntry(t, st, "Active Commission Entry")

	// No active commission
	_, err := st.DB().GetActiveCommissionForEntry(entryID)
	if err == nil {
		t.Fatal("expected error for no active commission")
	}

	// Create active commission
	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	got, err := st.DB().GetActiveCommissionForEntry(entryID)
	if err != nil {
		t.Fatalf("GetActiveCommissionForEntry: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("id = %q, want %q", got.ID, c.ID)
	}
}

// --- Steward Commission Logic Tests ---

func TestCreateCommissionValidation(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())
	s.SetCommissionRunner(runner)

	// Non-existent entry
	_, err := s.CreateCommission("nonexistent", "test", "", "", 0)
	if err == nil {
		t.Fatal("expected error for non-existent entry")
	}

	// Valid entry
	entryID := insertTestEntry(t, st, "Valid Entry")
	c, err := s.CreateCommission(entryID, "Ship it", "", "", 0)
	if err != nil {
		t.Fatalf("CreateCommission: %v", err)
	}
	if c.Status != "active" {
		t.Errorf("status = %q, want %q", c.Status, "active")
	}
	if c.Authority != "advance_and_execute" {
		t.Errorf("authority = %q, want %q", c.Authority, "advance_and_execute")
	}
	if c.Model != "claude-opus-4.7" {
		t.Errorf("model = %q, want %q", c.Model, "claude-opus-4.7")
	}
	if c.MaxCost != 50.0 {
		t.Errorf("max_cost = %f, want 50.0", c.MaxCost)
	}
}

func TestCreateCommissionDuplicateBlocked(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())
	s.SetCommissionRunner(runner)

	entryID := insertTestEntry(t, st, "Duplicate Entry")

	_, err := s.CreateCommission(entryID, "first", "", "", 0)
	if err != nil {
		t.Fatalf("first CreateCommission: %v", err)
	}

	_, err = s.CreateCommission(entryID, "second", "", "", 0)
	if err == nil {
		t.Fatal("expected error for duplicate active commission")
	}
}

func TestPauseCommission(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	entryID := insertTestEntry(t, st, "Pause Entry")
	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	if err := s.PauseCommission(c.ID); err != nil {
		t.Fatalf("PauseCommission: %v", err)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "paused" {
		t.Errorf("status = %q, want %q", got.Status, "paused")
	}
}

func TestPauseCommissionNotActive(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	entryID := insertTestEntry(t, st, "Not Active Entry")
	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "completed"}
	st.DB().CreateCommission(c)

	if err := s.PauseCommission(c.ID); err == nil {
		t.Fatal("expected error for pausing non-active commission")
	}
}

func TestResumeCommission(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())
	s.SetCommissionRunner(runner)

	entryID := insertTestEntry(t, st, "Resume Entry")
	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "paused"}
	st.DB().CreateCommission(c)

	if err := s.ResumeCommission(c.ID); err != nil {
		t.Fatalf("ResumeCommission: %v", err)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "active" {
		t.Errorf("status = %q, want %q", got.Status, "active")
	}
}

func TestRevokeCommission(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	entryID := insertTestEntry(t, st, "Revoke Entry")
	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	if err := s.RevokeCommission(c.ID); err != nil {
		t.Fatalf("RevokeCommission: %v", err)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "revoked" {
		t.Errorf("status = %q, want %q", got.Status, "revoked")
	}
}

func TestRevokeCommissionCancelsGoroutine(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	entryID := insertTestEntry(t, st, "Cancel Goroutine Entry")
	c := &store.Commission{EntryID: entryID, Intent: "test", Scope: "single", Status: "active"}
	st.DB().CreateCommission(c)

	// Simulate a running goroutine
	ctx, cancel := context.WithCancel(context.Background())
	s.commission.mu.Lock()
	if s.commission.running == nil {
		s.commission.running = make(map[string]context.CancelFunc)
	}
	s.commission.running[c.ID] = cancel
	s.commission.mu.Unlock()

	if err := s.RevokeCommission(c.ID); err != nil {
		t.Fatalf("RevokeCommission: %v", err)
	}

	// Verify context was cancelled
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("expected goroutine context to be cancelled")
	}

	// Verify removed from running map
	s.commission.mu.Lock()
	_, exists := s.commission.running[c.ID]
	s.commission.mu.Unlock()
	if exists {
		t.Error("expected commission to be removed from running map")
	}
}

func TestRunCommissionFullLifecycle(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())

	entryID := insertTestEntry(t, st, "Full Lifecycle Entry")

	// Set up maturity progression:
	// RetryAdvance call 1: raw → researched
	// RetryAdvance call 2: researched → planned
	// (planned → specced handled by GenerateScenarios mock)
	// (specced → executing handled by RetryExecute mock)
	// (executing → verified handled by EvaluateAndVerify mock)
	runner.advanceSequence[entryID] = []string{"researched", "planned"}

	// Gate evaluations: all advance
	runner.gateResults[entryID] = gateResult{action: "advance", reasoning: "looks good"}

	// Verify: passes
	runner.verifyResults[entryID] = verifyResult{passed: true, reasoning: "all scenarios passed"}

	s.SetCommissionRunner(runner)

	c, err := s.CreateCommission(entryID, "Full lifecycle test", "", "", 100.0)
	if err != nil {
		t.Fatalf("CreateCommission: %v", err)
	}

	// Wait for the commission goroutine to complete
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := st.DB().GetCommission(c.ID)
		if got.Status == "completed" || got.Status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "completed" {
		t.Errorf("commission status = %q, want completed (cost: %.1f, decisions: %d)", got.Status, got.CostUsed, len(got.Decisions))
		for _, d := range got.Decisions {
			t.Logf("  decision: stage=%s action=%s reasoning=%s", d.Stage, d.Action, d.Reasoning)
		}
	}

	// Check that decisions were recorded
	if len(got.Decisions) == 0 {
		t.Error("expected at least one decision recorded")
	}

	// Check cost was tracked
	if got.CostUsed <= 0 {
		t.Error("expected cost to be tracked")
	}
}

func TestRunCommissionBudgetExceeded(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())

	entryID := insertTestEntry(t, st, "Budget Entry")

	// Entry starts at raw, but we'll give it a tiny budget
	runner.advanceSequence[entryID] = []string{"researched", "planned"}
	runner.gateResults[entryID] = gateResult{action: "advance", reasoning: "looks good"}

	s.SetCommissionRunner(runner)

	// Create commission with tiny budget that will be exceeded quickly
	c := &store.Commission{
		EntryID:   entryID,
		Intent:    "Budget test",
		Scope:     "single",
		Authority: "advance_and_execute",
		Model:     "claude-opus-4.7",
		MaxCost:   0.1, // Tiny budget — will be exceeded immediately
		Status:    "active",
	}
	st.DB().CreateCommission(c)

	go s.runCommission(c.ID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := st.DB().GetCommission(c.ID)
		if got.Status == "failed" || got.Status == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "failed" {
		t.Errorf("status = %q, want %q", got.Status, "failed")
	}
}

func TestRunCommissionSurface(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())

	entryID := insertTestEntry(t, st, "Surface Entry")

	// Gate says surface
	runner.advanceSequence[entryID] = []string{"researched"}
	runner.gateResults[entryID] = gateResult{action: "surface", reasoning: "needs human input on direction"}

	s.SetCommissionRunner(runner)

	c, err := s.CreateCommission(entryID, "Surface test", "", "", 50.0)
	if err != nil {
		t.Fatalf("CreateCommission: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := st.DB().GetCommission(c.ID)
		if got.Status == "paused" || got.Status == "completed" || got.Status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "paused" {
		t.Errorf("status = %q, want %q (surfaced commissions should be paused)", got.Status, "paused")
	}
}

func TestRunCommissionAdvanceOnlyStopsAtSpecced(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())

	entryID := insertTestEntry(t, st, "Advance Only Entry")

	// fast-forward to specced
	st.DB().SetMaturity(entryID, "specced", "")
	st.DB().SetScenarios(entryID, "- test scenario 1\n- test scenario 2")

	s.SetCommissionRunner(runner)

	c := &store.Commission{
		EntryID:   entryID,
		Intent:    "Advance only test",
		Scope:     "single",
		Authority: "advance_only",
		Model:     "claude-opus-4.7",
		MaxCost:   50.0,
		Status:    "active",
	}
	st.DB().CreateCommission(c)

	go s.runCommission(c.ID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := st.DB().GetCommission(c.ID)
		if got.Status != "active" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "paused" {
		t.Errorf("status = %q, want paused (advance_only should surface at specced)", got.Status)
	}
}

func TestRunCommissionNoRunner(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	// No CommissionRunner set

	entryID := insertTestEntry(t, st, "No Runner Entry")

	c := &store.Commission{
		EntryID: entryID,
		Intent:  "No runner test",
		Scope:   "single",
		Status:  "active",
	}
	st.DB().CreateCommission(c)

	s.runCommission(c.ID)

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
}

func TestRunCommissionStageFails(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())
	runner := newMockCommissionRunner(st.DB())

	entryID := insertTestEntry(t, st, "Stage Fail Entry")

	// Advance will fail
	runner.failAdvance[entryID] = fmt.Errorf("research agent crashed")

	s.SetCommissionRunner(runner)

	c := &store.Commission{
		EntryID:   entryID,
		Intent:    "Stage failure test",
		Scope:     "single",
		Authority: "advance_and_execute",
		Model:     "claude-opus-4.7",
		MaxCost:   50.0,
		Status:    "active",
	}
	st.DB().CreateCommission(c)

	go s.runCommission(c.ID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := st.DB().GetCommission(c.ID)
		if got.Status == "failed" || got.Status == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, _ := st.DB().GetCommission(c.ID)
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}

	// Check decision recorded
	if len(got.Decisions) == 0 {
		t.Error("expected failure decision to be recorded")
	}
}

func TestActiveCommissionCount(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	if s.activeCommissionCount() != 0 {
		t.Errorf("initial count = %d, want 0", s.activeCommissionCount())
	}

	// Add a fake running commission
	s.commission.mu.Lock()
	if s.commission.running == nil {
		s.commission.running = make(map[string]context.CancelFunc)
	}
	s.commission.running["test-1"] = func() {}
	s.commission.mu.Unlock()

	if s.activeCommissionCount() != 1 {
		t.Errorf("count = %d, want 1", s.activeCommissionCount())
	}
}

func TestStatusIncludesActiveCommissions(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	status := s.Status()
	if status.ActiveCommissions != 0 {
		t.Errorf("initial ActiveCommissions = %d, want 0", status.ActiveCommissions)
	}
}

func TestModelCost(t *testing.T) {
	st := setupTestStore(t)
	s := New(st, DefaultConfig())

	tests := []struct {
		model string
		want  float64
	}{
		{"claude-haiku-4.5", 0.33},
		{"claude-sonnet-4.6", 1.0},
		{"claude-opus-4.7", 7.5},
		{"unknown-model", 1.0}, // default
	}

	for _, tt := range tests {
		got := s.modelCost(tt.model)
		if got != tt.want {
			t.Errorf("modelCost(%q) = %f, want %f", tt.model, got, tt.want)
		}
	}
}
