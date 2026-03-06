// bench benchmarks classification models via LM Studio.
// Usage: go run ./cmd/bench [--models model1,model2] [--input "raw text"]
//
// When --use-profiles is set (default), each model uses its registered profile's
// system prompt and temperature. Without it, a single default prompt is used.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
	_ "modernc.org/sqlite"
)

type classResult struct {
	Category   string   `json:"category"`
	Confidence float64  `json:"confidence"`
	Title      string   `json:"title"`
	Fields     any      `json:"fields"`
	Tags       []string `json:"tags"`
}

type benchResult struct {
	Model       string        `json:"model"`
	Profile     string        `json:"profile"`
	Input       string        `json:"input"`
	Duration    time.Duration `json:"duration_ms"`
	Result      *classResult  `json:"result,omitempty"`
	RawOutput   string        `json:"raw_output,omitempty"`
	Error       string        `json:"error,omitempty"`
	ValidJSON   bool          `json:"valid_json"`
	RetryNeeded bool          `json:"retry_needed"`
}

func main() {
	modelsFlag := flag.String("models", "mistralai/ministral-3-3b,qwen/qwen3-1.7b,qwen/qwen3.5-9b", "Comma-separated model IDs")
	inputFlag := flag.String("input", "", "Raw text to classify (if empty, reads from DB)")
	dbFlag := flag.String("db", "", "Path to brain.db (auto-detected if empty)")
	urlFlag := flag.String("url", "http://localhost:1234/v1", "LM Studio base URL")
	limitFlag := flag.Int("limit", 5, "Number of DB entries to test")
	useProfiles := flag.Bool("use-profiles", true, "Use per-model profile prompts (false = single default prompt)")
	flag.Parse()

	models := strings.Split(*modelsFlag, ",")

	var inputs []string
	if *inputFlag != "" {
		inputs = []string{*inputFlag}
	} else {
		inputs = loadFromDB(*dbFlag, *limitFlag)
	}

	if len(inputs) == 0 {
		log.Fatal("No test inputs found")
	}

	fmt.Printf("=== Brain Classification Benchmark ===\n")
	fmt.Printf("Models: %s\n", strings.Join(models, ", "))
	fmt.Printf("Inputs: %d\n", len(inputs))
	fmt.Printf("Profiles: %v\n\n", *useProfiles)

	var allResults []benchResult
	for _, model := range models {
		profile, prompt, temp := resolveProfile(model, *useProfiles)
		fmt.Printf("--- Model: %s [profile: %s, temp: %.1f] ---\n", model, profile, temp)

		for i, input := range inputs {
			truncated := input
			if len(truncated) > 80 {
				truncated = truncated[:80] + "..."
			}
			fmt.Printf("  [%d] %q\n", i+1, truncated)

			result := runClassification(*urlFlag, model, input, prompt, temp)
			result.Profile = profile
			allResults = append(allResults, result)

			if result.Error != "" {
				fmt.Printf("      ERROR: %s (%.0fms)\n", result.Error, float64(result.Duration.Milliseconds()))
			} else {
				fmt.Printf("      -> %s (%.0f%%) %q  [%.0fms] json=%v retry=%v\n",
					result.Result.Category,
					result.Result.Confidence*100,
					result.Result.Title,
					float64(result.Duration.Milliseconds()),
					result.ValidJSON,
					result.RetryNeeded,
				)
			}
		}
		fmt.Println()
	}

	printSummary(models, inputs, allResults)
}

func resolveProfile(modelID string, useProfiles bool) (name, prompt string, temp float64) {
	if useProfiles {
		if p := classifier.LookupProfile(modelID); p != nil {
			prompt = p.ClassifyPrompt
			name = p.Name
			temp = p.Temperature
		}
	}
	if prompt == "" {
		prompt = classifier.DefaultClassifyPrompt
		if name == "" {
			name = "default"
		}
	}
	if temp == 0 {
		temp = 0.1
	}
	return name, prompt, temp
}

func loadFromDB(dbPath string, limit int) []string {
	if dbPath == "" {
		dbPath = findBrainDB()
	}
	if dbPath == "" {
		log.Println("Could not find brain.db -- provide --db flag or --input text")
		return nil
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("Opening DB: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT body FROM entries WHERE body != '' ORDER BY RANDOM() LIMIT ?`, limit)
	if err != nil {
		log.Fatalf("Querying entries: %v", err)
	}
	defer rows.Close()

	var inputs []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			log.Printf("Scan error: %v", err)
			continue
		}
		inputs = append(inputs, body)
	}

	fmt.Printf("Loaded %d entries from %s\n", len(inputs), dbPath)
	return inputs
}

func findBrainDB() string {
	candidates := []string{
		"private-brain/brain.db",
		"../private-brain/brain.db",
		"../../private-brain/brain.db",
	}
	home := os.Getenv("USERPROFILE")
	if home != "" {
		candidates = append(candidates,
			home+`\Documents\code\stuffleberry\scripture-study\private-brain\brain.db`,
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func runClassification(baseURL, model, input, prompt string, temp float64) benchResult {
	br := benchResult{Model: model, Input: input}

	messages := []map[string]string{
		{"role": "system", "content": prompt},
		{"role": "user", "content": input},
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": temp,
		"max_tokens":  32768,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		br.Error = err.Error()
		br.Duration = time.Since(start)
		return br
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		br.Error = err.Error()
		br.Duration = time.Since(start)
		return br
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	br.Duration = time.Since(start)

	if resp.StatusCode != 200 {
		br.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
		return br
	}

	var lmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error,omitempty"`
	}

	if err := json.Unmarshal(body, &lmResp); err != nil {
		br.Error = "parse response: " + err.Error()
		return br
	}
	if lmResp.Error != nil {
		br.Error = "LM Studio: " + lmResp.Error.Message
		return br
	}
	if len(lmResp.Choices) == 0 {
		br.Error = "empty choices"
		return br
	}

	raw := lmResp.Choices[0].Message.Content
	br.RawOutput = raw

	cleaned := stripThinking(raw)
	cleaned = stripJSONFences(cleaned)
	br.ValidJSON = json.Valid([]byte(cleaned))

	if br.ValidJSON {
		var cr classResult
		if err := json.Unmarshal([]byte(cleaned), &cr); err == nil {
			br.Result = &cr
		} else {
			br.Error = "unmarshal result: " + err.Error()
		}
	} else {
		br.RetryNeeded = true
		retryResult := retryClassification(baseURL, model, input, raw, prompt, temp)
		if retryResult != nil {
			br.Result = retryResult
			br.ValidJSON = true
		} else {
			br.Error = "invalid JSON even after retry"
		}
	}

	return br
}

func retryClassification(baseURL, model, input, badResponse, prompt string, temp float64) *classResult {
	messages := []map[string]string{
		{"role": "system", "content": prompt},
		{"role": "user", "content": input},
		{"role": "assistant", "content": badResponse},
		{"role": "user", "content": "Your response was not valid JSON. Return ONLY the JSON object, nothing else."},
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": temp,
		"max_tokens":  32768,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var lmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &lmResp); err != nil || len(lmResp.Choices) == 0 {
		return nil
	}

	cleaned := stripThinking(lmResp.Choices[0].Message.Content)
	cleaned = stripJSONFences(cleaned)

	var cr classResult
	if json.Valid([]byte(cleaned)) {
		if err := json.Unmarshal([]byte(cleaned), &cr); err == nil {
			return &cr
		}
	}
	return nil
}

func stripThinking(s string) string {
	re := regexp.MustCompile(`(?s)<think>.*?</think>`)
	s = re.ReplaceAllString(s, "")
	if idx := strings.Index(s, "<think>"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

func printSummary(models []string, inputs []string, results []benchResult) {
	fmt.Println("=== SUMMARY ===")
	fmt.Printf("%-30s %-15s %8s %8s %8s %8s %8s\n",
		"Model", "Profile", "Avg(ms)", "Min(ms)", "Max(ms)", "JSON OK", "Retries")
	fmt.Println(strings.Repeat("-", 97))

	for _, model := range models {
		var durations []time.Duration
		jsonOK, retries := 0, 0
		profile := "default"
		for _, r := range results {
			if r.Model != model {
				continue
			}
			durations = append(durations, r.Duration)
			if r.ValidJSON {
				jsonOK++
			}
			if r.RetryNeeded {
				retries++
			}
			if r.Profile != "" {
				profile = r.Profile
			}
		}
		if len(durations) == 0 {
			continue
		}

		var sum, mn, mx time.Duration
		mn = durations[0]
		for _, d := range durations {
			sum += d
			if d < mn {
				mn = d
			}
			if d > mx {
				mx = d
			}
		}
		avg := sum / time.Duration(len(durations))

		fmt.Printf("%-30s %-15s %8d %8d %8d %5d/%d  %5d\n",
			model, profile,
			avg.Milliseconds(), mn.Milliseconds(), mx.Milliseconds(),
			jsonOK, len(durations), retries,
		)
	}

	fmt.Printf("\n=== PER-INPUT COMPARISON ===\n\n")
	for i, input := range inputs {
		truncated := input
		if len(truncated) > 100 {
			truncated = truncated[:100] + "..."
		}
		fmt.Printf("Input %d: %q\n", i+1, truncated)
		for _, r := range results {
			if r.Input != input {
				continue
			}
			if r.Error != "" {
				fmt.Printf("  %-25s ERROR: %s\n", r.Model, r.Error)
			} else if r.Result != nil {
				fmt.Printf("  %-25s -> %-10s %5.0f%% %q  %dms\n",
					r.Model, r.Result.Category,
					r.Result.Confidence*100,
					r.Result.Title,
					r.Duration.Milliseconds(),
				)
			}
		}
		fmt.Println()
	}
}
