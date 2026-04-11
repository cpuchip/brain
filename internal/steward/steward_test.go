package steward

import (
	"fmt"
	"strings"
	"testing"
)

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
		{1, 30, 30},   // base * 2^0 = 30s
		{2, 60, 60},   // base * 2^1 = 60s
		{3, 120, 120}, // base * 2^2 = 120s
		{4, 240, 240}, // base * 2^3 = 240s
		{5, 300, 300}, // base * 2^4 = 480s but capped at 300s
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
