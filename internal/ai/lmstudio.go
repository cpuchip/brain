package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// LMStudioClient talks to LM Studio's OpenAI-compatible API.
// Default endpoint: http://localhost:1234/v1
type LMStudioClient struct {
	baseURL string
	model   string
	http    *http.Client
	mu      sync.RWMutex
}

// NewLMStudioClient creates a client for LM Studio's local API.
func NewLMStudioClient(baseURL, model string) *LMStudioClient {
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	return &LMStudioClient{
		baseURL: baseURL,
		model:   model,
		http: &http.Client{
			Timeout: 2 * time.Minute, // match classify timeout
		},
	}
}

// Model returns the current model identifier.
func (c *LMStudioClient) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// SetModel hot-swaps the active model.
func (c *LMStudioClient) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
	log.Printf("LM Studio model switched to: %s", model)
}

// lmMessage is a message in the OpenAI chat format.
type lmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// lmRequest is the OpenAI chat completions request body.
type lmRequest struct {
	Model       string      `json:"model"`
	Messages    []lmMessage `json:"messages"`
	Temperature float64     `json:"temperature"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
}

// lmResponse is the OpenAI chat completions response body.
type lmResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Complete sends a chat completion request and returns the response text.
func (c *LMStudioClient) Complete(ctx context.Context, messages []ChatMessage, temperature float64) (string, error) {
	c.mu.RLock()
	model := c.model
	c.mu.RUnlock()

	// Convert to OpenAI format
	lmMsgs := make([]lmMessage, len(messages))
	for i, m := range messages {
		lmMsgs[i] = lmMessage{Role: m.Role, Content: m.Content}
	}

	reqBody := lmRequest{
		Model:       model,
		Messages:    lmMsgs,
		Temperature: temperature,
		MaxTokens:   4096, // thinking models need headroom for CoT + response
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("LM Studio request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LM Studio returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
	}

	var lmResp lmResponse
	if err := json.Unmarshal(respBody, &lmResp); err != nil {
		return "", fmt.Errorf("parsing LM Studio response: %w", err)
	}

	if lmResp.Error != nil {
		return "", fmt.Errorf("LM Studio error: %s", lmResp.Error.Message)
	}

	if len(lmResp.Choices) == 0 || lmResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("empty response from LM Studio (model: %s)", model)
	}

	return stripThinkingContent(lmResp.Choices[0].Message.Content), nil
}

// stripThinkingContent removes chain-of-thought output from "thinking" models
// like Qwen 3.5 that embed reasoning in the response content.
// Handles both <think>...</think> tags and "Thinking Process:" rendered blocks.
func stripThinkingContent(s string) string {
	// Strip <think>...</think> tags (possibly multiline, non-greedy)
	reThink := regexp.MustCompile(`(?s)<think>.*?</think>`)
	s = reThink.ReplaceAllString(s, "")

	// Strip unclosed <think> tag (model hit token limit mid-thought)
	if idx := strings.Index(s, "<think>"); idx >= 0 {
		s = s[:idx]
	}

	// Strip "Thinking Process:" blocks (LM Studio's rendered format)
	// These run from the header to the first { or end of string
	if strings.HasPrefix(strings.TrimSpace(s), "Thinking Process") {
		// Find the first JSON object start
		if idx := strings.Index(s, "{"); idx >= 0 {
			s = s[idx:]
		} else {
			// No JSON found — return empty so caller can retry
			return ""
		}
	}

	return strings.TrimSpace(s)
}

// CompleteJSON sends a request expecting a JSON response. Strips markdown fences
// if the model wraps JSON in ```json blocks. Retries once if the response isn't valid JSON.
func (c *LMStudioClient) CompleteJSON(ctx context.Context, messages []ChatMessage, temperature float64) ([]byte, error) {
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
	log.Printf("LM Studio returned non-JSON, retrying with correction...")
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
		return nil, fmt.Errorf("LM Studio response is not valid JSON after retry: %s", content[:min(len(content), 200)])
	}

	return []byte(cleaned), nil
}

// Ping checks if LM Studio is reachable by hitting the /models endpoint.
func (c *LMStudioClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("LM Studio not reachable at %s: %w", c.baseURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LM Studio returned %d from /models", resp.StatusCode)
	}
	return nil
}
