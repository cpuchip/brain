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

	// Persistent session for classification (reused across thoughts)
	classifierSession *copilot.Session
	classifierSystem  string // system message the session was created with
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

	if c.classifierSession != nil {
		c.classifierSession.Destroy()
		c.classifierSession = nil
	}

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

// CopilotClient returns the underlying Copilot SDK client for use by Agent sessions.
func (c *Client) CopilotClient() *copilot.Client {
	return c.copilotClient
}

// SetModel hot-swaps the active model. Destroys any cached session so the next
// call picks up the new model.
func (c *Client) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
	if c.classifierSession != nil {
		c.classifierSession.Destroy()
		c.classifierSession = nil
	}
	log.Printf("AI model switched to: %s", model)
}

// ChatMessage is a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Complete sends a prompt through the Copilot SDK and returns the response text.
// If the system message matches a previous call, the existing session is reused
// (avoiding a new background process per thought). A new session is created when
// the system message or model changes.
func (c *Client) Complete(ctx context.Context, messages []ChatMessage, temperature float64) (string, error) {
	c.mu.Lock()
	model := c.model

	// Build system message and user prompt from the messages list
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

	// Reuse existing session if system message matches, otherwise create new
	session := c.classifierSession
	if session != nil && c.classifierSystem != systemMsg {
		session.Destroy()
		session = nil
		c.classifierSession = nil
	}

	if session == nil {
		c.mu.Unlock() // unlock during session creation (may block)

		sessionCfg := &copilot.SessionConfig{
			Model:               model,
			OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		}
		if systemMsg != "" {
			sessionCfg.SystemMessage = &copilot.SystemMessageConfig{
				Content: systemMsg,
			}
		}

		var err error
		session, err = c.copilotClient.CreateSession(ctx, sessionCfg)
		if err != nil {
			return "", fmt.Errorf("creating session (model=%s): %w", model, err)
		}

		c.mu.Lock()
		// Check if another goroutine created one while we were unlocked
		if c.classifierSession != nil {
			session.Destroy()
			session = c.classifierSession
		} else {
			c.classifierSession = session
			c.classifierSystem = systemMsg
			log.Printf("Created reusable classifier session (model: %s)", model)
		}
	}
	c.mu.Unlock()

	// Send and wait for response (synchronous — much simpler than event-driven)
	response, err := session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: userPrompt,
	})
	if err != nil {
		// Session may be broken — destroy it so next call creates a fresh one
		c.mu.Lock()
		if c.classifierSession == session {
			c.classifierSession.Destroy()
			c.classifierSession = nil
		}
		c.mu.Unlock()
		return "", fmt.Errorf("sending message: %w", err)
	}

	if response == nil || response.Data.Content == nil || *response.Data.Content == "" {
		return "", fmt.Errorf("empty response from model %s", model)
	}

	return *response.Data.Content, nil
}

// CompleteJSON sends a request expecting a JSON response. Strips markdown fences
// if the model wraps JSON in ```json blocks. Retries once if the response isn't valid JSON.
func (c *Client) CompleteJSON(ctx context.Context, messages []ChatMessage, temperature float64) ([]byte, error) {
	content, err := c.Complete(ctx, messages, temperature)
	if err != nil {
		return nil, err
	}

	// Strip markdown JSON fences if present
	cleaned := stripJSONFences(content)

	// Validate it's actual JSON
	if json.Valid([]byte(cleaned)) {
		return []byte(cleaned), nil
	}

	// Model returned prose instead of JSON — retry with a nudge
	log.Printf("Model returned non-JSON, retrying with correction...")
	retryMessages := append(messages, ChatMessage{
		Role:    "user",
		Content: "Your response was not valid JSON. Return ONLY the JSON object, nothing else.",
	})
	content, err = c.Complete(ctx, retryMessages, temperature)
	if err != nil {
		return nil, err
	}

	cleaned = stripJSONFences(content)
	if !json.Valid([]byte(cleaned)) {
		return nil, fmt.Errorf("response is not valid JSON after retry: %s", content[:min(len(content), 200)])
	}

	return []byte(cleaned), nil
}

// CompleteStructuredJSON falls back to CompleteJSON for the Copilot SDK client,
// which does not support response_format schema-constrained generation.
func (c *Client) CompleteStructuredJSON(ctx context.Context, messages []ChatMessage, temperature float64, schema map[string]any) ([]byte, error) {
	return c.CompleteJSON(ctx, messages, temperature)
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
