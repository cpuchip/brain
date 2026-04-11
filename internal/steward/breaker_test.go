package steward

import (
	"testing"
	"time"
)

func TestBreakerStartsClosed(t *testing.T) {
	cb := NewCircuitBreaker(DefaultBreakerConfig())
	if !cb.Allow("research") {
		t.Error("new breaker should allow requests (closed)")
	}
}

func TestBreakerOpensAfterThreshold(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 3, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	// Failures 1 and 2: still closed
	cb.RecordFailure("research")
	cb.RecordFailure("research")
	if !cb.Allow("research") {
		t.Error("breaker should still be closed after 2 failures (threshold=3)")
	}

	// Failure 3: opens
	cb.RecordFailure("research")
	if cb.Allow("research") {
		t.Error("breaker should be open after 3 failures")
	}

	sb := cb.StageStatus("research")
	if sb == nil {
		t.Fatal("expected stage status")
	}
	if sb.State != BreakerOpen {
		t.Errorf("state = %s, want open", sb.State)
	}
	if sb.TotalTrips != 1 {
		t.Errorf("total trips = %d, want 1", sb.TotalTrips)
	}
}

func TestBreakerCooldownToHalfOpen(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 2, Cooldown: 10 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Trip the breaker
	cb.RecordFailure("execute")
	cb.RecordFailure("execute")
	if cb.Allow("execute") {
		t.Error("breaker should be open")
	}

	// Advance time past cooldown
	cb.nowFunc = func() time.Time { return now.Add(11 * time.Minute) }
	if !cb.Allow("execute") {
		t.Error("breaker should transition to half-open after cooldown and allow a probe")
	}

	sb := cb.StageStatus("execute")
	if sb.State != BreakerHalfOpen {
		t.Errorf("state = %s, want half_open", sb.State)
	}

	// Second call while half-open should be rejected (only one probe)
	if cb.Allow("execute") {
		t.Error("half-open breaker should only allow one probe")
	}
}

func TestBreakerHalfOpenProbeSuccess(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 2, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Trip it
	cb.RecordFailure("plan")
	cb.RecordFailure("plan")

	// Advance past cooldown → half-open
	cb.nowFunc = func() time.Time { return now.Add(6 * time.Minute) }
	cb.Allow("plan") // transitions to half-open

	// Probe succeeds → should close
	cb.RecordSuccess("plan")
	sb := cb.StageStatus("plan")
	if sb.State != BreakerClosed {
		t.Errorf("state = %s, want closed after successful probe", sb.State)
	}
	if sb.ConsecutiveFails != 0 {
		t.Errorf("consecutive failures = %d, want 0 after success", sb.ConsecutiveFails)
	}
}

func TestBreakerHalfOpenProbeFails(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 2, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	// Trip it
	cb.RecordFailure("research")
	cb.RecordFailure("research")

	// Advance past cooldown → half-open
	cb.nowFunc = func() time.Time { return now.Add(6 * time.Minute) }
	cb.Allow("research") // transitions to half-open

	// Probe fails → should re-open
	cb.RecordFailure("research")
	sb := cb.StageStatus("research")
	if sb.State != BreakerOpen {
		t.Errorf("state = %s, want open after failed probe", sb.State)
	}
	if sb.TotalTrips != 2 {
		t.Errorf("total trips = %d, want 2 (initial + re-open)", sb.TotalTrips)
	}
}

func TestBreakerSuccessResetsClosed(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 5, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	cb.RecordFailure("execute")
	cb.RecordFailure("execute")
	cb.RecordFailure("execute")

	sb := cb.StageStatus("execute")
	if sb.ConsecutiveFails != 3 {
		t.Fatalf("consecutive failures = %d, want 3", sb.ConsecutiveFails)
	}

	// Success resets the counter
	cb.RecordSuccess("execute")
	sb = cb.StageStatus("execute")
	if sb.ConsecutiveFails != 0 {
		t.Errorf("consecutive failures = %d, want 0 after success", sb.ConsecutiveFails)
	}
	if sb.State != BreakerClosed {
		t.Errorf("state = %s, want closed", sb.State)
	}
}

func TestBreakerIndependentStages(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 2, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	// Trip research
	cb.RecordFailure("research")
	cb.RecordFailure("research")

	// Execute should still be open
	if !cb.Allow("execute") {
		t.Error("execute breaker should be closed — independent of research")
	}
	if cb.Allow("research") {
		t.Error("research breaker should be open")
	}
}

func TestBreakerReset(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 2, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	cb.RecordFailure("research")
	cb.RecordFailure("research")
	if cb.Allow("research") {
		t.Fatal("should be open")
	}

	cb.Reset("research")
	if !cb.Allow("research") {
		t.Error("should be closed after manual reset")
	}
	sb := cb.StageStatus("research")
	if sb.ConsecutiveFails != 0 {
		t.Errorf("consecutive failures = %d, want 0 after reset", sb.ConsecutiveFails)
	}
}

func TestBreakerOpenStages(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 1, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	cb.RecordFailure("research")
	cb.RecordFailure("execute")

	open := cb.OpenStages()
	if len(open) != 2 {
		t.Errorf("open stages = %v, want 2 stages", open)
	}
}

func TestBreakerAllStatus(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 2, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	cb.RecordFailure("research")
	cb.RecordFailure("execute")

	all := cb.AllStatus()
	if len(all) != 2 {
		t.Errorf("AllStatus length = %d, want 2", len(all))
	}
	if all["research"].State != BreakerClosed {
		t.Errorf("research state = %s, want closed (only 1 failure, threshold 2)", all["research"].State)
	}
}

func TestBreakerFormatBlockedMessage(t *testing.T) {
	cfg := BreakerConfig{FailureThreshold: 1, Cooldown: 5 * time.Minute}
	cb := NewCircuitBreaker(cfg)

	// Closed → no message
	msg := cb.FormatBlockedMessage("research")
	if msg != "" {
		t.Errorf("closed breaker should return empty message, got %q", msg)
	}

	cb.RecordFailure("research")
	msg = cb.FormatBlockedMessage("research")
	if msg == "" {
		t.Error("open breaker should return a non-empty message")
	}
}

func TestBreakerStageStatusNil(t *testing.T) {
	cb := NewCircuitBreaker(DefaultBreakerConfig())
	if cb.StageStatus("nonexistent") != nil {
		t.Error("should return nil for unknown stage")
	}
}

func TestDefaultBreakerConfig(t *testing.T) {
	cfg := DefaultBreakerConfig()
	if cfg.FailureThreshold != 5 {
		t.Errorf("FailureThreshold = %d, want 5", cfg.FailureThreshold)
	}
	if cfg.Cooldown != 10*time.Minute {
		t.Errorf("Cooldown = %v, want 10m", cfg.Cooldown)
	}
}
