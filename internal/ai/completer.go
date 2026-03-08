package ai

import "context"

// Completer is the interface that AI backends must implement for the classifier.
// Both the Copilot SDK client and the LM Studio client satisfy this.
type Completer interface {
	// CompleteJSON sends a chat-style request and returns a validated JSON response.
	// It strips markdown fences and retries once if the model returns non-JSON.
	CompleteJSON(ctx context.Context, messages []ChatMessage, temperature float64) ([]byte, error)

	// CompleteStructuredJSON sends a request with a response_format JSON schema,
	// using grammar-based sampling to guarantee valid JSON output.
	// Falls back to CompleteJSON if not supported.
	CompleteStructuredJSON(ctx context.Context, messages []ChatMessage, temperature float64, schema map[string]any) ([]byte, error)

	// Model returns the current model identifier.
	Model() string

	// SetModel hot-swaps the active model.
	SetModel(model string)
}
