package ai

import "context"

// Completer is the interface that AI backends must implement for the classifier.
// Both the Copilot SDK client and the LM Studio client satisfy this.
type Completer interface {
	// CompleteJSON sends a chat-style request and returns a validated JSON response.
	// It strips markdown fences and retries once if the model returns non-JSON.
	CompleteJSON(ctx context.Context, messages []ChatMessage, temperature float64) ([]byte, error)

	// Model returns the current model identifier.
	Model() string

	// SetModel hot-swaps the active model.
	SetModel(model string)
}
