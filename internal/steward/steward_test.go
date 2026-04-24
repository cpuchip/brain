package steward

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cpuchip/brain/internal/store"
)

// setupTestStore creates a temporary SQLite DB for integration tests.
func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := store.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.New(db, nil, nil)
}

// insertTestEntry creates a minimal entry and returns its ID.
func insertTestEntry(t *testing.T, st *store.Store, title string) string {
	t.Helper()
	entry := &store.Entry{Title: title, Category: "idea"}
	id, err := st.DB().InsertEntry(entry)
	if err != nil {
		t.Fatalf("InsertEntry: %v", err)
	}
	return id
}

func TestDiagnose(t *testing.T) {
	tests := []struct {
		name         string
		reason       string
		failureCount int
		want         FailureType
	}{
		// Timeout patterns
		{"context deadline exceeded", "context deadline exceeded", 1, FailureTimeout},
		{"context canceled", "context canceled", 1, FailureTimeout},
		{"timeout in message", "operation timed out waiting for response", 1, FailureTimeout},
		{"inactivity timeout", "agent terminated due to inactivity", 1, FailureTimeout},

		// Transient patterns
		{"rate limit 429", "received 429 rate limit from API", 1, FailureTransient},
		{"rate limit text", "rate limit exceeded, try again later", 1, FailureTransient},
		{"server error 500", "internal server error 500", 1, FailureTransient},
		{"bad gateway 502", "502 bad gateway", 1, FailureTransient},
		{"service unavailable 503", "503 service unavailable", 1, FailureTransient},
		{"connection refused", "dial tcp 127.0.0.1:8080: connection refused", 1, FailureTransient},
		{"temporary failure", "temporary failure in name resolution", 1, FailureTransient},
		{"ECONNRESET", "read tcp: ECONNRESET", 1, FailureTransient},

		// Tool error patterns
		{"tool call failed", "tool call to read_file failed", 1, FailureToolError},
		{"mcp error", "MCP server returned error", 1, FailureToolError},
		{"tool failed", "tool failed: invalid arguments", 1, FailureToolError},

		// Model limit (from repeated failures)
		{"repeated failures trigger model limit", "some random error", 2, FailureModelLimit},
		{"three failures is model limit", "generic error text", 3, FailureModelLimit},

		// Unknown fallback
		{"unknown error first time", "some random error", 1, FailureUnknown},
		{"unknown with zero count", "inexplicable failure", 0, FailureUnknown},

		// Case insensitivity
		{"uppercase TIMEOUT", "CONTEXT DEADLINE EXCEEDED", 1, FailureTimeout},
		{"mixed case Rate Limit", "Rate Limit exceeded", 1, FailureTransient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Diagnose(tt.reason, tt.failureCount)
			if got != tt.want {
				t.Errorf("Diagnose(%q, %d) = %s, want %s", tt.reason, tt.failureCount, got, tt.want)
			}
		})
	}
}

func TestBuildRetryContext(t *testing.T) {
	tests := []struct {
		name      string
		diagnosis FailureType
		reason    string
		attempt   int
		contains  []string // substrings the output should contain
	}{
		{
			"timeout guidance",
			FailureTimeout,
			"context deadline exceeded",
			1,
			[]string{"timed out", "smaller steps", "context deadline exceeded"},
		},
		{
			"transient guidance",
			FailureTransient,
			"429 rate limit",
			1,
			[]string{"transient", "429 rate limit"},
		},
		{
			"tool error guidance",
			FailureToolError,
			"tool call failed",
			1,
			[]string{"tool", "tool call failed"},
		},
		{
			"model limit guidance",
			FailureModelLimit,
			"repeated failure",
			2,
			[]string{"simplif", "repeated failure"},
		},
		{
			"unknown guidance",
			FailureUnknown,
			"mystery error",
			1,
			[]string{"mystery error"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildRetryContext(tt.diagnosis, tt.reason, tt.attempt)
			if got == "" {
				t.Fatal("BuildRetryContext returned empty string")
			}
			for _, substr := range tt.contains {
				if !strings.Contains(strings.ToLower(got), strings.ToLower(substr)) {
					t.Errorf("BuildRetryContext output should contain %q, got: %s", substr, got)
				}
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	cfg := DefaultConfig()
	s := New(nil, cfg)

	tests := []struct {
		attempt  int
		minDelay float64 // seconds
		maxDelay float64 // seconds, capped at BackoffMax
	}{
		{1, 30, 30},    // base * 2^0 = 30s
		{2, 60, 60},    // base * 2^1 = 60s
		{3, 120, 120},  // base * 2^2 = 120s
		{4, 240, 240},  // base * 2^3 = 240s
		{5, 300, 300},  // base * 2^4 = 480s but capped at 300s
		{10, 300, 300}, // way above cap
	}

	for _, tt := range tests {
		t.Run("attempt_"+string(rune('0'+tt.attempt)), func(t *testing.T) {
			got := s.backoff(tt.attempt)
			gotSecs := got.Seconds()
			if gotSecs < tt.minDelay || gotSecs > tt.maxDelay {
				t.Errorf("backoff(%d) = %v, want between %vs and %vs", tt.attempt, got, tt.minDelay, tt.maxDelay)
			}
		})
	}
}

func TestSetEnabled(t *testing.T) {
	s := New(nil, DefaultConfig())
	if !s.cfg.Enabled {
		t.Fatal("steward should be enabled by default")
	}

	s.SetEnabled(false)
	status := s.Status()
	if status.Enabled {
		t.Error("steward should be disabled after SetEnabled(false)")
	}

	s.SetEnabled(true)
	status = s.Status()
	if !status.Enabled {
		t.Error("steward should be enabled after SetEnabled(true)")
	}
}

func TestOnFailureDisabled(t *testing.T) {
	// When disabled, OnFailure should be a no-op (no panic, no action)
	s := New(nil, DefaultConfig())
	s.SetEnabled(false)
	// Should not panic even with nil retrier
	s.OnFailure("test-entry", "research", fmt.Errorf("test error"))
}

func TestOnFailureNoRetrier(t *testing.T) {
	cfg := DefaultConfig()
	s := New(nil, cfg)
	// Enabled but no retrier — should return immediately
	s.OnFailure("test-entry", "research", fmt.Errorf("test error"))
}

// --- Phase 2: Model Escalation Tests ---

func TestDefaultModelForStage(t *testing.T) {
	s := New(nil, DefaultConfig())

	tests := []struct {
		name     string
		stage    string
		maturity string
		want     string
	}{
		{"execute stage", "execute", "specced", "claude-sonnet-4.6"},
		{"specced maturity", "advance", "specced", "claude-sonnet-4.6"},
		{"researched maturity", "advance", "researched", "claude-opus-4.7"},
		{"planned maturity", "advance", "planned", "claude-opus-4.7"},
		{"raw maturity", "advance", "raw", "claude-sonnet-4.6"},
		{"empty maturity", "advance", "", "claude-sonnet-4.6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.defaultModelForStage(tt.stage, tt.maturity)
			if got != tt.want {
				t.Errorf("defaultModelForStage(%q, %q) = %q, want %q", tt.stage, tt.maturity, got, tt.want)
			}
		})
	}
}

func TestPickModel(t *testing.T) {
	s := New(nil, DefaultConfig())

	tests := []struct {
		name          string
		stage         string
		maturity      string
		failureCount  int
		diagnosis     FailureType
		wantModel     string
		wantEscalated bool
	}{
		// Transient: never escalate, use default (research now defaults to sonnet)
		{
			"transient first failure research",
			"advance", "raw", 1, FailureTransient,
			"claude-sonnet-4.6", false,
		},
		{
			"transient second failure research",
			"advance", "raw", 2, FailureTransient,
			"claude-sonnet-4.6", false,
		},

		// Model limit: always escalate
		{
			"model_limit first failure → escalate sonnet to opus",
			"advance", "raw", 1, FailureModelLimit,
			"claude-opus-4.7", true, // model can't handle it, go up one tier
		},

		// Timeout: escalate on 2nd+
		{
			"timeout first failure → no escalation",
			"advance", "raw", 1, FailureTimeout,
			"claude-sonnet-4.6", false,
		},
		{
			"timeout second failure → escalate sonnet to opus",
			"advance", "raw", 2, FailureTimeout,
			"claude-opus-4.7", true,
		},
		{
			"timeout third failure → chain exhausted from sonnet",
			"advance", "raw", 3, FailureTimeout,
			"", false, // sonnet(1) + 2 escalation steps = idx 3, beyond chain
		},

		// Tool error: escalate on 2nd+
		{
			"tool_error first failure → no escalation",
			"advance", "raw", 1, FailureToolError,
			"claude-sonnet-4.6", false,
		},
		{
			"tool_error second failure → escalate",
			"advance", "raw", 2, FailureToolError,
			"claude-opus-4.7", true,
		},

		// Execute stage (default: sonnet)
		{
			"timeout second on execute → escalate sonnet to opus",
			"execute", "specced", 2, FailureTimeout,
			"claude-opus-4.7", true,
		},
		{
			"timeout third on execute → chain exhausted",
			"execute", "specced", 3, FailureTimeout,
			"", false, // beyond chain
		},

		// Plan stage (default: opus) — already at top
		{
			"timeout second on plan → chain exhausted",
			"advance", "researched", 2, FailureTimeout,
			"", false, // opus is already top of chain
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &store.Entry{
				ID:           "test",
				Maturity:     tt.maturity,
				FailureCount: tt.failureCount,
			}
			gotModel, gotEscalated := s.pickModel(entry, tt.stage, tt.diagnosis)
			if gotModel != tt.wantModel || gotEscalated != tt.wantEscalated {
				t.Errorf("pickModel(stage=%q, maturity=%q, failures=%d, diag=%s) = (%q, %v), want (%q, %v)",
					tt.stage, tt.maturity, tt.failureCount, tt.diagnosis,
					gotModel, gotEscalated, tt.wantModel, tt.wantEscalated)
			}
		})
	}
}

func TestPickModelModelLimitAlwaysEscalates(t *testing.T) {
	s := New(nil, DefaultConfig())

	// model_limit with failureCount=1: shouldEscalate=true, nextIdx=defaultIdx+1
	// Research default is now sonnet(1) → opus(2), always escalated even on first failure
	entry := &store.Entry{ID: "test", Maturity: "raw", FailureCount: 1}
	model, escalated := s.pickModel(entry, "advance", FailureModelLimit)
	if model != "claude-opus-4.7" || !escalated {
		t.Errorf("model_limit first failure: got (%q, %v), want (opus, true)", model, escalated)
	}

	// failureCount=2 → escalate further: sonnet(1) + escalationSteps(1) → opus(2)
	entry.FailureCount = 2
	model, escalated = s.pickModel(entry, "advance", FailureModelLimit)
	if model != "claude-opus-4.7" || !escalated {
		t.Errorf("model_limit second failure: got (%q, %v), want (opus, true)", model, escalated)
	}

	// failureCount=3 → escalate further: sonnet(1) + escalationSteps(2) → idx 3, exhausted
	entry.FailureCount = 3
	model, escalated = s.pickModel(entry, "advance", FailureModelLimit)
	if model != "" {
		t.Errorf("model_limit third failure: got model %q, want empty (chain exhausted from sonnet)", model)
	}

	// failureCount=4 → chain exhausted
	entry.FailureCount = 4
	model, escalated = s.pickModel(entry, "advance", FailureModelLimit)
	if model != "" {
		t.Errorf("model_limit fourth failure: got model %q, want empty (chain exhausted)", model)
	}
}

func TestDefaultConfigEscalationChain(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.EscalationChain) != 3 {
		t.Fatalf("EscalationChain length = %d, want 3", len(cfg.EscalationChain))
	}
	expected := []struct {
		model string
		cost  float64
	}{
		{"claude-haiku-4.5", 0.33},
		{"claude-sonnet-4.6", 1.0},
		{"claude-opus-4.7", 7.5},
	}
	for i, want := range expected {
		got := cfg.EscalationChain[i]
		if got.Model != want.model || got.Cost != want.cost {
			t.Errorf("chain[%d] = {%q, %.2f}, want {%q, %.2f}", i, got.Model, got.Cost, want.model, want.cost)
		}
	}
	if cfg.MaxCostPerEntry != 20.0 {
		t.Errorf("MaxCostPerEntry = %.1f, want 20.0", cfg.MaxCostPerEntry)
	}
}

func TestStatusIncludesEscalationFields(t *testing.T) {
	s := New(nil, DefaultConfig())
	status := s.Status()
	if status.TotalEscalations != 0 {
		t.Errorf("initial TotalEscalations = %d, want 0", status.TotalEscalations)
	}
	if status.MaxCostPerEntry != 20.0 {
		t.Errorf("MaxCostPerEntry = %.1f, want 20.0", status.MaxCostPerEntry)
	}
}

// --- Phase 4: Quarantine Tests ---

func TestSetQuarantined(t *testing.T) {
	st := setupTestStore(t)
	id := insertTestEntry(t, st, "quarantine test")

	// Initially not quarantined
	entry, err := st.DB().GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if entry.Quarantined {
		t.Error("entry should not be quarantined initially")
	}
	if entry.QuarantinedAt != "" {
		t.Error("quarantined_at should be empty initially")
	}

	// Quarantine it
	if err := st.DB().SetQuarantined(id, true); err != nil {
		t.Fatalf("SetQuarantined(true): %v", err)
	}
	entry, _ = st.DB().GetEntry(id)
	if !entry.Quarantined {
		t.Error("entry should be quarantined after SetQuarantined(true)")
	}
	if entry.QuarantinedAt == "" {
		t.Error("quarantined_at should be set after quarantining")
	}

	// Unquarantine it
	if err := st.DB().SetQuarantined(id, false); err != nil {
		t.Fatalf("SetQuarantined(false): %v", err)
	}
	entry, _ = st.DB().GetEntry(id)
	if entry.Quarantined {
		t.Error("entry should not be quarantined after SetQuarantined(false)")
	}
	if entry.QuarantinedAt != "" {
		t.Error("quarantined_at should be cleared after unquarantining")
	}
}

func TestListQuarantined(t *testing.T) {
	st := setupTestStore(t)
	id1 := insertTestEntry(t, st, "quarantined 1")
	id2 := insertTestEntry(t, st, "quarantined 2")
	_ = insertTestEntry(t, st, "not quarantined")

	st.DB().SetQuarantined(id1, true)
	st.DB().SetQuarantined(id2, true)

	entries, err := st.DB().ListQuarantined()
	if err != nil {
		t.Fatalf("ListQuarantined: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListQuarantined returned %d entries, want 2", len(entries))
	}

	// Should be sorted by quarantined_at DESC (most recent first)
	ids := map[string]bool{entries[0].ID: true, entries[1].ID: true}
	if !ids[id1] || !ids[id2] {
		t.Error("ListQuarantined should return both quarantined entries")
	}
}

func TestListQuarantinedEmpty(t *testing.T) {
	st := setupTestStore(t)
	_ = insertTestEntry(t, st, "not quarantined")

	entries, err := st.DB().ListQuarantined()
	if err != nil {
		t.Fatalf("ListQuarantined: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ListQuarantined returned %d entries, want 0", len(entries))
	}
}

func TestUnquarantine(t *testing.T) {
	st := setupTestStore(t)
	id := insertTestEntry(t, st, "will unquarantine")

	// Set up quarantine state
	st.DB().SetQuarantined(id, true)
	st.DB().IncrementFailureCount(id, "test failure reason")   //nolint:errcheck
	st.DB().IncrementFailureCount(id, "test failure reason 2") //nolint:errcheck

	// Verify preconditions
	entry, _ := st.DB().GetEntry(id)
	if !entry.Quarantined {
		t.Fatal("entry should be quarantined before Unquarantine")
	}
	if entry.FailureCount != 2 {
		t.Fatalf("failure_count should be 2, got %d", entry.FailureCount)
	}

	// Unquarantine via steward
	s := New(st, DefaultConfig())
	if err := s.Unquarantine(id, "here's some guidance"); err != nil {
		t.Fatalf("Unquarantine: %v", err)
	}

	// Verify post-conditions
	entry, _ = st.DB().GetEntry(id)
	if entry.Quarantined {
		t.Error("entry should not be quarantined after Unquarantine")
	}
	if entry.FailureCount != 0 {
		t.Errorf("failure_count should be 0 after Unquarantine, got %d", entry.FailureCount)
	}
}

func TestUnquarantineNoFeedback(t *testing.T) {
	st := setupTestStore(t)
	id := insertTestEntry(t, st, "no feedback test")

	st.DB().SetQuarantined(id, true)

	s := New(st, DefaultConfig())
	if err := s.Unquarantine(id, ""); err != nil {
		t.Fatalf("Unquarantine with empty feedback: %v", err)
	}

	entry, _ := st.DB().GetEntry(id)
	if entry.Quarantined {
		t.Error("entry should not be quarantined after Unquarantine")
	}
}

func TestUnquarantineRecordsAction(t *testing.T) {
	st := setupTestStore(t)
	id := insertTestEntry(t, st, "action record test")
	st.DB().SetQuarantined(id, true)

	s := New(st, DefaultConfig())
	s.Unquarantine(id, "try this approach instead")

	// Check that the action was recorded via Status()
	status := s.Status()
	found := false
	for _, a := range status.RecentActions {
		if a.EntryID == id && a.ActionType == "unquarantine" {
			found = true
			if a.Notes != "try this approach instead" {
				t.Errorf("action notes = %q, want 'try this approach instead'", a.Notes)
			}
		}
	}
	if !found {
		t.Error("Unquarantine should record an 'unquarantine' action")
	}
}

func TestQuarantineSetsFlag(t *testing.T) {
	st := setupTestStore(t)
	id := insertTestEntry(t, st, "quarantine flag test")

	entry, _ := st.DB().GetEntry(id)
	entry.FailureCount = 3

	s := New(st, DefaultConfig())
	s.quarantine(entry, FailureUnknown, "test reason")

	// Verify the quarantine flag was set
	updated, _ := st.DB().GetEntry(id)
	if !updated.Quarantined {
		t.Error("quarantine() should set the quarantined flag")
	}
	if updated.QuarantinedAt == "" {
		t.Error("quarantine() should set quarantined_at timestamp")
	}
}

func TestQuarantineCostLimitSetsFlag(t *testing.T) {
	st := setupTestStore(t)
	id := insertTestEntry(t, st, "cost limit flag test")

	entry, _ := st.DB().GetEntry(id)
	entry.FailureCount = 1
	entry.PremiumRequestsUsed = 15.0

	s := New(st, DefaultConfig())
	s.quarantineCostLimit(entry, FailureModelLimit, "budget blown")

	// Verify the quarantine flag was set
	updated, _ := st.DB().GetEntry(id)
	if !updated.Quarantined {
		t.Error("quarantineCostLimit() should set the quarantined flag")
	}
}
