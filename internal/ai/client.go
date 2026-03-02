package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Client wraps the GitHub Models API (OpenAI-compatible).
type Client struct {
	endpoint   string
	token      string
	model      string
	httpClient *http.Client

	// Rate limiting
	mu       sync.Mutex
	calls    []time.Time
	maxCalls int
}

// NewClient creates a new AI client for GitHub Models.
func NewClient(endpoint, token, model string, maxCallsPerHour int) *Client {
	return &Client{
		endpoint: endpoint,
		token:    token,
		model:    model,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		maxCalls: maxCallsPerHour,
	}
}

// ChatRequest is the OpenAI-compatible chat completion request.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

// ChatMessage is a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse is the OpenAI-compatible chat completion response.
type ChatResponse struct {
	ID      string         `json:"id"`
	Choices []ChatChoice   `json:"choices"`
	Usage   *ChatUsage     `json:"usage,omitempty"`
	Error   *ChatError     `json:"error,omitempty"`
}

// ChatChoice is one completion choice.
type ChatChoice struct {
	Index   int         `json:"index"`
	Message ChatMessage `json:"message"`
}

// ChatUsage tracks token consumption.
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatError is returned when the API encounters an error.
type ChatError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// Complete sends a chat completion request and returns the response.
func (c *Client) Complete(ctx context.Context, messages []ChatMessage, temperature float64) (string, *ChatUsage, error) {
	if err := c.checkRateLimit(); err != nil {
		return "", nil, err
	}

	req := ChatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   2048,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", nil, fmt.Errorf("parsing response: %w", err)
	}

	if chatResp.Error != nil {
		return "", nil, fmt.Errorf("API error: %s (%s)", chatResp.Error.Message, chatResp.Error.Code)
	}

	if len(chatResp.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices in response")
	}

	c.recordCall()

	return chatResp.Choices[0].Message.Content, chatResp.Usage, nil
}

// CompleteJSON sends a request expecting a JSON response. Wraps Complete with
// a retry that strips markdown fences if the model wraps JSON in ```json blocks.
func (c *Client) CompleteJSON(ctx context.Context, messages []ChatMessage, temperature float64) ([]byte, *ChatUsage, error) {
	content, usage, err := c.Complete(ctx, messages, temperature)
	if err != nil {
		return nil, nil, err
	}

	// Strip markdown JSON fences if present
	cleaned := stripJSONFences(content)

	// Validate it's actual JSON
	if !json.Valid([]byte(cleaned)) {
		return nil, usage, fmt.Errorf("response is not valid JSON: %s", content)
	}

	return []byte(cleaned), usage, nil
}

// checkRateLimit ensures we haven't exceeded the hourly limit.
func (c *Client) checkRateLimit() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-1 * time.Hour)
	// Prune old entries
	valid := c.calls[:0]
	for _, t := range c.calls {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	c.calls = valid

	if len(c.calls) >= c.maxCalls {
		return fmt.Errorf("rate limit exceeded: %d calls in the last hour (max %d)", len(c.calls), c.maxCalls)
	}
	return nil
}

func (c *Client) recordCall() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, time.Now())
}

// stripJSONFences removes ```json ... ``` wrapping from model output.
func stripJSONFences(s string) string {
	// Trim whitespace
	trimmed := []byte(s)

	// Check for ```json prefix
	if bytes.HasPrefix(bytes.TrimSpace(trimmed), []byte("```")) {
		lines := bytes.Split(trimmed, []byte("\n"))
		if len(lines) >= 3 {
			// Remove first and last lines (the fences)
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
