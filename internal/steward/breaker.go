package steward

import (
	"fmt"
	"sync"
	"time"
)

// BreakerState represents the circuit breaker's current state.
type BreakerState string

const (
	BreakerClosed   BreakerState = "closed"    // Normal operation — requests pass through
	BreakerOpen     BreakerState = "open"       // Stage is broken — requests are rejected
	BreakerHalfOpen BreakerState = "half_open"  // Cooldown expired — allow one probe request
)

// BreakerConfig holds circuit breaker tuning parameters.
type BreakerConfig struct {
	FailureThreshold int           // consecutive failures across entries before opening (default 5)
	Cooldown         time.Duration // how long to stay open before probing (default 10m)
}

// DefaultBreakerConfig returns conservative defaults.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold: 5,
		Cooldown:         10 * time.Minute,
	}
}

// StageBreaker tracks circuit state for a single pipeline stage.
type StageBreaker struct {
	State            BreakerState `json:"state"`
	ConsecutiveFails int          `json:"consecutive_failures"`
	LastFailureAt    time.Time    `json:"last_failure_at,omitempty"`
	OpenedAt         time.Time    `json:"opened_at,omitempty"`
	LastProbeAt      time.Time    `json:"last_probe_at,omitempty"`
	TotalTrips       int          `json:"total_trips"` // how many times this breaker has opened
}

// CircuitBreaker manages per-stage circuit breakers.
// D&C 101:47-54 — if the watchman keeps failing, stop sending entries
// until the stage recovers.
type CircuitBreaker struct {
	mu       sync.Mutex
	stages   map[string]*StageBreaker
	cfg      BreakerConfig
	nowFunc  func() time.Time // injectable for testing
}

// NewCircuitBreaker creates a circuit breaker with the given config.
func NewCircuitBreaker(cfg BreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		stages:  make(map[string]*StageBreaker),
		cfg:     cfg,
		nowFunc: time.Now,
	}
}

// Allow checks whether a request to the given stage should proceed.
// Returns true if the stage is closed or half-open (probe allowed).
// Returns false if the stage is open (still in cooldown).
//
// When transitioning from open → half-open, Allow returns true once
// (the probe). If that probe fails, the breaker re-opens.
func (cb *CircuitBreaker) Allow(stage string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	sb := cb.stages[stage]
	if sb == nil {
		return true // no breaker state → closed by default
	}

	switch sb.State {
	case BreakerClosed:
		return true

	case BreakerOpen:
		// Check if cooldown has expired
		if cb.nowFunc().After(sb.OpenedAt.Add(cb.cfg.Cooldown)) {
			sb.State = BreakerHalfOpen
			sb.LastProbeAt = cb.nowFunc()
			return true // allow one probe
		}
		return false // still cooling down

	case BreakerHalfOpen:
		// Only one probe at a time — reject until RecordSuccess or RecordFailure
		return false

	default:
		return true
	}
}

// RecordFailure records a failure for a stage. If consecutive failures
// reach the threshold, the breaker opens.
func (cb *CircuitBreaker) RecordFailure(stage string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	sb := cb.getOrCreate(stage)
	sb.ConsecutiveFails++
	sb.LastFailureAt = cb.nowFunc()

	switch sb.State {
	case BreakerClosed:
		if sb.ConsecutiveFails >= cb.cfg.FailureThreshold {
			sb.State = BreakerOpen
			sb.OpenedAt = cb.nowFunc()
			sb.TotalTrips++
		}

	case BreakerHalfOpen:
		// Probe failed — re-open
		sb.State = BreakerOpen
		sb.OpenedAt = cb.nowFunc()
		sb.TotalTrips++
	}
}

// RecordSuccess records a success for a stage. Resets the consecutive
// failure counter and closes the breaker if it was half-open.
func (cb *CircuitBreaker) RecordSuccess(stage string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	sb := cb.getOrCreate(stage)
	sb.ConsecutiveFails = 0

	if sb.State == BreakerHalfOpen {
		sb.State = BreakerClosed
	}
}

// StageStatus returns a snapshot of a single stage's breaker state.
// Returns nil if no breaker has been created for the stage.
func (cb *CircuitBreaker) StageStatus(stage string) *StageBreaker {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	sb := cb.stages[stage]
	if sb == nil {
		return nil
	}
	// Return a copy
	copy := *sb
	return &copy
}

// AllStatus returns a snapshot of all stage breaker states.
func (cb *CircuitBreaker) AllStatus() map[string]StageBreaker {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	result := make(map[string]StageBreaker, len(cb.stages))
	for stage, sb := range cb.stages {
		result[stage] = *sb
	}
	return result
}

// Reset forces a breaker back to closed state. Used when an admin
// wants to manually clear a tripped breaker.
func (cb *CircuitBreaker) Reset(stage string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	sb := cb.stages[stage]
	if sb != nil {
		sb.State = BreakerClosed
		sb.ConsecutiveFails = 0
	}
}

// OpenStages returns the names of all currently open (or half-open) stages.
func (cb *CircuitBreaker) OpenStages() []string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	var result []string
	for stage, sb := range cb.stages {
		if sb.State == BreakerOpen || sb.State == BreakerHalfOpen {
			result = append(result, stage)
		}
	}
	return result
}

// FormatBlockedMessage returns a human-readable explanation of why
// a stage is blocked and when it will be probed.
func (cb *CircuitBreaker) FormatBlockedMessage(stage string) string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	sb := cb.stages[stage]
	if sb == nil || sb.State == BreakerClosed {
		return ""
	}

	remaining := sb.OpenedAt.Add(cb.cfg.Cooldown).Sub(cb.nowFunc())
	if remaining < 0 {
		remaining = 0
	}

	return fmt.Sprintf("⚡ **Circuit breaker OPEN** for %s — %d consecutive failures across entries. "+
		"Will probe in %s.\n\nThe steward is protecting against wasting tokens on a systematically broken stage.",
		stage, sb.ConsecutiveFails, remaining.Round(time.Second))
}

// getOrCreate returns the breaker for a stage, creating it if needed.
// Caller must hold cb.mu.
func (cb *CircuitBreaker) getOrCreate(stage string) *StageBreaker {
	sb := cb.stages[stage]
	if sb == nil {
		sb = &StageBreaker{State: BreakerClosed}
		cb.stages[stage] = sb
	}
	return sb
}
