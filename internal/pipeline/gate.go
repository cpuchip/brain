package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/config"
)

// EvaluateGate asks an AI model to evaluate the current pipeline stage output
// and recommend whether to advance, revise, or surface to the human.
// Returns action ("advance", "revise", "surface"), reasoning, and feedback (for revise).
func (p *Pipeline) EvaluateGate(ctx context.Context, entryID, model string) (action, reasoning, feedback string, err error) {
	entry, err := p.store.DB().GetEntry(entryID)
	if err != nil {
		return "", "", "", fmt.Errorf("entry not found: %w", err)
	}

	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}

	// Load the scratch file content (the stage output)
	stageOutput := ""
	if entry.ScratchPath != "" {
		absPath := entry.ScratchPath
		if p.workspace != "" && !filepath.IsAbs(absPath) {
			absPath = filepath.Join(p.workspace, absPath)
		}
		if data, readErr := os.ReadFile(absPath); readErr == nil {
			stageOutput = string(data)
			// Cap at 8000 chars for the prompt
			if len(stageOutput) > 8000 {
				stageOutput = stageOutput[:8000] + "\n\n[... truncated at 8000 chars ...]"
			}
		}
	}

	// Load session messages for recent context
	msgs, _ := p.store.DB().ListSessionMessages(entryID)
	recentMsgs := ""
	if len(msgs) > 0 {
		// Take last 5 messages for context
		start := 0
		if len(msgs) > 5 {
			start = len(msgs) - 5
		}
		var parts []string
		for _, m := range msgs[start:] {
			parts = append(parts, fmt.Sprintf("[%s] %s", m.Role, truncate(m.Content, 500)))
		}
		recentMsgs = strings.Join(parts, "\n\n")
	}

	// Build project context
	projectCtx := FormatProjectContext(p.BuildProjectContext(entry))

	prompt := fmt.Sprintf(`Evaluate this pipeline stage output and decide the next action.

**Entry:** %s
**Current maturity:** %s
**Category:** %s

%s

## Stage Output (scratch file)

%s

## Recent Conversation

%s

## Your Decision

Based on the quality and completeness of the stage output, choose ONE action:

- **advance**: Output is good quality. Key questions are addressed. Ready for next stage.
- **revise**: Output needs improvement. Provide specific feedback for what to fix.
- **surface**: Something requires human judgment that you can't resolve.

Respond with ONLY a JSON object (no markdown fences):
{"action": "advance|revise|surface", "reasoning": "brief explanation", "feedback": "specific revision guidance if action is revise, empty otherwise"}`,
		entry.Title, maturity, entry.Category,
		projectCtx, stageOutput, recentMsgs)

	if model == "" {
		model = config.PipelineBigModel
	}

	agentCfg := ai.AgentConfig{
		Model:              model,
		SystemMessage:      "You are a steward evaluating pipeline stage outputs. Be concise and decisive. Your job is quality control — not perfection. Advance when the output is good enough to build on. Revise only when there are clear gaps. Surface only when genuine human judgment is needed.",
		WorkingDir:         p.resolveWorkDir(entry),
		AgentName:          "steward-gate",
		PremiumRequestCost: config.ModelCost(model),
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)
	response, askErr := agent.Ask(ctx, prompt)
	if askErr != nil {
		return "", "", "", fmt.Errorf("gate evaluation agent failed: %w", askErr)
	}

	// Track cost
	if err := p.store.DB().IncrementPremiumRequests(entryID, agentCfg.PremiumRequestCost); err != nil {
		log.Printf("warning: failed to track gate eval cost for %s: %v", entryID, err)
	}

	// Parse the JSON response
	action, reasoning, feedback, parseErr := parseGateResponse(response)
	if parseErr != nil {
		// If parsing fails, default to surface
		return "surface", fmt.Sprintf("Gate evaluation returned unparseable response: %s", truncate(response, 200)), "", nil
	}

	log.Printf("steward gate: entry=%s maturity=%s → %s (%s)", entryID, maturity, action, truncate(reasoning, 100))
	return action, reasoning, feedback, nil
}

// GenerateScenarios asks an AI model to create testable acceptance criteria
// for a planned entry, saves them on the entry, and advances to specced.
func (p *Pipeline) GenerateScenarios(ctx context.Context, entryID, model string) ([]string, error) {
	entry, err := p.store.DB().GetEntry(entryID)
	if err != nil {
		return nil, fmt.Errorf("entry not found: %w", err)
	}

	if entry.Maturity != "planned" {
		return nil, fmt.Errorf("entry must be planned to generate scenarios (currently: %s)", entry.Maturity)
	}

	// Load scratch file
	stageOutput := ""
	if entry.ScratchPath != "" {
		absPath := entry.ScratchPath
		if p.workspace != "" && !filepath.IsAbs(absPath) {
			absPath = filepath.Join(p.workspace, absPath)
		}
		if data, readErr := os.ReadFile(absPath); readErr == nil {
			stageOutput = string(data)
			if len(stageOutput) > 8000 {
				stageOutput = stageOutput[:8000] + "\n\n[... truncated ...]"
			}
		}
	}

	projectCtx := FormatProjectContext(p.BuildProjectContext(entry))

	prompt := fmt.Sprintf(`Generate testable acceptance criteria (scenarios) for this entry.

**Entry:** %s
**Category:** %s

%s

## Research & Plan Output

%s

## Requirements

Generate 3-7 specific, testable scenarios that define "done" for this entry.
Each scenario should be:
- Observable (can be verified by looking at something)
- Specific (not vague)
- Independent (each scenario tests one thing)

Respond with ONLY a JSON array of scenario strings (no markdown fences):
["scenario 1", "scenario 2", "scenario 3"]`,
		entry.Title, entry.Category, projectCtx, stageOutput)

	if model == "" {
		model = config.PipelineBigModel
	}

	agentCfg := ai.AgentConfig{
		Model:              model,
		SystemMessage:      "You are a steward generating acceptance criteria. Be practical and specific. Focus on what can be verified.",
		WorkingDir:         p.resolveWorkDir(entry),
		AgentName:          "steward-scenarios",
		PremiumRequestCost: config.ModelCost(model),
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)
	response, askErr := agent.Ask(ctx, prompt)
	if askErr != nil {
		return nil, fmt.Errorf("scenario generation agent failed: %w", askErr)
	}

	if err := p.store.DB().IncrementPremiumRequests(entryID, agentCfg.PremiumRequestCost); err != nil {
		log.Printf("warning: failed to track scenario gen cost for %s: %v", entryID, err)
	}

	// Parse scenario array
	scenarios, parseErr := parseScenariosResponse(response)
	if parseErr != nil {
		return nil, fmt.Errorf("failed to parse scenarios: %w (response: %s)", parseErr, truncate(response, 200))
	}

	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios generated")
	}

	// Save scenarios on the entry
	scenariosStr := "- " + strings.Join(scenarios, "\n- ")
	if err := p.store.DB().SetScenarios(entryID, scenariosStr); err != nil {
		return nil, fmt.Errorf("saving scenarios: %w", err)
	}

	// Advance to specced
	if err := p.store.DB().SetMaturity(entryID, "specced", ""); err != nil {
		return nil, fmt.Errorf("advancing to specced: %w", err)
	}

	p.notify("entry.updated", entryID, map[string]string{"maturity": "specced"})

	log.Printf("steward scenarios: entry=%s generated %d scenarios, advanced to specced", entryID, len(scenarios))
	return scenarios, nil
}

// EvaluateAndVerify checks execution output against scenarios and verifies the entry.
// It reads the entry's scenarios and execution output, evaluates each scenario,
// and calls Verify to update the entry's maturity.
func (p *Pipeline) EvaluateAndVerify(ctx context.Context, entryID, model string) (passed bool, reasoning string, err error) {
	entry, err := p.store.DB().GetEntry(entryID)
	if err != nil {
		return false, "", fmt.Errorf("entry not found: %w", err)
	}

	if entry.Maturity != "executing" {
		return false, "", fmt.Errorf("entry must be executing to verify (currently: %s)", entry.Maturity)
	}

	scenarios := entry.Scenarios
	if strings.TrimSpace(scenarios) == "" {
		return false, "", fmt.Errorf("entry has no scenarios to verify against")
	}

	// Load scratch file / execution output
	execOutput := ""
	if entry.ScratchPath != "" {
		absPath := entry.ScratchPath
		if p.workspace != "" && !filepath.IsAbs(absPath) {
			absPath = filepath.Join(p.workspace, absPath)
		}
		if data, readErr := os.ReadFile(absPath); readErr == nil {
			execOutput = string(data)
			if len(execOutput) > 8000 {
				execOutput = execOutput[:8000] + "\n\n[... truncated ...]"
			}
		}
	}

	// Get recent session messages for execution output context
	msgs, _ := p.store.DB().ListSessionMessages(entryID)
	execMsgs := ""
	if len(msgs) > 0 {
		start := 0
		if len(msgs) > 10 {
			start = len(msgs) - 10
		}
		var parts []string
		for _, m := range msgs[start:] {
			parts = append(parts, fmt.Sprintf("[%s] %s", m.Role, truncate(m.Content, 500)))
		}
		execMsgs = strings.Join(parts, "\n\n")
	}

	prompt := fmt.Sprintf(`Evaluate whether the execution output satisfies each acceptance scenario.

**Entry:** %s

## Scenarios

%s

## Execution Output / Scratch File

%s

## Recent Messages

%s

## Your Evaluation

For each scenario, determine if it PASSED or FAILED based on the execution output.
Be honest — if a scenario can't be verified from the available output, mark it as failed.

Respond with ONLY a JSON object (no markdown fences):
{"all_passed": true|false, "reasoning": "brief overall assessment", "results": [{"scenario": "...", "passed": true|false, "notes": "..."}]}`,
		entry.Title, scenarios, execOutput, execMsgs)

	if model == "" {
		model = config.PipelineBigModel
	}

	agentCfg := ai.AgentConfig{
		Model:              model,
		SystemMessage:      "You are a steward verifying execution results against acceptance criteria. Be honest and thorough. If evidence is insufficient to confirm a scenario, mark it as failed.",
		WorkingDir:         p.resolveWorkDir(entry),
		AgentName:          "steward-verify",
		PremiumRequestCost: config.ModelCost(model),
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)
	response, askErr := agent.Ask(ctx, prompt)
	if askErr != nil {
		return false, "", fmt.Errorf("verification agent failed: %w", askErr)
	}

	if err := p.store.DB().IncrementPremiumRequests(entryID, agentCfg.PremiumRequestCost); err != nil {
		log.Printf("warning: failed to track verification cost for %s: %v", entryID, err)
	}

	// Parse the verification response
	allPassed, verifyReasoning, results, parseErr := parseVerifyResponse(response)
	if parseErr != nil {
		return false, fmt.Sprintf("Verification response unparseable: %s", truncate(response, 200)), nil
	}

	// Convert to VerifyRequest and call Pipeline.Verify
	var scenarioResults []ScenarioResult
	for _, r := range results {
		scenarioResults = append(scenarioResults, ScenarioResult{
			Scenario: r.Scenario,
			Passed:   r.Passed,
			Notes:    r.Notes,
		})
	}

	if len(scenarioResults) > 0 {
		_, verifyErr := p.Verify(VerifyRequest{
			EntryID: entryID,
			Results: scenarioResults,
		})
		if verifyErr != nil {
			return false, "", fmt.Errorf("verify call failed: %w", verifyErr)
		}
	}

	log.Printf("steward verify: entry=%s all_passed=%v (%s)", entryID, allPassed, truncate(verifyReasoning, 100))
	return allPassed, verifyReasoning, nil
}

// --- Response parsing helpers ---

type gateResponse struct {
	Action    string `json:"action"`
	Reasoning string `json:"reasoning"`
	Feedback  string `json:"feedback"`
}

func parseGateResponse(response string) (action, reasoning, feedback string, err error) {
	// Try to extract JSON from the response
	jsonStr := extractJSON(response)
	var resp gateResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return "", "", "", fmt.Errorf("parse gate JSON: %w", err)
	}

	// Validate action
	switch resp.Action {
	case "advance", "revise", "surface":
		return resp.Action, resp.Reasoning, resp.Feedback, nil
	default:
		return "", "", "", fmt.Errorf("invalid gate action: %s", resp.Action)
	}
}

func parseScenariosResponse(response string) ([]string, error) {
	jsonStr := extractJSON(response)
	var scenarios []string
	if err := json.Unmarshal([]byte(jsonStr), &scenarios); err != nil {
		return nil, fmt.Errorf("parse scenarios JSON: %w", err)
	}
	return scenarios, nil
}

type verifyResponseResult struct {
	Scenario string `json:"scenario"`
	Passed   bool   `json:"passed"`
	Notes    string `json:"notes"`
}

type verifyResponse struct {
	AllPassed bool                   `json:"all_passed"`
	Reasoning string                 `json:"reasoning"`
	Results   []verifyResponseResult `json:"results"`
}

func parseVerifyResponse(response string) (allPassed bool, reasoning string, results []verifyResponseResult, err error) {
	jsonStr := extractJSON(response)
	var resp verifyResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return false, "", nil, fmt.Errorf("parse verify JSON: %w", err)
	}
	return resp.AllPassed, resp.Reasoning, resp.Results, nil
}

// extractJSON tries to find a JSON object or array in a response string,
// handling cases where the model wraps it in markdown code fences.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)

	// Strip markdown code fences
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

	s = strings.TrimSpace(s)

	// If it starts with { or [, return as-is
	if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		return s
	}

	// Try to find JSON object in the text
	start := strings.Index(s, "{")
	if start >= 0 {
		return s[start:]
	}
	start = strings.Index(s, "[")
	if start >= 0 {
		return s[start:]
	}

	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
