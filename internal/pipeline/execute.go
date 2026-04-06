package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/store"
)

// ExecuteModel is the model used for execution passes (needs strong reasoning + tool use).
const ExecuteModel = "claude-sonnet-4"

// ExecuteRequest holds parameters for kicking off execution of a specced entry.
type ExecuteRequest struct {
	EntryID  string `json:"id"`
	Feedback string `json:"feedback,omitempty"` // optional human guidance
}

// ExecuteResult is returned immediately when execution is kicked off.
type ExecuteResult struct {
	EntryID string `json:"id"`
	Message string `json:"message"`
}

// VerifyRequest holds the scenario verification results from the human.
type VerifyRequest struct {
	EntryID string           `json:"id"`
	Results []ScenarioResult `json:"results"`
}

// ScenarioResult is a single scenario's pass/fail from human verification.
type ScenarioResult struct {
	Scenario string `json:"scenario"`
	Passed   bool   `json:"passed"`
	Notes    string `json:"notes,omitempty"`
}

// VerifyResult is returned after verification is processed.
type VerifyResult struct {
	EntryID     string `json:"id"`
	AllPassed   bool   `json:"all_passed"`
	NewMaturity string `json:"new_maturity"`
	Message     string `json:"message"`
}

// Execute validates an entry is specced with scenarios, marks it as executing,
// and kicks off the execution agent in a background goroutine.
// Returns immediately — execution runs async.
func (p *Pipeline) Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error) {
	entry, err := p.store.DB().GetEntry(req.EntryID)
	if err != nil {
		return nil, fmt.Errorf("entry not found: %w", err)
	}

	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}
	if maturity != "specced" {
		return nil, fmt.Errorf("entry must be specced to execute (currently: %s)", maturity)
	}

	if entry.Notebook {
		return nil, fmt.Errorf("entry is in notebook mode — remove from notebook to execute")
	}

	if strings.TrimSpace(entry.Scenarios) == "" {
		return nil, fmt.Errorf("entry has no scenarios — cannot execute without acceptance criteria")
	}

	if p.pool == nil {
		return nil, fmt.Errorf("agent pool not available — execution requires Copilot SDK")
	}

	// Mark as executing
	if err := p.store.DB().SetMaturity(entry.ID, "executing", ""); err != nil {
		return nil, fmt.Errorf("setting maturity to executing: %w", err)
	}

	// Post session message about execution starting
	p.store.DB().AddSessionMessage(entry.ID, "agent",
		fmt.Sprintf("Execution started. Agent will work through %d scenarios.",
			countScenarios(entry.Scenarios)))

	p.notify("entry.updated", entry.ID, map[string]string{"maturity": "executing"})
	p.notify("message.new", entry.ID, map[string]string{"role": "agent"})

	// Fire and forget — execution runs in background
	go p.runExecute(entry, req.Feedback)

	return &ExecuteResult{
		EntryID: entry.ID,
		Message: fmt.Sprintf("Execution started for '%s'", entry.Title),
	}, nil
}

// Verify processes the human's scenario pass/fail results.
// All pass → verified. Any fail → back to planned with feedback.
func (p *Pipeline) Verify(req VerifyRequest) (*VerifyResult, error) {
	entry, err := p.store.DB().GetEntry(req.EntryID)
	if err != nil {
		return nil, fmt.Errorf("entry not found: %w", err)
	}

	if entry.Maturity != "executing" {
		return nil, fmt.Errorf("entry must be in executing state to verify (currently: %s)", entry.Maturity)
	}

	if len(req.Results) == 0 {
		return nil, fmt.Errorf("no scenario results provided")
	}

	// Check which scenarios passed/failed
	var failed []string
	allPassed := true
	for _, r := range req.Results {
		if !r.Passed {
			allPassed = false
			note := r.Scenario
			if r.Notes != "" {
				note += " — " + r.Notes
			}
			failed = append(failed, note)
		}
	}

	if allPassed {
		if err := p.store.DB().SetMaturity(entry.ID, "verified", "All scenarios passed"); err != nil {
			return nil, fmt.Errorf("setting maturity to verified: %w", err)
		}
		p.store.DB().AddSessionMessage(entry.ID, "agent",
			fmt.Sprintf("Verified! All %d scenarios passed.\n\n"+
				"**Sabbath moment:** Before we close this — what worked well? What would you do differently? Any loose ends?",
				len(req.Results)))

		p.notify("entry.updated", entry.ID, map[string]string{"maturity": "verified"})
		p.notify("message.new", entry.ID, map[string]string{"role": "agent"})

		return &VerifyResult{
			EntryID:     entry.ID,
			AllPassed:   true,
			NewMaturity: "verified",
			Message:     fmt.Sprintf("All %d scenarios passed — entry verified!", len(req.Results)),
		}, nil
	}

	// Some failed — return to planned with feedback
	feedback := fmt.Sprintf("Verification failed. %d/%d scenarios failed:\n- %s",
		len(failed), len(req.Results), strings.Join(failed, "\n- "))

	if err := p.store.DB().SetMaturity(entry.ID, "planned", feedback); err != nil {
		return nil, fmt.Errorf("setting maturity back to planned: %w", err)
	}
	p.store.DB().AddSessionMessage(entry.ID, "agent", "Verification failed. Returned to planned.\n\n"+feedback)

	p.notify("entry.updated", entry.ID, map[string]string{"maturity": "planned"})
	p.notify("message.new", entry.ID, map[string]string{"role": "agent"})

	return &VerifyResult{
		EntryID:     entry.ID,
		AllPassed:   false,
		NewMaturity: "planned",
		Message:     feedback,
	}, nil
}

// BuildExecutionContext builds the full prompt that would be sent to the execution agent.
// Used both for the preview endpoint and the actual execution.
func (p *Pipeline) BuildExecutionContext(entry *store.Entry, feedback string) string {
	return buildExecutePrompt(entry, p.loadScratchContent(entry), feedback, FormatProjectContext(p.BuildProjectContext(entry)))
}

// runExecute is the background goroutine that actually runs the execution agent.
func (p *Pipeline) runExecute(entry *store.Entry, feedback string) {
	ctx := p.pool.StartTask(entry.ID, "execute")
	defer p.pool.FinishTask(entry.ID)

	// Load all accumulated context
	scratchContent := p.loadScratchContent(entry)
	projectCtx := FormatProjectContext(p.BuildProjectContext(entry))

	prompt := buildExecutePrompt(entry, scratchContent, feedback, projectCtx)

	// Load governance document
	govDoc := ""
	govPath := filepath.Join(p.codeDir, "docs", "governance", "execute-covenant.md")
	if data, err := os.ReadFile(govPath); err == nil {
		govDoc = string(data)
	}

	// Build system message
	systemMsg := "You are an execution agent for the brain pipeline.\n\n"
	if govDoc != "" {
		systemMsg += "## Your Governance Covenant\n\n" + govDoc + "\n\n---\n\n"
	}
	systemMsg += `Your job is to implement the specced plan for the entry below.

Rules:
1. Read the plan and scenarios carefully before writing any code
2. Implement according to the plan — don't redesign unless blocked
3. Write working code, not pseudocode or summaries
4. Test against the scenarios provided
5. If you encounter a blocker, document it and stop — don't work around it silently
6. Write all work products to the appropriate directory (code to workspace, docs to scratch)
7. Post progress as session messages so the human can follow along

Token budget guidance:
- Read files only when needed. Use search to locate, then read specific ranges.
- Write incrementally — don't buffer everything until the end.
- If the implementation is large, break it into phases and complete each before starting the next.`

	agentCfg := ai.AgentConfig{
		Model:         ExecuteModel,
		SystemMessage: systemMsg,
		MCPServers:    p.mcpDefsForCategory(entry.Category),
		WorkingDir:    p.workspace,
		AgentName:     "execute",
		AllowedWritePaths: map[string][]string{
			"execute": {".", ".spec/scratch"}, // Execution can write broadly
		},
		TokenWarningThreshold: 200000,
		PremiumRequestCost:    1.0, // Sonnet
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)

	log.Printf("Execution starting for %s (%s)", entry.ID, entry.Title)

	response, err := agent.Ask(ctx, prompt)
	if err != nil {
		// Still track cost even on failure — the premium request was consumed
		if costErr := p.store.DB().IncrementPremiumRequests(entry.ID, agentCfg.PremiumRequestCost); costErr != nil {
			log.Printf("warning: failed to track cost for %s: %v", entry.ID, costErr)
		}
		log.Printf("Execution failed for %s: %v", entry.ID, err)
		p.store.DB().SetMaturity(entry.ID, "specced", fmt.Sprintf("Execution failed: %v", err))

		// Track failure count and escalate if needed
		count, countErr := p.store.DB().IncrementFailureCount(entry.ID, err.Error())
		if countErr != nil {
			log.Printf("warning: failed to increment failure count for %s: %v", entry.ID, countErr)
		}
		msg := fmt.Sprintf("Execution failed: %v\n\nEntry returned to specced. You can retry or revise the plan.", err)
		if count >= 3 {
			msg += fmt.Sprintf("\n\n🔴 This entry has failed %d consecutive times. Something structural may be wrong.", count)
		}
		p.store.DB().AddSessionMessage(entry.ID, "agent", msg)
		p.notify("entry.updated", entry.ID, map[string]string{"maturity": "specced"})
		p.notify("message.new", entry.ID, map[string]string{"role": "agent"})
		return
	}

	// Track premium request cost
	if err := p.store.DB().IncrementPremiumRequests(entry.ID, agentCfg.PremiumRequestCost); err != nil {
		log.Printf("warning: failed to track cost for %s: %v", entry.ID, err)
	}

	// Reset failure count on success
	p.store.DB().ResetFailureCount(entry.ID)

	log.Printf("Execution complete for %s (%d chars response)", entry.ID, len(response))

	// Store the execution output
	p.store.DB().SetAgentOutput(entry.ID, response, 0)

	// Post completion message with scenario reminder
	p.store.DB().AddSessionMessage(entry.ID, "agent",
		"Execution complete. Please verify the scenarios:\n\n"+entry.Scenarios+
			"\n\nUse the Verify button to mark each scenario as pass/fail.")

	// Set route_status to your_turn so it shows up for Michael
	p.store.DB().UpdateRouteStatus(entry.ID, "your_turn")

	p.notify("entry.updated", entry.ID, map[string]string{"maturity": "executing", "route_status": "your_turn"})
	p.notify("message.new", entry.ID, map[string]string{"role": "agent"})
}

// loadScratchContent reads the scratch file for an entry from disk.
func (p *Pipeline) loadScratchContent(entry *store.Entry) string {
	if entry.ScratchPath == "" {
		return ""
	}
	absPath := entry.ScratchPath
	if !filepath.IsAbs(absPath) && p.workspace != "" {
		absPath = filepath.Join(p.workspace, absPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	content := string(data)
	if len(content) > 10000 {
		content = content[:10000] + "\n...(truncated — full content in scratch file)"
	}
	return content
}

func buildExecutePrompt(entry *store.Entry, scratchContent, feedback, projectCtx string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Execute the following specced brain entry:\n\n")
	fmt.Fprintf(&sb, "**Title:** %s\n", entry.Title)
	fmt.Fprintf(&sb, "**Category:** %s\n", entry.Category)
	fmt.Fprintf(&sb, "**Content:** %s\n\n", entry.Body)

	if len(entry.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n\n", strings.Join(entry.Tags, ", "))
	}

	if projectCtx != "" {
		sb.WriteString(projectCtx)
		sb.WriteString("\n")
	}

	// Scenarios are the acceptance criteria
	fmt.Fprintf(&sb, "## Scenarios (Acceptance Criteria)\n\n")
	fmt.Fprintf(&sb, "%s\n\n", entry.Scenarios)
	fmt.Fprintf(&sb, "Your implementation must satisfy ALL of these scenarios. They will be verified by the human after you finish.\n\n")

	if scratchContent != "" {
		fmt.Fprintf(&sb, "## Research & Plan (from scratch file)\n\n")
		fmt.Fprintf(&sb, "```markdown\n%s\n```\n\n", scratchContent)
	}

	if feedback != "" {
		fmt.Fprintf(&sb, "## Human Guidance\n\n%s\n\n", feedback)
	}

	fmt.Fprintf(&sb, "## Instructions\n\n")
	fmt.Fprintf(&sb, "1. Read the plan and scenarios carefully\n")
	fmt.Fprintf(&sb, "2. Implement the plan — create files, write code, update configs as needed\n")
	fmt.Fprintf(&sb, "3. Verify each scenario is satisfied by your implementation\n")
	fmt.Fprintf(&sb, "4. Report what was done and any notes for the verification step\n")

	return sb.String()
}

// countScenarios counts the number of scenario lines (lines starting with - or numbered).
func countScenarios(scenarios string) int {
	count := 0
	for _, line := range strings.Split(scenarios, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "•") ||
			(len(line) > 1 && line[0] >= '1' && line[0] <= '9' && line[1] == '.') {
			count++
		}
	}
	if count == 0 {
		count = 1 // at least one if there's any text
	}
	return count
}
