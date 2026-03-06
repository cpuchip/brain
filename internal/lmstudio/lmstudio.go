// Package lmstudio provides utilities for managing the LM Studio server,
// including auto-starting the server and auto-loading models.
package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Model represents a loaded model in LM Studio.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// EnsureServer checks if LM Studio server is running and starts it if not.
// Returns nil if the server is reachable (or was successfully started).
func EnsureServer(ctx context.Context, baseURL string) error {
	if isServerRunning(ctx, baseURL) {
		return nil
	}

	log.Printf("LM Studio server not running, starting with 'lms server start'...")

	// Check that lms CLI is available
	if _, err := exec.LookPath("lms"); err != nil {
		return fmt.Errorf("lms CLI not found in PATH — install LM Studio and ensure 'lms' is available: %w", err)
	}

	cmd := exec.CommandContext(ctx, "lms", "server", "start")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("starting LM Studio server: %w\nOutput: %s", err, string(output))
	}
	log.Printf("lms server start: %s", strings.TrimSpace(string(output)))

	// Wait for server to be ready (up to 30 seconds)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if isServerRunning(ctx, baseURL) {
			log.Printf("LM Studio server is ready")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return fmt.Errorf("LM Studio server did not become ready within 30 seconds")
}

// EnsureModel checks if the given model is loaded in LM Studio, and loads it if not.
// The modelID should match what LM Studio recognizes (e.g. "text-embedding-qwen3-embedding-4b").
func EnsureModel(ctx context.Context, baseURL, modelID string) error {
	loaded, err := ListModels(ctx, baseURL)
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}

	for _, m := range loaded {
		if m.ID == modelID || strings.Contains(m.ID, modelID) {
			return nil // Already loaded
		}
	}

	log.Printf("Model %q not loaded, loading with 'lms load %s'...", modelID, modelID)

	cmd := exec.CommandContext(ctx, "lms", "load", modelID, "--yes")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("loading model %q: %w\nOutput: %s", modelID, err, string(output))
	}
	log.Printf("lms load: %s", strings.TrimSpace(string(output)))

	// Verify model is now loaded (with retry)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		loaded, err := ListModels(ctx, baseURL)
		if err == nil {
			for _, m := range loaded {
				if m.ID == modelID || strings.Contains(m.ID, modelID) {
					log.Printf("Model %q loaded successfully", modelID)
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	return fmt.Errorf("model %q did not appear in loaded models within 60 seconds", modelID)
}

// ListModels returns the currently loaded models from LM Studio.
func ListModels(ctx context.Context, baseURL string) ([]Model, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, string(body))
	}

	var result struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	return result.Data, nil
}

// LoadModelAPI loads a model via the LM Studio REST API (POST /api/v1/models/load).
// This is the HTTP-based alternative to the CLI 'lms load' approach —
// useful when the lms CLI isn't available or for programmatic model switching.
func LoadModelAPI(ctx context.Context, baseURL, modelID string) error {
	// REST API uses a different base than the OpenAI-compat endpoint
	apiBase := toRESTBase(baseURL)

	reqBody, _ := json.Marshal(map[string]any{
		"model": modelID,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/api/v1/models/load", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("creating load request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Minute} // loading can be slow
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("loading model %q via API: %w", modelID, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("load model %q returned %d: %s", modelID, resp.StatusCode, string(body[:min(len(body), 300)]))
	}

	log.Printf("Model %q loaded via REST API", modelID)
	return nil
}

// UnloadModelAPI unloads a model via the LM Studio REST API (POST /api/v1/models/unload).
func UnloadModelAPI(ctx context.Context, baseURL, instanceID string) error {
	apiBase := toRESTBase(baseURL)

	reqBody, _ := json.Marshal(map[string]string{
		"instance_id": instanceID,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/api/v1/models/unload", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("creating unload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unloading model %q via API: %w", instanceID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unload model %q returned %d: %s", instanceID, resp.StatusCode, string(body[:min(len(body), 300)]))
	}

	log.Printf("Model %q unloaded via REST API", instanceID)
	return nil
}

// toRESTBase converts an OpenAI-compat base URL (http://localhost:1234/v1)
// to the REST API base (http://localhost:1234).
func toRESTBase(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

// isServerRunning checks if the LM Studio server responds to a simple request.
func isServerRunning(ctx context.Context, baseURL string) bool {
	url := strings.TrimRight(baseURL, "/") + "/models"
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
