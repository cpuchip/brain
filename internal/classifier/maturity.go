package classifier

import (
	"strings"
)

// MaturityStage represents how ready an entry is to act on.
type MaturityStage string

const (
	MaturityRaw        MaturityStage = "raw"
	MaturityResearched MaturityStage = "researched"
	MaturityPlanned    MaturityStage = "planned"
	MaturitySpecced    MaturityStage = "specced"
	MaturityExecuting  MaturityStage = "executing"
	MaturityVerified   MaturityStage = "verified"
)

// PipelineCategories are the categories that enter the maturity pipeline.
var PipelineCategories = map[string]bool{
	"ideas":    true,
	"projects": true,
	"study":    true,
}

// AssessMaturity evaluates a classified entry's readiness.
// Most entries start as "raw". Entries that are already concrete and actionable
// may start at "planned" (clear scope/approach) or rarely "specced" (has
// acceptance criteria). This is a heuristic, not AI — fast and deterministic.
func AssessMaturity(result *Result) MaturityStage {
	if !PipelineCategories[result.Category] {
		return "" // not a pipeline category — no maturity assigned
	}

	body := strings.ToLower(result.Fields.Notes)
	title := strings.ToLower(result.Title)
	oneLiner := strings.ToLower(result.Fields.OneLiner)
	nextAction := strings.ToLower(result.Fields.NextAction)
	text := body + " " + title + " " + oneLiner + " " + nextAction

	// Check for spec-level signals: scenarios, acceptance criteria, testable conditions.
	// Require multiple signals or unambiguous markers to avoid false positives from
	// casual language (e.g. "must be processed" in prose isn't a spec).
	specSignals := 0
	strongSpec := []string{"scenario", "acceptance criteria", "test case"}
	for _, sig := range strongSpec {
		if strings.Contains(text, sig) {
			specSignals += 2 // strong signals count double
		}
	}
	weakSpec := []string{"when i", "given that", "should be", "must be"}
	for _, sig := range weakSpec {
		if strings.Contains(text, sig) {
			specSignals++
		}
	}
	if specSignals >= 2 {
		return MaturitySpecced
	}

	// Check for plan-level signals: clear approach, concrete steps, specific actions
	planSignals := 0
	if result.Fields.NextAction != "" {
		planSignals++
	}
	if result.Fields.Status == "active" || result.Fields.Status == "waiting" {
		planSignals++
	}
	if len(result.SubItems) >= 2 {
		planSignals++ // has a checklist
	}
	// Concrete language patterns — count each match
	concretePatterns := []string{"add a ", "add the ", "fix the ", "update the ", "change the ", "create a ", "implement ", "wire up ", "connect "}
	concreteCount := 0
	for _, pat := range concretePatterns {
		if strings.Contains(text, pat) {
			concreteCount++
		}
	}
	if concreteCount > 0 {
		planSignals++
	}
	if concreteCount >= 2 {
		planSignals++ // multiple concrete actions = stronger signal
	}
	if planSignals >= 2 {
		return MaturityPlanned
	}

	return MaturityRaw
}
