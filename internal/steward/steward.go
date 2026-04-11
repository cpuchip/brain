// Package steward implements the Watch→Diagnose→Act→Account loop for the
// brain pipeline. Phase 1: automatic retry-with-context after failures.
//
// Scriptural frame: the steward watches for failures (D&C 101 tower),
// diagnoses them (Ezek 34 shepherd seeking the lost), acts proportionally
// (Jacob 5 pruning), and renders account (D&C 72 stewardship).
package steward

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/cpuchip/brain/internal/store"
)

// Notifier pushes events to the WebSocket hub.
type Notifier interface {
	Notify(eventType, entryID string, data any)
}

// PipelineRetrier can re-run pipeline stages for entries.
type PipelineRetrier interface {
	// RetryAdvance re-runs the current pipeline stage for an entry with
	// the given feedback injected into the prompt as context.
	RetryAdvance(ctx context.Context, entryID, feedback string) error

	// RetryExecute re-runs execution for a specced entry with feedback context.
	RetryExecute(ctx context.Context, entryID, feedback string) error
}

// Config holds steward tuning parameters.
type Config struct {
	MaxRetries      int           // per-entry retries before quarantine (default 2)
	BackoffBase     time.Duration // base delay before first retry (default 30s)
	BackoffMax      time.Duration // maximum delay between retries (default 5m)
	QuarantineAfter int           // total attempts before dead-letter (default 3)
	Enabled         bool          // master switch
}

// DefaultConfig returns conservative Phase 1 defaults.
func DefaultConfig() Config {
	return Config{
		MaxRetries:      2,
		BackoffBase:     30 * time.Second,
		BackoffMax:      5 * time.Minute,
		QuarantineAfter: 3,
		Enabled:         true,
	}
}

// Action records what the steward decided to do.
type Action struct {
	EntryID    string      `json:"entry_id"`
	Timestamp  time.Time   `json:"timestamp"`
	ActionType string      `json:"action_type"` // "retry", "quarantine", "backoff_wait"
	Diagnosis  FailureType `json:"diagnosis"`
	Attempt    int         `json:"attempt"`
	Notes      string      `json:"notes"`
}

// Status is the observable state of the steward for the API.
type Status struct {
	Enabled       bool      `json:"enabled"`
	TotalRetries  int       `json:"total_retries"`
	TotalQuarant  int       `json:"total_quarantines"`
	LastActionAt  time.Time `json:"last_action_at,omitempty"`
	RecentActions []Action  `json:"recent_actions,omitempty"`
}

// Steward watches for pipeline failures and orchestrates retries.
type Steward struct {
	store    *store.Store
	retrier  PipelineRetrier
	notifier Notifier
	cfg      Config
	ctx      context.Context
	cancel   context.CancelFunc

	mu            sync.Mutex
	totalRetries  int
	totalQuarant  int
	lastActionAt  time.Time
	recentActions []Action // ring buffer, last 20
}

// New creates a steward. Call SetNotifier and SetRetrier before use.
func New(st *store.Store, cfg Config) *Steward {
	ctx, cancel := context.WithCancel(context.Background())
	return &Steward{
		store:  st,
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}
}

// SetNotifier configures the WebSocket push sink.
func (s *Steward) SetNotifier(n Notifier) {
	s.notifier = n
}

// SetRetrier configures the pipeline retry interface.
func (s *Steward) SetRetrier(r PipelineRetrier) {
	s.retrier = r
}

// Stop cancels the steward's context.
func (s *Steward) Stop() {
	s.cancel()
}

// SetEnabled toggles the steward's enabled state at runtime.
func (s *Steward) SetEnabled(enabled bool) {
	s.mu.Lock()
	s.cfg.Enabled = enabled
	s.mu.Unlock()
}

// Status returns the current observable state.
func (s *Steward) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Enabled:       s.cfg.Enabled,
		TotalRetries:  s.totalRetries,
		TotalQuarant:  s.totalQuarant,
		LastActionAt:  s.lastActionAt,
		RecentActions: append([]Action(nil), s.recentActions...),
	}
}

// OnFailure is called by the pipeline when any stage fails.
// It runs the Watch→Diagnose→Act→Account cycle asynchronously.
// This is the main entry point — the pipeline calls this instead of
// (or in addition to) recordFailure.
func (s *Steward) OnFailure(entryID, stage string, failErr error) {
	if !s.cfg.Enabled || s.retrier == nil {
		return
	}
	go s.handleFailure(entryID, stage, failErr)
}

// handleFailure is the async Watch→Diagnose→Act→Account cycle.
func (s *Steward) handleFailure(entryID, stage string, failErr error) {
	// Watch: read entry state
	entry, err := s.store.DB().GetEntry(entryID)
	if err != nil {
		log.Printf("steward: cannot read entry %s: %v", entryID, err)
		return
	}

	failureCount := entry.FailureCount
	failureReason := failErr.Error()

	// Diagnose: classify the failure
	diagnosis := Diagnose(failureReason, failureCount)

	// Act: decide what to do
	if failureCount >= s.cfg.QuarantineAfter || diagnosis == FailureUnknown {
		s.quarantine(entry, diagnosis, failureReason)
		return
	}

	// Calculate backoff delay
	delay := s.backoff(failureCount)

	// Log the wait
	s.recordAction(Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: "backoff_wait",
		Diagnosis:  diagnosis,
		Attempt:    failureCount,
		Notes:      fmt.Sprintf("Waiting %s before retry (attempt %d/%d)", delay, failureCount, s.cfg.QuarantineAfter),
	})

	// Wait for backoff (respecting cancellation)
	select {
	case <-time.After(delay):
	case <-s.ctx.Done():
		return
	}

	// Re-read entry to check it hasn't been manually changed during backoff
	entry, err = s.store.DB().GetEntry(entryID)
	if err != nil {
		log.Printf("steward: cannot re-read entry %s after backoff: %v", entryID, err)
		return
	}

	// If the human has taken control (your_turn) or maturity changed, stand down
	if entry.RouteStatus == "your_turn" || entry.RouteStatus == "running" {
		log.Printf("steward: entry %s route_status is %s — human has control, standing down", entryID, entry.RouteStatus)
		return
	}

	// Build retry context from the failure
	retryFeedback := BuildRetryContext(diagnosis, failureReason, failureCount)

	s.retry(entry, stage, diagnosis, retryFeedback)
}

// retry attempts to re-run the failed stage with diagnostic context.
func (s *Steward) retry(entry *store.Entry, stage string, diagnosis FailureType, feedback string) {
	entryID := entry.ID
	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}

	// Post session message about the retry
	msg := fmt.Sprintf("🔄 **Steward:** Retrying %s (diagnosis: %s, attempt %d/%d)\n\nFeedback for agent: %s",
		stage, diagnosis, entry.FailureCount, s.cfg.QuarantineAfter, feedback)
	s.store.DB().AddSessionMessage(entryID, "system", msg)
	s.notify("message.new", entryID, nil)

	action := Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: "retry",
		Diagnosis:  diagnosis,
		Attempt:    entry.FailureCount,
		Notes:      fmt.Sprintf("Retrying %s stage with diagnostic context", stage),
	}

	var retryErr error
	if maturity == "specced" || stage == "execute" {
		retryErr = s.retrier.RetryExecute(s.ctx, entryID, feedback)
	} else {
		retryErr = s.retrier.RetryAdvance(s.ctx, entryID, feedback)
	}

	if retryErr != nil {
		action.Notes += fmt.Sprintf(" — retry dispatch failed: %v", retryErr)
		log.Printf("steward: retry dispatch failed for %s: %v", entryID, retryErr)
	}

	s.recordAction(action)

	s.mu.Lock()
	s.totalRetries++
	s.mu.Unlock()
}

// quarantine marks an entry for human review after exhausting retries.
func (s *Steward) quarantine(entry *store.Entry, diagnosis FailureType, reason string) {
	entryID := entry.ID

	// Set route_status to your_turn so it surfaces for the human
	s.store.DB().UpdateRouteStatus(entryID, "your_turn")

	msg := fmt.Sprintf("🛑 **Steward: Quarantined** — entry has failed %d times.\n\n"+
		"**Last diagnosis:** %s\n"+
		"**Last failure:** %s\n\n"+
		"The steward has exhausted its retry strategies. This entry needs your attention.\n\n"+
		"You can:\n"+
		"- **Revise** with guidance to help the agent\n"+
		"- **Advance** to retry from scratch\n"+
		"- **Reject** to start over\n"+
		"- **Defer** to revisit later",
		entry.FailureCount, diagnosis, reason)

	s.store.DB().AddSessionMessage(entryID, "system", msg)
	s.notify("message.new", entryID, nil)
	s.notify("entry.updated", entryID, map[string]string{"route_status": "your_turn"})

	action := Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: "quarantine",
		Diagnosis:  diagnosis,
		Attempt:    entry.FailureCount,
		Notes:      fmt.Sprintf("Quarantined after %d failures: %s", entry.FailureCount, reason),
	}
	s.recordAction(action)

	s.mu.Lock()
	s.totalQuarant++
	s.mu.Unlock()

	log.Printf("steward: quarantined entry %s after %d failures (diagnosis: %s)", entryID, entry.FailureCount, diagnosis)
}

// backoff calculates exponential backoff with jitter.
func (s *Steward) backoff(attempt int) time.Duration {
	base := s.cfg.BackoffBase
	delay := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
	if delay > s.cfg.BackoffMax {
		delay = s.cfg.BackoffMax
	}
	return delay
}

// recordAction logs an action and maintains the recent actions ring buffer.
func (s *Steward) recordAction(a Action) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActionAt = a.Timestamp
	s.recentActions = append(s.recentActions, a)
	if len(s.recentActions) > 20 {
		s.recentActions = s.recentActions[len(s.recentActions)-20:]
	}
	log.Printf("steward: [%s] %s entry=%s attempt=%d — %s", a.ActionType, a.Diagnosis, a.EntryID, a.Attempt, a.Notes)
}

// notify is a helper to push WebSocket events if a notifier is attached.
func (s *Steward) notify(eventType, entryID string, data any) {
	if s.notifier != nil {
		s.notifier.Notify(eventType, entryID, data)
	}
}
