// Package ibecome provides an HTTP client for creating tasks in the
// ibeco.me (becoming) app. When brain classifies a thought as "actions"
// or "projects", it can automatically create a corresponding task in
// ibecome so the user can track and check it off from any browser.
package ibecome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
)

// Client talks to the ibecome REST API.
type Client struct {
	baseURL    string // e.g. "https://ibeco.me"
	token      string // bec_... bearer token
	httpClient *http.Client
}

// NewClient creates an ibecome API client. The token is the same
// bearer token used for the relay WebSocket connection.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// taskRequest matches the JSON body expected by POST /api/tasks.
type taskRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	SourceDoc   string `json:"source_doc,omitempty"`
	Scripture   string `json:"scripture,omitempty"`
	Type        string `json:"type"`   // once | daily | weekly | ongoing
	Status      string `json:"status"` // active | completed | paused | archived
}

// taskResponse is the JSON returned by POST /api/tasks.
type taskResponse struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// CreateTaskFromResult creates a task in ibecome based on a classifier result.
// It maps brain categories and fields to ibecome task fields.
// Returns the created task ID, or 0 if creation was skipped or failed.
func (c *Client) CreateTaskFromResult(ctx context.Context, result *classifier.Result, rawText string) (int64, error) {
	req := taskRequest{
		Title:  result.Title,
		Status: "active",
	}

	// Build description from raw text + extracted fields
	var desc strings.Builder
	desc.WriteString(rawText)

	switch result.Category {
	case "actions":
		req.Type = "once"
		if result.Fields.DueDate != "" {
			desc.WriteString("\n\nDue: " + result.Fields.DueDate)
		}

	case "projects":
		req.Type = "ongoing"
		// Map classifier status to ibecome status
		switch result.Fields.Status {
		case "done":
			req.Status = "completed"
		case "waiting", "blocked":
			req.Status = "paused"
		case "someday":
			req.Status = "paused"
		default: // "active" or empty
			req.Status = "active"
		}
		if result.Fields.NextAction != "" {
			desc.WriteString("\n\nNext action: " + result.Fields.NextAction)
		}

	default:
		return 0, nil // only actions and projects become tasks
	}

	req.Description = desc.String()

	// Map scripture references if present
	if result.Fields.References != "" {
		req.Scripture = result.Fields.References
	}

	// Source doc: tag it as coming from brain
	req.SourceDoc = "brain"

	return c.createTask(ctx, req)
}

func (c *Client) createTask(ctx context.Context, task taskRequest) (int64, error) {
	body, err := json.Marshal(task)
	if err != nil {
		return 0, fmt.Errorf("marshaling task: %w", err)
	}

	url := c.baseURL + "/api/tasks"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("POST %s returned %d: %s", url, resp.StatusCode, string(respBody))
	}

	var result taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Task was created but we couldn't parse the response — not fatal
		log.Printf("[ibecome] task created but response parse failed: %v", err)
		return 0, nil
	}

	return result.ID, nil
}
