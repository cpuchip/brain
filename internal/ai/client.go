package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
)

// Client wraps the GitHub Copilot SDK for model inference.
type Client struct {
	copilotClient *copilot.Client
	model         string
	mu            sync.Mutex
	started       bool
}

// NewClient creates a new AI client using the Copilot SDK.
// The githubToken is optional — if empty, the SDK uses the logged-in Copilot CLI user.
func NewClient(model string, githubToken string) *Client {
	opts := &copilot.ClientOptions{
		LogLevel: "error",
	}
	if githubToken != "" {
		opts.GitHubToken = githubToken
	}

	return &Client{
		copilotClient: copilot.NewClient(opts),
		model:         model,
	}
}

// Start initializes the Copilot CLI server process.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return nil
	}

	if err := c.copilotClient.Start(ctx); err != nil {
		return fmt.Errorf("starting Copilot CLI: %w", err)
	}
	c.started = true
	log.Printf("Copilot SDK started (model: %s)", c.model)
	return nil
}

// Stop shuts down the Copilot CLI server process.
func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return nil
	}

	c.started = false
	return c.copilotClient.Stop()
}

// Model returns the current model name.
func (c *Client) Model() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model
}

// SetModel hot-swaps the active model. Takes effect on the next session.
func (c *Client) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
	log.Printf("AI model switched to: %s", model)
}

// ChatMessage is a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Complete sends a prompt through the Copilot SDK and returns the response text.
// The system message should be the first ChatMessage with Role "system".
func (c *Client) Complete(ctx context.Context, messages []ChatMessage, temperature float64) (string, error) {
	c.mu.Lock()
	model := c.model
	c.mu.Unlock()

	// Build system message from the messages list
	var systemMsg string
	var userPrompt string
	for _, m := range messages {
		switch m.Role {
		case "system":
			systemMsg = m.Content
		case "user":
			userPrompt += m.Content + "\n"
		}
	}

	// Create a session for this request
	sessionCfg := &copilot.SessionConfig{
		Model:               model,
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
	}
	if systemMsg != "" {
		sessionCfg.SystemMessage = &copilot.SystemMessageConfig{
			Content: systemMsg,
		}
	}

	session, err := c.copilotClient.CreateSession(ctx, sessionCfg)
	if err != nil {
		return "", fmt.Errorf("creating session (model=%s): %w", model, err)
	}
	defer session.Destroy()

	// Collect the response via events
	var response string
	done := make(chan struct{})
	var eventErr error

	session.On(func(event copilot.SessionEvent) {
		switch event.Type {
		case "assistant.message":
			if event.Data.Content != nil {
				response = *event.Data.Content
			}
		case "session.idle":
			close(done)
		case "error":
			if event.Data.Content != nil {
				eventErr = fmt.Errorf("session error: %s", *event.Data.Content)
			}
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})

	// Send the user prompt
	_, err = session.Send(ctx, copilot.MessageOptions{
		Prompt: userPrompt,
	})
	if err != nil {
		return "", fmt.Errorf("sending message: %w", err)
	}

	// Wait for completion or context cancellation
	select {
	case <-done:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	if eventErr != nil {
		return "", eventErr
	}

	if response == "" {
		return "", fmt.Errorf("empty response from model %s", model)
	}

	return response, nil
}

// CompleteJSON sends a request expecting a JSON response. Strips markdown fences
// if the model wraps JSON in ```json blocks.
func (c *Client) CompleteJSON(ctx context.Context, messages []ChatMessage, temperature float64) ([]byte, error) {
	content, err := c.Complete(ctx, messages, temperature)
	if err != nil {
		return nil, err
	}

	// Strip markdown JSON fences if present
	cleaned := stripJSONFences(content)

	// Validate it's actual JSON
	if !json.Valid([]byte(cleaned)) {
		return nil, fmt.Errorf("response is not valid JSON: %s", content)
	}

	return []byte(cleaned), nil
}

// stripJSONFences removes ```json ... ``` wrapping from model output.
func stripJSONFences(s string) string {
	trimmed := []byte(s)

	if bytes.HasPrefix(bytes.TrimSpace(trimmed), []byte("```")) {
		lines := bytes.Split(trimmed, []byte("\n"))
		if len(lines) >= 3 {
			start := 1
			end := len(lines) - 1
			for end > start && bytes.TrimSpace(lines[end]) == nil {
				end--
			}
			if bytes.HasPrefix(bytes.TrimSpace(lines[end]), []byte("```")) {
				lines = lines[start:end]
			} else {
				lines = lines[start:]
			}
			return string(bytes.Join(lines, []byte("\n")))
		}
	}
	return s
}
