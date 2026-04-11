package steward

import (
	"sync"
	"testing"
	"time"

	"github.com/cpuchip/brain/internal/store"
)

// mockNudger records calls to NudgeEntry for test assertions.
type mockNudger struct {
	mu      sync.Mutex
	nudged  []string // entry IDs that were nudged
	failIDs map[string]bool
}

func newMockNudger() *mockNudger {
	return &mockNudger{failIDs: map[string]bool{}}
}

func (m *mockNudger) NudgeEntry(entry *store.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failIDs[entry.ID] {
		return &nudgeError{id: entry.ID}
	}
	m.nudged = append(m.nudged, entry.ID)
	return nil
}

func (m *mockNudger) nudgedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.nudged)
}

type nudgeError struct{ id string }

func (e *nudgeError) Error() string { return "nudge failed for " + e.id }

// --- Tests ---

func TestNextWakeTime(t *testing.T) {
	// 10:30 AM, hours = [7, 11, 15, 19]
	now := time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local)
	hours := []int{7, 11, 15, 19}

	next := nextWakeTime(now, hours)
	if next.Hour() != 11 {
		t.Errorf("next wake hour = %d, want 11", next.Hour())
	}
	if !next.After(now) {
		t.Error("next wake time should be after now")
	}
}

func TestNextWakeTimeWrapsToTomorrow(t *testing.T) {
	// 20:00, all hours have passed
	now := time.Date(2026, 4, 11, 20, 0, 0, 0, time.Local)
	hours := []int{7, 11, 15, 19}

	next := nextWakeTime(now, hours)
	if next.Day() != 12 {
		t.Errorf("next wake day = %d, want 12 (tomorrow)", next.Day())
	}
	if next.Hour() != 7 {
		t.Errorf("next wake hour = %d, want 7 (first hour)", next.Hour())
	}
}

func TestDefaultNudgeConfig(t *testing.T) {
	cfg := DefaultNudgeConfig()
	if !cfg.Enabled {
		t.Error("nudge should be enabled by default")
	}
	if len(cfg.WakeHours) != 4 {
		t.Errorf("wake hours = %d, want 4", len(cfg.WakeHours))
	}
	if cfg.RawStaleAfter != 24*time.Hour {
		t.Errorf("RawStaleAfter = %v, want 24h", cfg.RawStaleAfter)
	}
	if cfg.PresenceTimeout != 2*time.Hour {
		t.Errorf("PresenceTimeout = %v, want 2h", cfg.PresenceTimeout)
	}
}

func TestGetNudgeStatusDefaults(t *testing.T) {
	s := New(nil, DefaultConfig())
	status := s.GetNudgeStatus()

	if status.Enabled {
		t.Error("nudge should not be enabled before StartWatchLoop")
	}
	if status.TotalNudges != 0 {
		t.Errorf("initial TotalNudges = %d, want 0", status.TotalNudges)
	}
}

func TestSetNudgePaused(t *testing.T) {
	s := New(nil, DefaultConfig())

	s.SetNudgePaused(true)
	status := s.GetNudgeStatus()
	if !status.Paused {
		t.Error("nudge bot should be paused after SetNudgePaused(true)")
	}

	s.SetNudgePaused(false)
	status = s.GetNudgeStatus()
	if status.Paused {
		t.Error("nudge bot should not be paused after SetNudgePaused(false)")
	}
}

func TestTouchActivity(t *testing.T) {
	s := New(nil, DefaultConfig())
	s.nudgeCfg = DefaultNudgeConfig()

	// Before touching, user is not present (lastActivityAt is zero)
	status := s.GetNudgeStatus()
	if status.UserPresent {
		t.Error("user should not be present before TouchActivity")
	}

	s.TouchActivity()
	status = s.GetNudgeStatus()
	if !status.UserPresent {
		t.Error("user should be present after TouchActivity")
	}
}

func TestRunWatchScanNudgesStaleEntries(t *testing.T) {
	st := setupTestStore(t)

	// Create a stale raw entry (old created_at, category=ideas so it matches ListStaleEntries)
	entry := &store.Entry{
		Title:    "stale raw entry",
		Category: "ideas",
		Created:  time.Now().UTC().Add(-48 * time.Hour),
		Updated:  time.Now().UTC().Add(-48 * time.Hour),
	}
	id, err := st.DB().InsertEntry(entry)
	if err != nil {
		t.Fatalf("InsertEntry: %v", err)
	}
	_ = id

	nudger := newMockNudger()
	s := New(st, DefaultConfig())
	s.nudger = nudger
	s.nudgeCfg = DefaultNudgeConfig()

	// Set user as present
	s.TouchActivity()

	// Run the scan directly
	s.runWatchScan()

	if nudger.nudgedCount() != 1 {
		t.Errorf("nudged count = %d, want 1", nudger.nudgedCount())
	}
}

func TestRunWatchScanSkipsWhenPaused(t *testing.T) {
	st := setupTestStore(t)
	entry := &store.Entry{Title: "stale but paused", Category: "ideas", Created: time.Now().UTC().Add(-48 * time.Hour), Updated: time.Now().UTC().Add(-48 * time.Hour)}
	st.DB().InsertEntry(entry)

	nudger := newMockNudger()
	s := New(st, DefaultConfig())
	s.nudger = nudger
	s.nudgeCfg = DefaultNudgeConfig()
	s.TouchActivity()
	s.SetNudgePaused(true)

	s.runWatchScan()

	if nudger.nudgedCount() != 0 {
		t.Errorf("nudged count = %d, want 0 (paused)", nudger.nudgedCount())
	}
}

func TestRunWatchScanSkipsWhenNoPresence(t *testing.T) {
	st := setupTestStore(t)
	entry := &store.Entry{Title: "stale no presence", Category: "ideas", Created: time.Now().UTC().Add(-48 * time.Hour), Updated: time.Now().UTC().Add(-48 * time.Hour)}
	st.DB().InsertEntry(entry)

	nudger := newMockNudger()
	s := New(st, DefaultConfig())
	s.nudger = nudger
	s.nudgeCfg = DefaultNudgeConfig()
	// Don't call TouchActivity — user not present

	s.runWatchScan()

	if nudger.nudgedCount() != 0 {
		t.Errorf("nudged count = %d, want 0 (no presence)", nudger.nudgedCount())
	}
}

func TestStartWatchLoopInitializesState(t *testing.T) {
	st := setupTestStore(t)
	nudger := newMockNudger()
	s := New(st, DefaultConfig())
	s.nudger = nudger

	cfg := DefaultNudgeConfig()
	s.StartWatchLoop(cfg)
	defer s.Stop()

	status := s.GetNudgeStatus()
	if !status.Enabled {
		t.Error("nudge should be enabled after StartWatchLoop")
	}
	if len(status.WakeHours) != 4 {
		t.Errorf("wake hours = %d, want 4", len(status.WakeHours))
	}
	if status.NextRunAt.IsZero() {
		t.Error("next run at should be set after StartWatchLoop")
	}
}

func TestStartWatchLoopDisabledWithoutNudger(t *testing.T) {
	s := New(nil, DefaultConfig())
	// No nudger set — should not panic
	s.StartWatchLoop(DefaultNudgeConfig())
	defer s.Stop()

	status := s.GetNudgeStatus()
	if status.Enabled {
		t.Error("nudge should not be enabled without nudger")
	}
}

func TestStatusIncludesNudgeBot(t *testing.T) {
	s := New(nil, DefaultConfig())
	s.nudgeCfg = DefaultNudgeConfig()
	s.TouchActivity()

	status := s.Status()
	if status.NudgeBot.UserPresent != true {
		t.Error("Status().NudgeBot.UserPresent should reflect TouchActivity")
	}
}

func TestWatchScanRecordsAction(t *testing.T) {
	st := setupTestStore(t)
	nudger := newMockNudger()
	s := New(st, DefaultConfig())
	s.nudger = nudger
	s.nudgeCfg = DefaultNudgeConfig()
	s.TouchActivity()

	s.runWatchScan()

	status := s.Status()
	found := false
	for _, a := range status.RecentActions {
		if a.ActionType == "watch_scan" {
			found = true
		}
	}
	if !found {
		t.Error("runWatchScan should record a 'watch_scan' action")
	}
}
