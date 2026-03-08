// eval evaluates classifier accuracy across models using a curated test dataset.
//
// Usage:
//
//	go run ./cmd/eval [--models model1,model2] [--url http://localhost:1234/v1]
//	go run ./cmd/eval --focus sub_items   # only run sub_items test cases
//	go run ./cmd/eval --focus category    # only run category test cases
//	go run ./cmd/eval --verbose           # show raw model output on failures
//	go run ./cmd/eval --timeout 5m        # per-request timeout (default 5m)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
)

// TestCase defines an input with expected classification results.
type TestCase struct {
	Name            string   // human-readable test name
	Input           string   // raw text to classify
	ExpectCategory  string   // expected category (empty = don't check)
	ExpectSubItems  []string // expected sub_items (nil = don't check, empty = expect none)
	MinSubItems     int      // minimum sub_items count (0 = don't check)
	ExpectTags      []string // tags that should appear (subset match)
	ExpectFieldName string   // expected fields.name (empty = don't check)
	Focus           string   // "category", "sub_items", "fields", "tags" — for filtering
}

// EvalResult captures one model's result on one test case.
type EvalResult struct {
	Model     string
	Profile   string
	Test      TestCase
	Duration  time.Duration
	Result    *classResult
	RawOutput string
	Error     string
	ValidJSON bool
	Passed    bool
	Failures  []string // what didn't match
}

type classResult struct {
	Category   string   `json:"category"`
	Confidence float64  `json:"confidence"`
	Title      string   `json:"title"`
	Fields     fields   `json:"fields"`
	Tags       []string `json:"tags"`
	SubItems   []string `json:"sub_items"`
}

type fields struct {
	Name       string `json:"name,omitempty"`
	Context    string `json:"context,omitempty"`
	FollowUps  string `json:"follow_ups,omitempty"`
	Status     string `json:"status,omitempty"`
	NextAction string `json:"next_action,omitempty"`
	OneLiner   string `json:"one_liner,omitempty"`
	DueDate    string `json:"due_date,omitempty"`
	References string `json:"references,omitempty"`
	Insight    string `json:"insight,omitempty"`
	Mood       string `json:"mood,omitempty"`
	Gratitude  string `json:"gratitude,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

func main() {
	modelsFlag := flag.String("models", "mistralai/ministral-3-3b,qwen/qwen3-1.7b,qwen/qwen3.5-9b", "Comma-separated model IDs")
	urlFlag := flag.String("url", "http://localhost:1234/v1", "LM Studio base URL")
	focusFlag := flag.String("focus", "", "Only run tests with this focus (category, sub_items, fields, tags)")
	verboseFlag := flag.Bool("verbose", false, "Show raw model output on failures")
	timeoutFlag := flag.Duration("timeout", 5*time.Minute, "Per-request timeout (e.g. 2m, 5m, 30s)")
	structuredFlag := flag.Bool("structured", false, "Use response_format JSON schema (structured output)")
	flag.Parse()

	models := strings.Split(*modelsFlag, ",")
	testCases := getTestCases()

	// Filter by focus if specified
	if *focusFlag != "" {
		var filtered []TestCase
		for _, tc := range testCases {
			if tc.Focus == *focusFlag {
				filtered = append(filtered, tc)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "No test cases with focus %q\n", *focusFlag)
			os.Exit(1)
		}
		testCases = filtered
	}

	fmt.Printf("=== Brain Classifier Evaluation ===\n")
	fmt.Printf("Models: %s\n", strings.Join(models, ", "))
	fmt.Printf("Tests:  %d\n", len(testCases))
	if *focusFlag != "" {
		fmt.Printf("Focus:  %s\n", *focusFlag)
	}
	if *structuredFlag {
		fmt.Printf("Mode:   structured output (response_format)\n")
	}
	fmt.Println()

	var allResults []EvalResult

	for _, model := range models {
		profileName, prompt, temp := resolveProfile(model)
		fmt.Printf("--- Model: %s [profile: %s, temp: %.1f] ---\n", model, profileName, temp)

		passed, total := 0, 0
		for _, tc := range testCases {
			total++
			result := runAndEval(*urlFlag, model, profileName, prompt, temp, tc, *timeoutFlag, *structuredFlag)
			allResults = append(allResults, result)

			icon := "✓"
			if !result.Passed {
				icon = "✗"
			} else {
				passed++
			}

			fmt.Printf("  %s [%s] %s", icon, tc.Focus, tc.Name)
			if result.Error != "" {
				fmt.Printf(" — ERROR: %s", result.Error)
			} else {
				fmt.Printf(" (%dms)", result.Duration.Milliseconds())
				if !result.Passed {
					for _, f := range result.Failures {
						fmt.Printf("\n      → %s", f)
					}
					if *verboseFlag && result.RawOutput != "" {
						truncated := result.RawOutput
						if len(truncated) > 500 {
							truncated = truncated[:500] + "..."
						}
						fmt.Printf("\n      raw: %s", truncated)
					}
				}
			}
			fmt.Println()
		}

		fmt.Printf("  Score: %d/%d (%.0f%%)\n\n", passed, total, float64(passed)/float64(total)*100)
	}

	printScoreboard(models, testCases, allResults)
}

func resolveProfile(modelID string) (name, prompt string, temp float64) {
	noThink := false
	if p := classifier.LookupProfile(modelID); p != nil {
		prompt = p.ClassifyPrompt
		name = p.Name
		temp = p.Temperature
		noThink = p.NoThink
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
	if noThink && !strings.Contains(prompt, "/no_think") {
		prompt += "\n/no_think"
	}
	return name, prompt, temp
}

func runAndEval(baseURL, model, profile, prompt string, temp float64, tc TestCase, timeout time.Duration, structured bool) EvalResult {
	er := EvalResult{
		Model:   model,
		Profile: profile,
		Test:    tc,
	}

	// Call LM Studio
	messages := []map[string]string{
		{"role": "system", "content": prompt},
		{"role": "user", "content": tc.Input},
	}
	reqMap := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": temp,
		"max_tokens":  2048,
	}
	if structured {
		reqMap["response_format"] = classifier.ClassificationSchema()
	}
	reqBody, _ := json.Marshal(reqMap)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		er.Error = err.Error()
		er.Duration = time.Since(start)
		return er
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		er.Error = err.Error()
		er.Duration = time.Since(start)
		return er
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	er.Duration = time.Since(start)

	if resp.StatusCode != 200 {
		er.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
		return er
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
		er.Error = "parse response: " + err.Error()
		return er
	}
	if lmResp.Error != nil {
		er.Error = "LM Studio: " + lmResp.Error.Message
		return er
	}
	if len(lmResp.Choices) == 0 {
		er.Error = "empty choices"
		return er
	}

	raw := lmResp.Choices[0].Message.Content
	er.RawOutput = raw

	cleaned := stripThinking(raw)
	cleaned = stripJSONFences(cleaned)
	er.ValidJSON = json.Valid([]byte(cleaned))

	if !er.ValidJSON {
		er.Error = "invalid JSON"
		return er
	}

	var cr classResult
	if err := json.Unmarshal([]byte(cleaned), &cr); err != nil {
		er.Error = "unmarshal: " + err.Error()
		return er
	}
	er.Result = &cr

	// Evaluate against expectations
	er.Passed = true

	if tc.ExpectCategory != "" && cr.Category != tc.ExpectCategory {
		er.Passed = false
		er.Failures = append(er.Failures, fmt.Sprintf("category: got %q, want %q", cr.Category, tc.ExpectCategory))
	}

	if tc.ExpectSubItems != nil {
		if len(tc.ExpectSubItems) == 0 && len(cr.SubItems) > 0 {
			er.Passed = false
			er.Failures = append(er.Failures, fmt.Sprintf("sub_items: got %d items, want none", len(cr.SubItems)))
		}
		if len(tc.ExpectSubItems) > 0 {
			// Check that each expected item appears (substring match)
			for _, want := range tc.ExpectSubItems {
				found := false
				wantLower := strings.ToLower(want)
				for _, got := range cr.SubItems {
					if strings.Contains(strings.ToLower(got), wantLower) {
						found = true
						break
					}
				}
				if !found {
					er.Passed = false
					er.Failures = append(er.Failures, fmt.Sprintf("sub_items: missing %q (got: %v)", want, cr.SubItems))
				}
			}
		}
	}

	if tc.MinSubItems > 0 && len(cr.SubItems) < tc.MinSubItems {
		er.Passed = false
		er.Failures = append(er.Failures, fmt.Sprintf("sub_items count: got %d, want >= %d", len(cr.SubItems), tc.MinSubItems))
	}

	if len(tc.ExpectTags) > 0 {
		gotTags := make(map[string]bool)
		for _, t := range cr.Tags {
			gotTags[strings.ToLower(t)] = true
		}
		for _, want := range tc.ExpectTags {
			if !gotTags[strings.ToLower(want)] {
				er.Passed = false
				er.Failures = append(er.Failures, fmt.Sprintf("tags: missing %q (got: %v)", want, cr.Tags))
			}
		}
	}

	if tc.ExpectFieldName != "" {
		if !strings.EqualFold(cr.Fields.Name, tc.ExpectFieldName) {
			er.Passed = false
			er.Failures = append(er.Failures, fmt.Sprintf("fields.name: got %q, want %q", cr.Fields.Name, tc.ExpectFieldName))
		}
	}

	return er
}

func printScoreboard(models []string, testCases []TestCase, results []EvalResult) {
	fmt.Println("=== SCOREBOARD ===")
	fmt.Println()

	// Collect focuses
	focuses := []string{}
	seen := map[string]bool{}
	for _, tc := range testCases {
		if !seen[tc.Focus] {
			focuses = append(focuses, tc.Focus)
			seen[tc.Focus] = true
		}
	}

	// Header
	fmt.Printf("%-30s", "Model")
	for _, f := range focuses {
		fmt.Printf("  %-12s", f)
	}
	fmt.Printf("  %-12s  %8s\n", "TOTAL", "Avg(ms)")
	fmt.Println(strings.Repeat("-", 30+14*(len(focuses)+1)+10))

	for _, model := range models {
		fmt.Printf("%-30s", truncate(model, 29))

		totalPassed, totalCount := 0, 0
		var totalDuration time.Duration

		for _, focus := range focuses {
			passed, count := 0, 0
			for _, r := range results {
				if r.Model != model || r.Test.Focus != focus {
					continue
				}
				count++
				if r.Passed {
					passed++
				}
			}
			if count > 0 {
				pct := float64(passed) / float64(count) * 100
				fmt.Printf("  %d/%d (%3.0f%%)  ", passed, count, pct)
			} else {
				fmt.Printf("  %-12s", "-")
			}
			totalPassed += passed
			totalCount += count
		}

		for _, r := range results {
			if r.Model == model {
				totalDuration += r.Duration
			}
		}

		if totalCount > 0 {
			pct := float64(totalPassed) / float64(totalCount) * 100
			avg := totalDuration / time.Duration(totalCount)
			fmt.Printf("  %d/%d (%3.0f%%)  %6dms", totalPassed, totalCount, pct, avg.Milliseconds())
		}
		fmt.Println()
	}

	// Failures detail at the end
	fmt.Println("\n=== FAILURES ===")
	anyFailure := false
	for _, r := range results {
		if r.Passed || r.Error != "" {
			continue
		}
		anyFailure = true
		fmt.Printf("\n%s | %s [%s]\n", truncate(r.Model, 25), r.Test.Name, r.Test.Focus)
		fmt.Printf("  Input: %s\n", truncate(r.Test.Input, 100))
		for _, f := range r.Failures {
			fmt.Printf("  → %s\n", f)
		}
	}
	if !anyFailure {
		fmt.Println("  None! All tests passed.")
	}
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
