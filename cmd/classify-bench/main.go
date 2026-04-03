// classify-bench benchmarks the brain classification system prompt across
// multiple AI models (local LM Studio and Copilot SDK) to compare quality.
//
// Usage:
//
//	go run ./cmd/classify-bench [flags]
//	  -lmstudio       Run LM Studio models (default: true if LM Studio is reachable)
//	  -copilot        Run Copilot SDK models (default: false)
//	  -lmstudio-url   LM Studio API URL (default: http://localhost:1234/v1)
//	  -testdata       Path to test data JSON (default: cmd/classify-bench/testdata.json)
//	  -output         Path for results markdown (default: stdout)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
)

// TestEntry represents one raw capture to classify.
type TestEntry struct {
	ID              string `json:"id"`
	RawText         string `json:"raw_text"`
	CurrentCategory string `json:"current_category"`
	CurrentTitle    string `json:"current_title"`
}

// ModelResult is the classification output from one model for one entry.
type ModelResult struct {
	Category   string        `json:"category"`
	Confidence float64       `json:"confidence"`
	Title      string        `json:"title"`
	Tags       []string      `json:"tags"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
}

// ModelConfig defines a model to benchmark.
type ModelConfig struct {
	Name    string // display name
	ModelID string // model identifier for the backend
	Backend string // "lmstudio" or "copilot"
}

var models = []ModelConfig{
	{Name: "Ministral 14B", ModelID: "mistralai/ministral-3-14b-reasoning", Backend: "lmstudio"},
	// Uncomment if qwen is loaded in LM Studio:
	// {Name: "Qwen 3.5 9B", ModelID: "qwen/qwen3.5-9b", Backend: "lmstudio"},
	{Name: "Claude Haiku 4.5", ModelID: "claude-3.5-haiku", Backend: "copilot"},
	{Name: "GPT-5.4 mini", ModelID: "gpt-5.4-mini", Backend: "copilot"},
	{Name: "GPT-5 mini", ModelID: "gpt-5-mini", Backend: "copilot"},
	{Name: "Raptor mini", ModelID: "raptor-mini", Backend: "copilot"},
}

func main() {
	lmstudioURL := flag.String("lmstudio-url", "http://localhost:1234/v1", "LM Studio API URL")
	testdataPath := flag.String("testdata", "cmd/classify-bench/testdata.json", "Path to test data JSON")
	outputPath := flag.String("output", "", "Path for results markdown (default: stdout)")
	runLM := flag.Bool("lmstudio", true, "Run LM Studio models")
	runCopilot := flag.Bool("copilot", false, "Run Copilot SDK models")
	flag.Parse()

	// Load test data
	data, err := os.ReadFile(*testdataPath)
	if err != nil {
		log.Fatalf("Reading test data: %v", err)
	}
	var entries []TestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Fatalf("Parsing test data: %v", err)
	}
	log.Printf("Loaded %d test entries", len(entries))

	ctx := context.Background()
	systemPrompt := classifier.DefaultClassifyPrompt

	// results[modelName][entryIndex] = ModelResult
	results := make(map[string][]ModelResult)

	// --- LM Studio models ---
	if *runLM {
		lm := ai.NewLMStudioClient(*lmstudioURL, "")
		for _, m := range models {
			if m.Backend != "lmstudio" {
				continue
			}
			log.Printf("=== %s (LM Studio) ===", m.Name)
			lm.SetModel(m.ModelID)
			results[m.Name] = benchmarkCompleter(ctx, lm, systemPrompt, entries)
		}
	}

	// --- Copilot SDK models ---
	if *runCopilot {
		copilotClient := copilot.NewClient(&copilot.ClientOptions{
			LogLevel: "error",
		})
		if err := copilotClient.Start(ctx); err != nil {
			log.Fatalf("Starting Copilot CLI: %v", err)
		}
		defer copilotClient.Stop()

		for _, m := range models {
			if m.Backend != "copilot" {
				continue
			}
			log.Printf("=== %s (Copilot SDK) ===", m.Name)
			results[m.Name] = benchmarkCopilotModel(ctx, copilotClient, m.ModelID, systemPrompt, entries)
		}
	}

	// --- Generate report ---
	report := generateReport(entries, results)

	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, []byte(report), 0644); err != nil {
			log.Fatalf("Writing output: %v", err)
		}
		log.Printf("Report written to %s", *outputPath)
	} else {
		fmt.Println(report)
	}
}

// benchmarkCompleter runs classification through a Completer (LM Studio).
func benchmarkCompleter(ctx context.Context, c ai.Completer, systemPrompt string, entries []TestEntry) []ModelResult {
	var results []ModelResult
	for i, e := range entries {
		start := time.Now()
		messages := []ai.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: e.RawText},
		}
		respBytes, err := c.CompleteJSON(ctx, messages, 0.1)
		elapsed := time.Since(start)

		if err != nil {
			log.Printf("  [%d/%d] %s → ERROR: %v", i+1, len(entries), truncate(e.RawText, 40), err)
			results = append(results, ModelResult{Error: err.Error(), Latency: elapsed})
			continue
		}

		var result classifier.Result
		if err := json.Unmarshal(respBytes, &result); err != nil {
			log.Printf("  [%d/%d] %s → PARSE ERROR: %v", i+1, len(entries), truncate(e.RawText, 40), err)
			results = append(results, ModelResult{Error: "parse: " + err.Error(), Latency: elapsed})
			continue
		}

		log.Printf("  [%d/%d] %s → %s (%.2f) %q", i+1, len(entries), truncate(e.RawText, 40), result.Category, result.Confidence, result.Title)
		results = append(results, ModelResult{
			Category:   result.Category,
			Confidence: result.Confidence,
			Title:      result.Title,
			Tags:       result.Tags,
			Latency:    elapsed,
		})
	}
	return results
}

// benchmarkCopilotModel runs classification through the Copilot SDK for one model.
func benchmarkCopilotModel(ctx context.Context, client *copilot.Client, modelID, systemPrompt string, entries []TestEntry) []ModelResult {
	// Create a single session for this model — reuse across all entries
	falseVal := false
	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
		Model: modelID,
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "replace",
			Content: systemPrompt,
		},
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		InfiniteSessions:    &copilot.InfiniteSessionConfig{Enabled: &falseVal},
		AvailableTools:      []string{}, // No tools — pure classification
	})
	if err != nil {
		log.Printf("Failed to create session for %s: %v", modelID, err)
		errResults := make([]ModelResult, len(entries))
		for i := range errResults {
			errResults[i] = ModelResult{Error: "session: " + err.Error()}
		}
		return errResults
	}
	defer session.Destroy()

	var results []ModelResult
	for i, e := range entries {
		start := time.Now()
		response, err := session.SendAndWait(ctx, copilot.MessageOptions{
			Prompt: e.RawText,
		})
		elapsed := time.Since(start)

		if err != nil {
			log.Printf("  [%d/%d] %s → ERROR: %v", i+1, len(entries), truncate(e.RawText, 40), err)
			results = append(results, ModelResult{Error: err.Error(), Latency: elapsed})
			continue
		}

		if response == nil || response.Data.Content == nil || *response.Data.Content == "" {
			log.Printf("  [%d/%d] %s → EMPTY RESPONSE", i+1, len(entries), truncate(e.RawText, 40))
			results = append(results, ModelResult{Error: "empty response", Latency: elapsed})
			continue
		}

		content := *response.Data.Content
		// Strip markdown fences if present
		content = stripJSONFences(content)

		var result classifier.Result
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			log.Printf("  [%d/%d] %s → PARSE ERROR: %v (raw: %.100s)", i+1, len(entries), truncate(e.RawText, 40), err, content)
			results = append(results, ModelResult{Error: "parse: " + err.Error(), Latency: elapsed})
			continue
		}

		log.Printf("  [%d/%d] %s → %s (%.2f) %q", i+1, len(entries), truncate(e.RawText, 40), result.Category, result.Confidence, result.Title)
		results = append(results, ModelResult{
			Category:   result.Category,
			Confidence: result.Confidence,
			Title:      result.Title,
			Tags:       result.Tags,
			Latency:    elapsed,
		})
	}
	return results
}

// generateReport produces a markdown comparison table.
func generateReport(entries []TestEntry, results map[string][]ModelResult) string {
	var sb strings.Builder
	sb.WriteString("# Classification Benchmark Results\n\n")
	sb.WriteString(fmt.Sprintf("*Generated: %s*\n\n", time.Now().Format("2006-01-02 15:04")))

	// Collect model names in stable order
	var modelNames []string
	for _, m := range models {
		if _, ok := results[m.Name]; ok {
			modelNames = append(modelNames, m.Name)
		}
	}

	if len(modelNames) == 0 {
		sb.WriteString("No models produced results.\n")
		return sb.String()
	}

	// --- Summary table ---
	sb.WriteString("## Summary\n\n")
	sb.WriteString("| # | Raw Input | ")
	for _, name := range modelNames {
		sb.WriteString(name + " | ")
	}
	sb.WriteString("\n|---|-----------|")
	for range modelNames {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")

	for i, e := range entries {
		raw := truncate(e.RawText, 50)
		sb.WriteString(fmt.Sprintf("| %d | %s | ", i+1, raw))
		for _, name := range modelNames {
			if i < len(results[name]) {
				r := results[name][i]
				if r.Error != "" {
					sb.WriteString("ERROR | ")
				} else {
					sb.WriteString(fmt.Sprintf("**%s** (%.0f%%) | ", r.Category, r.Confidence*100))
				}
			} else {
				sb.WriteString("— | ")
			}
		}
		sb.WriteString("\n")
	}

	// --- Detail per entry ---
	sb.WriteString("\n## Detail\n\n")
	for i, e := range entries {
		sb.WriteString(fmt.Sprintf("### Entry %d: %s\n\n", i+1, truncate(e.RawText, 80)))
		sb.WriteString(fmt.Sprintf("**Current:** %s → %q\n\n", e.CurrentCategory, e.CurrentTitle))

		for _, name := range modelNames {
			if i < len(results[name]) {
				r := results[name][i]
				if r.Error != "" {
					sb.WriteString(fmt.Sprintf("**%s:** ERROR — %s\n\n", name, r.Error))
				} else {
					sb.WriteString(fmt.Sprintf("**%s:** %s (%.0f%%) — %q [%s] (%dms)\n\n",
						name, r.Category, r.Confidence*100, r.Title,
						strings.Join(r.Tags, ", "), r.Latency.Milliseconds()))
				}
			}
		}
		sb.WriteString("---\n\n")
	}

	// --- Latency summary ---
	sb.WriteString("## Latency\n\n")
	sb.WriteString("| Model | Avg | Min | Max |\n|-------|-----|-----|-----|\n")
	for _, name := range modelNames {
		var total, min, max time.Duration
		count := 0
		min = time.Hour
		for _, r := range results[name] {
			if r.Error == "" {
				total += r.Latency
				count++
				if r.Latency < min {
					min = r.Latency
				}
				if r.Latency > max {
					max = r.Latency
				}
			}
		}
		if count > 0 {
			avg := total / time.Duration(count)
			sb.WriteString(fmt.Sprintf("| %s | %dms | %dms | %dms |\n", name, avg.Milliseconds(), min.Milliseconds(), max.Milliseconds()))
		} else {
			sb.WriteString(fmt.Sprintf("| %s | — | — | — |\n", name))
		}
	}

	return sb.String()
}

// stripJSONFences removes ```json ... ``` wrapping from model output.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 3 {
			end := len(lines) - 1
			for end > 1 && strings.TrimSpace(lines[end]) == "" {
				end--
			}
			if strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
				lines = lines[1:end]
			} else {
				lines = lines[1:]
			}
			return strings.Join(lines, "\n")
		}
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
