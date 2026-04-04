// pipeline-bench tests the brain classification + maturity pipeline against
// seed entries in an isolated sandbox database. Validates classification accuracy,
// maturity assessment, and prompt injection defense.
//
// Usage:
//
//	go run ./cmd/pipeline-bench [flags]
//	  -testdata    Path to test data JSON (default: cmd/pipeline-bench/testdata.json)
//	  -output      Path for results markdown (default: stdout)
//	  -clean       Remove sandbox dir after run
//	  -sandbox     Sandbox directory (default: test-sandbox)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/store"
)

// TestEntry is one seed entry with expectations.
type TestEntry struct {
	ID               string `json:"id"`
	RawText          string `json:"raw_text"`
	ExpectedCategory string `json:"expected_category"`
	ExpectedMaturity string `json:"expected_maturity"`
	Notes            string `json:"notes"`
}

// EntryResult captures what the pipeline produced for one test entry.
type EntryResult struct {
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Title      string  `json:"title"`
	Maturity   string  `json:"maturity"`
	Error      string  `json:"error,omitempty"`

	CategoryMatch bool `json:"category_match"`
	MaturityMatch bool `json:"maturity_match"`
}

func main() {
	testdataPath := flag.String("testdata", "cmd/pipeline-bench/testdata.json", "Path to test data JSON")
	outputPath := flag.String("output", "", "Path for results markdown (default: stdout)")
	sandboxDir := flag.String("sandbox", "test-sandbox", "Sandbox directory for isolated DB")
	clean := flag.Bool("clean", false, "Remove sandbox dir after run")
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

	// Create sandbox
	if err := os.MkdirAll(*sandboxDir, 0755); err != nil {
		log.Fatalf("Creating sandbox dir: %v", err)
	}
	if *clean {
		defer func() {
			log.Printf("Cleaning sandbox: %s", *sandboxDir)
			os.RemoveAll(*sandboxDir)
		}()
	}

	dbPath := *sandboxDir + "/brain.db"
	// Remove existing sandbox DB for a clean run
	os.Remove(dbPath)

	db, err := store.OpenDB(dbPath)
	if err != nil {
		log.Fatalf("Opening sandbox DB: %v", err)
	}
	defer db.Close()

	log.Printf("Sandbox DB: %s (schema with maturity columns)", dbPath)

	// Run pipeline for each entry
	results := make([]EntryResult, len(entries))
	for i, te := range entries {
		log.Printf("[%d/%d] %s", i+1, len(entries), truncate(te.RawText, 60))

		// Build a classifier.Result from the raw text to test maturity heuristics.
		// We don't call the AI classifier here — pipeline-bench tests the *maturity
		// assessment and injection defense*, not AI classification quality.
		// Use classify-bench for AI model comparison.

		result := &classifier.Result{
			Category: te.ExpectedCategory,
			Title:    te.RawText,
			Fields: classifier.Fields{
				NextAction: extractNextAction(te.RawText),
			},
		}

		// Extract sub-items if there are numbered items
		result.SubItems = extractSubItems(te.RawText)

		// Assess maturity
		maturity := classifier.AssessMaturity(result)

		er := EntryResult{
			Category:   te.ExpectedCategory,
			Confidence: 0.0, // Not testing AI classification
			Title:      truncate(te.RawText, 80),
			Maturity:   string(maturity),
		}

		// Check maturity match
		if te.ExpectedMaturity == "" {
			// Non-pipeline category — maturity should be empty
			er.MaturityMatch = maturity == ""
		} else {
			er.MaturityMatch = string(maturity) == te.ExpectedMaturity
		}
		er.CategoryMatch = true // We're using expected category as input

		results[i] = er

		status := "PASS"
		if !er.MaturityMatch {
			status = "FAIL"
		}
		log.Printf("  → maturity=%s expected=%s [%s]", er.Maturity, te.ExpectedMaturity, status)
	}

	// Test injection defense — verify entries with injection content in raw_text
	// have the delimiter wrapping applied correctly
	log.Printf("\n--- Injection defense check ---")
	for _, te := range entries {
		if !strings.Contains(te.Notes, "INJECTION") {
			continue
		}
		wrapped := classifier.WrapEntryText(te.RawText)
		hasDelimiters := strings.Contains(wrapped, "---BEGIN ENTRY---") && strings.Contains(wrapped, "---END ENTRY---")
		// Check that raw text is inside delimiters (not outside)
		beginIdx := strings.Index(wrapped, "---BEGIN ENTRY---")
		endIdx := strings.Index(wrapped, "---END ENTRY---")
		rawIdx := strings.Index(wrapped, te.RawText[:20]) // first 20 chars of raw text
		contained := rawIdx > beginIdx && rawIdx < endIdx

		status := "PASS"
		if !hasDelimiters || !contained {
			status = "FAIL"
		}
		log.Printf("  [%s] %s — delimiters=%v contained=%v [%s]",
			te.ID, truncate(te.RawText, 40), hasDelimiters, contained, status)
	}

	// Generate report
	report := generateReport(entries, results)

	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, []byte(report), 0644); err != nil {
			log.Fatalf("Writing output: %v", err)
		}
		log.Printf("Report written to %s", *outputPath)
	} else {
		fmt.Println(report)
	}

	// Summary
	pass, fail := 0, 0
	for _, r := range results {
		if r.MaturityMatch {
			pass++
		} else {
			fail++
		}
	}
	log.Printf("\nResults: %d/%d maturity matches (%d failures)", pass, len(results), fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func generateReport(entries []TestEntry, results []EntryResult) string {
	var sb strings.Builder
	sb.WriteString("# Pipeline Bench Results\n\n")
	sb.WriteString(fmt.Sprintf("*Generated: %s*\n\n", time.Now().Format("2006-01-02 15:04")))

	// Summary table
	sb.WriteString("## Maturity Assessment\n\n")
	sb.WriteString("| # | Input | Expected | Got | Match | Notes |\n")
	sb.WriteString("|---|-------|----------|-----|-------|-------|\n")

	pass, fail := 0, 0
	for i, te := range entries {
		r := results[i]
		match := "✅"
		if !r.MaturityMatch {
			match = "❌"
			fail++
		} else {
			pass++
		}
		expected := te.ExpectedMaturity
		if expected == "" {
			expected = "(none)"
		}
		got := r.Maturity
		if got == "" {
			got = "(none)"
		}
		sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s |\n",
			i+1, truncate(te.RawText, 40), expected, got, match, te.Notes))
	}

	sb.WriteString(fmt.Sprintf("\n**Score: %d/%d** (%d failures)\n\n", pass, len(entries), fail))

	// Injection defense section
	sb.WriteString("## Injection Defense\n\n")
	sb.WriteString("| ID | Input | Delimiters | Contained |\n")
	sb.WriteString("|---|-------|------------|----------|\n")
	for _, te := range entries {
		if !strings.Contains(te.Notes, "INJECTION") {
			continue
		}
		wrapped := classifier.WrapEntryText(te.RawText)
		hasDelimiters := strings.Contains(wrapped, "---BEGIN ENTRY---") && strings.Contains(wrapped, "---END ENTRY---")
		beginIdx := strings.Index(wrapped, "---BEGIN ENTRY---")
		endIdx := strings.Index(wrapped, "---END ENTRY---")
		rawIdx := strings.Index(wrapped, te.RawText[:min(20, len(te.RawText))])
		contained := rawIdx > beginIdx && rawIdx < endIdx

		delimStr := "✅"
		if !hasDelimiters {
			delimStr = "❌"
		}
		containStr := "✅"
		if !contained {
			containStr = "❌"
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			te.ID, truncate(te.RawText, 40), delimStr, containStr))
	}

	return sb.String()
}

// extractNextAction checks if the text contains action-like language.
func extractNextAction(text string) string {
	lower := strings.ToLower(text)
	actionVerbs := []string{"add ", "fix ", "create ", "build ", "implement ", "update ", "remove "}
	for _, v := range actionVerbs {
		if strings.HasPrefix(lower, v) {
			// First sentence is likely the next action
			if idx := strings.IndexAny(text, ".!"); idx > 0 && idx < 100 {
				return text[:idx]
			}
			return truncate(text, 80)
		}
	}
	return ""
}

// extractSubItems looks for numbered list-like patterns.
func extractSubItems(text string) []string {
	var items []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 3 && (line[0] >= '1' && line[0] <= '9') && (line[1] == ')' || line[1] == '.') {
			items = append(items, line[2:])
		}
	}
	return items
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
