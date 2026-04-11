// Package steward implements the Watch→Diagnose→Act→Account loop for the
// brain pipeline. Phase 1: retry-with-context. Phase 2: model escalation.
// Phase 3: per-stage circuit breaker.
//
// Scriptural frame: the steward watches for failures (D&C 101 tower),
// diagnoses them (Ezek 34 shepherd seeking the lost), acts proportionally
// (Jacob 5 pruning), and renders account (D&C 72 stewardship).
// The circuit breaker is the tower sentinel standing down when the enemy
// is too strong — stop wasting resources and wait for reinforcements.
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
	// If model is non-empty, it overrides the default model for the stage.
	RetryAdvance(ctx context.Context, entryID, feedback, model string) error

	// RetryExecute re-runs execution for a specced entry with feedback context.
	// If model is non-empty, it overrides the default model for the stage.
	RetryExecute(ctx context.Context, entryID, feedback, model string) error
}

// ModelTier defines a model with its cost multiplier for escalation.
type ModelTier struct {
	Model string  // e.g. "claude-haiku-4.5"
	Cost  float64 // premium request cost (0.33, 1.0, 3.0)
}

// Config holds steward tuning parameters.
type Config struct {
	MaxRetries      int           // per-entry retries before quarantine (default 2)
	BackoffBase     time.Duration // base delay before first retry (default 30s)
	BackoffMax      time.Duration // maximum delay between retries (default 5m)
	QuarantineAfter int           // total attempts before dead-letter (default 3)
	Enabled         bool          // master switch

	// Phase 2: Model escalation
	EscalationChain []ModelTier // ordered: cheapest → most capable → human
	MaxCostPerEntry float64     // max premium requests before quarantining (default 10.0)

	// Phase 3: Circuit breaker
	BreakerConfig BreakerConfig // per-stage circuit breaker settings
}

// DefaultConfig returns conservative defaults.
func DefaultConfig() Config {
	return Config{
		MaxRetries:      2,
		BackoffBase:     30 * time.Second,
		BackoffMax:      5 * time.Minute,
		QuarantineAfter: 3,
		Enabled:         true,
		EscalationChain: []ModelTier{
			{Model: "claude-haiku-4.5", Cost: 0.33},
			{Model: "claude-sonnet-4.6", Cost: 1.0},
			{Model: "claude-opus-4.6", Cost: 3.0},
			// Beyond this: quarantine (human)
		},
		MaxCostPerEntry: 10.0,
		BreakerConfig:   DefaultBreakerConfig(),
	}
}

// Action records what the steward decided to do.
type Action struct {
	EntryID    string      `json:"entry_id"`
	Timestamp  time.Time   `json:"timestamp"`
	ActionType string      `json:"action_type"` // "retry", "escalate", "quarantine", "backoff_wait", "cost_limit"
	Diagnosis  FailureType `json:"diagnosis"`
	Attempt    int         `json:"attempt"`
	Model      string      `json:"model,omitempty"`      // which model was used
	Escalated  bool        `json:"escalated,omitempty"`   // was this an escalation?
	Notes      string      `json:"notes"`
}

// Status is the observable state of the steward for the API.
type Status struct {
	Enabled          bool                    `json:"enabled"`
	TotalRetries     int                     `json:"total_retries"`
	TotalEscalations int                     `json:"total_escalations"`
	TotalQuarant     int                     `json:"total_quarantines"`
	LastActionAt     time.Time               `json:"last_action_at,omitempty"`
	RecentActions    []Action                `json:"recent_actions,omitempty"`
	MaxCostPerEntry  float64                 `json:"max_cost_per_entry"`
	CircuitBreakers  map[string]StageBreaker `json:"circuit_breakers,omitempty"`
	NudgeBot         NudgeStatus             `json:"nudge_bot"`
	ActiveCommissions int                    `json:"active_commissions"`
}

// Steward watches for pipeline failures and orchestrates retries.
type Steward struct {
	store      *store.Store
	retrier    PipelineRetrier
	notifier   Notifier
	nudger     Nudger
	cfg        Config
	nudgeCfg   NudgeConfig
	breaker    *CircuitBreaker
	nudge      nudgeState
	commission commissionState
	ctx        context.Context
	cancel     context.CancelFunc

	mu               sync.Mutex
	totalRetries     int
	totalEscalations int
	totalQuarant     int
	lastActionAt     time.Time
	recentActions    []Action // ring buffer, last 20
}

// New creates a steward. Call SetNotifier and SetRetrier before use.
func New(st *store.Store, cfg Config) *Steward {
	ctx, cancel := context.WithCancel(context.Background())
	return &Steward{
		store:   st,
		cfg:     cfg,
		breaker: NewCircuitBreaker(cfg.BreakerConfig),
		ctx:     ctx,
		cancel:  cancel,
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

// Breaker returns the circuit breaker for direct status queries.
func (s *Steward) Breaker() *CircuitBreaker {
	return s.breaker
}

// ResetBreaker manually resets a stage's circuit breaker to closed.
func (s *Steward) ResetBreaker(stage string) {
	s.breaker.Reset(stage)
	s.recordAction(Action{
		EntryID:    "",
		Timestamp:  time.Now(),
		ActionType: "breaker_reset",
		Notes:      fmt.Sprintf("Circuit breaker for %s manually reset to closed", stage),
	})
	log.Printf("steward: circuit breaker for %s manually reset", stage)
}

// Status returns the current observable state.
func (s *Steward) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Enabled:           s.cfg.Enabled,
		TotalRetries:      s.totalRetries,
		TotalEscalations:  s.totalEscalations,
		TotalQuarant:      s.totalQuarant,
		LastActionAt:      s.lastActionAt,
		RecentActions:     append([]Action(nil), s.recentActions...),
		MaxCostPerEntry:   s.cfg.MaxCostPerEntry,
		CircuitBreakers:   s.breaker.AllStatus(),
		NudgeBot:          s.GetNudgeStatus(),
		ActiveCommissions: s.activeCommissionCount(),
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

	// Quarantine immediately for unknown failures or exhausted attempts
	if failureCount >= s.cfg.QuarantineAfter || diagnosis == FailureUnknown {
		s.breaker.RecordFailure(stage)
		s.quarantine(entry, diagnosis, failureReason)
		return
	}

	// Circuit breaker: if this stage is open, don't retry — quarantine immediately
	if !s.breaker.Allow(stage) {
		msg := s.breaker.FormatBlockedMessage(stage)
		s.store.DB().AddSessionMessage(entryID, "system", msg)
		s.notify("message.new", entryID, nil)
		s.quarantine(entry, diagnosis, failureReason)
		s.recordAction(Action{
			EntryID:    entryID,
			Timestamp:  time.Now(),
			ActionType: "circuit_open",
			Diagnosis:  diagnosis,
			Attempt:    failureCount,
			Notes:      fmt.Sprintf("Circuit breaker open for %s — skipping retry", stage),
		})
		return
	}

	// Cost guardrail: check premium request budget before spending more
	if s.cfg.MaxCostPerEntry > 0 && entry.PremiumRequestsUsed >= s.cfg.MaxCostPerEntry {
		s.quarantineCostLimit(entry, diagnosis, failureReason)
		return
	}

	// Determine model: escalate if diagnosis suggests model can't handle it,
	// or if we've already retried once with the same tier
	model, escalated := s.pickModel(entry, stage, diagnosis)

	// If pickModel returns "", we've exhausted the escalation chain → quarantine
	if model == "" {
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
		Model:      model,
		Escalated:  escalated,
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

	// Re-check cost after backoff (human may have added cost in the interim)
	if s.cfg.MaxCostPerEntry > 0 && entry.PremiumRequestsUsed >= s.cfg.MaxCostPerEntry {
		s.quarantineCostLimit(entry, diagnosis, failureReason)
		return
	}

	// Build retry context from the failure
	retryFeedback := BuildRetryContext(diagnosis, failureReason, failureCount)

	s.retry(entry, stage, diagnosis, retryFeedback, model, escalated)
}

// retry attempts to re-run the failed stage with diagnostic context and optional model escalation.
func (s *Steward) retry(entry *store.Entry, stage string, diagnosis FailureType, feedback, model string, escalated bool) {
	entryID := entry.ID
	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}

	// Post session message about the retry
	actionWord := "Retrying"
	if escalated {
		actionWord = "Escalating"
	}
	msg := fmt.Sprintf("🔄 **Steward:** %s %s → **%s** (diagnosis: %s, attempt %d/%d)\n\nFeedback for agent: %s",
		actionWord, stage, model, diagnosis, entry.FailureCount, s.cfg.QuarantineAfter, feedback)
	s.store.DB().AddSessionMessage(entryID, "system", msg)
	s.notify("message.new", entryID, nil)

	actionType := "retry"
	if escalated {
		actionType = "escalate"
	}

	action := Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: actionType,
		Diagnosis:  diagnosis,
		Attempt:    entry.FailureCount,
		Model:      model,
		Escalated:  escalated,
		Notes:      fmt.Sprintf("%s %s stage with %s", actionWord, stage, model),
	}

	var retryErr error
	if maturity == "specced" || stage == "execute" {
		retryErr = s.retrier.RetryExecute(s.ctx, entryID, feedback, model)
	} else {
		retryErr = s.retrier.RetryAdvance(s.ctx, entryID, feedback, model)
	}

	if retryErr != nil {
		action.Notes += fmt.Sprintf(" — retry dispatch failed: %v", retryErr)
		log.Printf("steward: retry dispatch failed for %s: %v", entryID, retryErr)
		s.breaker.RecordFailure(stage)
	} else {
		// Dispatch succeeded — the agent is running. Record success on the breaker
		// so half-open probes transition back to closed.
		s.breaker.RecordSuccess(stage)
	}

	s.recordAction(action)

	s.mu.Lock()
	s.totalRetries++
	if escalated {
		s.totalEscalations++
	}
	s.mu.Unlock()
}

// quarantine marks an entry for human review after exhausting retries.
func (s *Steward) quarantine(entry *store.Entry, diagnosis FailureType, reason string) {
	entryID := entry.ID

	// Set route_status to your_turn and quarantined flag
	s.store.DB().UpdateRouteStatus(entryID, "your_turn")
	s.store.DB().SetQuarantined(entryID, true)

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

// quarantineCostLimit quarantines an entry because it has exceeded its premium request budget.
func (s *Steward) quarantineCostLimit(entry *store.Entry, diagnosis FailureType, reason string) {
	entryID := entry.ID

	s.store.DB().UpdateRouteStatus(entryID, "your_turn")
	s.store.DB().SetQuarantined(entryID, true)

	msg := fmt.Sprintf("💰 **Steward: Cost limit reached** — entry has used %.1f premium requests (limit: %.1f).\n\n"+
		"**Last diagnosis:** %s\n"+
		"**Last failure:** %s\n\n"+
		"The steward won't spend more on automatic retries. This entry needs your attention.\n\n"+
		"You can:\n"+
		"- **Advance** to retry manually (bypasses the cost limit)\n"+
		"- **Revise** with guidance\n"+
		"- **Defer** to revisit later",
		entry.PremiumRequestsUsed, s.cfg.MaxCostPerEntry, diagnosis, reason)

	s.store.DB().AddSessionMessage(entryID, "system", msg)
	s.notify("message.new", entryID, nil)
	s.notify("entry.updated", entryID, map[string]string{"route_status": "your_turn"})

	s.recordAction(Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: "cost_limit",
		Diagnosis:  diagnosis,
		Attempt:    entry.FailureCount,
		Notes:      fmt.Sprintf("Cost limit: %.1f/%.1f premium requests used", entry.PremiumRequestsUsed, s.cfg.MaxCostPerEntry),
	})

	s.mu.Lock()
	s.totalQuarant++
	s.mu.Unlock()

	log.Printf("steward: cost-limited entry %s (%.1f/%.1f premium requests)", entryID, entry.PremiumRequestsUsed, s.cfg.MaxCostPerEntry)
}

// Unquarantine clears quarantine on an entry, resets its failure count,
// and optionally posts human feedback as a session message.
func (s *Steward) Unquarantine(entryID, feedback string) error {
	if err := s.store.DB().SetQuarantined(entryID, false); err != nil {
		return fmt.Errorf("clear quarantine: %w", err)
	}
	if err := s.store.DB().ResetFailureCount(entryID); err != nil {
		return fmt.Errorf("reset failure count: %w", err)
	}

	if feedback != "" {
		s.store.DB().AddSessionMessage(entryID, "user", feedback)
	}

	msg := "✅ **Steward: Unquarantined** — failure count reset, ready for processing."
	s.store.DB().AddSessionMessage(entryID, "system", msg)

	s.notify("message.new", entryID, nil)
	s.notify("entry.updated", entryID, map[string]string{"quarantined": "false"})

	s.recordAction(Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: "unquarantine",
		Notes:      feedback,
	})

	log.Printf("steward: unquarantined entry %s", entryID)
	return nil
}

// pickModel determines which model to use for a retry, and whether this
// constitutes an escalation from the stage's default.
//
// Decision logic:
//   - model_limit or timeout on 2nd+ attempt → escalate to next tier
//   - transient → retry with same model (the issue was external)
//   - tool_error on 2nd+ attempt → escalate (might need a smarter model)
//   - otherwise → use stage default
//
// Returns ("", false) if the escalation chain is exhausted (caller should quarantine).
func (s *Steward) pickModel(entry *store.Entry, stage string, diagnosis FailureType) (model string, escalated bool) {
	defaultModel := s.defaultModelForStage(stage, entry.Maturity)
	failureCount := entry.FailureCount

	// Should we escalate?
	shouldEscalate := false
	switch diagnosis {
	case FailureModelLimit:
		// Always escalate for model limit
		shouldEscalate = true
	case FailureTimeout:
		// Escalate on 2nd+ timeout — the model may need more capability
		shouldEscalate = failureCount >= 2
	case FailureToolError:
		// Escalate on 2nd+ tool error — smarter model may handle tools better
		shouldEscalate = failureCount >= 2
	case FailureTransient:
		// Don't escalate for transient — the issue was external
		shouldEscalate = false
	}

	if !shouldEscalate {
		return defaultModel, false
	}

	// Find where the default model sits in the chain and go one tier up
	chain := s.cfg.EscalationChain
	defaultIdx := -1
	for i, tier := range chain {
		if tier.Model == defaultModel {
			defaultIdx = i
			break
		}
	}

	if defaultIdx < 0 {
		// Default model not in chain — try from the top of the chain
		// (This shouldn't happen with correct config, but be safe)
		return defaultModel, false
	}

	nextIdx := defaultIdx + 1
	// For repeated escalation failures, try to go even higher
	// failureCount-1 because the first failure uses the default
	escalationSteps := failureCount - 1
	if escalationSteps > 0 {
		nextIdx = defaultIdx + escalationSteps
	}

	if nextIdx >= len(chain) {
		// Exhausted the chain — signal caller to quarantine (human is next)
		return "", false
	}

	return chain[nextIdx].Model, true
}

// defaultModelForStage returns the default model for a pipeline stage.
func (s *Steward) defaultModelForStage(stage, maturity string) string {
	switch {
	case stage == "execute" || maturity == "specced":
		return "claude-sonnet-4.6" // Execute default
	case maturity == "researched" || maturity == "planned":
		return "claude-opus-4.6" // Plan default
	default:
		return "claude-haiku-4.5" // Research default
	}
}

// backoff calculates exponential backoff.
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
