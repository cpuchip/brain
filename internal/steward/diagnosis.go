package steward

import "strings"

// FailureType classifies what went wrong.
type FailureType string

const (
	FailureTransient  FailureType = "transient"   // network, rate limit, API error
	FailureTimeout    FailureType = "timeout"      // inactivity or wall-clock timeout
	FailureModelLimit FailureType = "model_limit"  // model can't handle the task
	FailureToolError  FailureType = "tool_error"   // MCP tool failure
	FailureUnknown    FailureType = "unknown"       // needs human review
)

// Diagnose classifies a failure based on the error message and attempt count.
// Phase 1: simple pattern matching. Phase 2 will add LLM-assisted diagnosis.
func Diagnose(reason string, failureCount int) FailureType {
	lower := strings.ToLower(reason)

	// Timeout patterns
	if containsAny(lower, "timeout", "timed out", "context deadline exceeded", "context canceled", "inactivity") {
		return FailureTimeout
	}

	// Transient / rate-limit patterns
	if containsAny(lower, "429", "rate limit", "too many requests",
		"500", "502", "503", "service unavailable", "internal server error",
		"connection refused", "connection reset", "econnreset", "eof", "broken pipe",
		"temporary failure", "network") {
		return FailureTransient
	}

	// Tool errors
	if containsAny(lower, "tool call", "mcp", "tool error", "tool failed") {
		return FailureToolError
	}

	// Model capability issues — same error repeated suggests the model can't do it
	if failureCount >= 2 {
		return FailureModelLimit
	}

	return FailureUnknown
}

// containsAny returns true if s contains any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
