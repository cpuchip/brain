// Package pipeline implements brain entry maturity pipeline operations:
// research passes, plan passes, and stage transitions.
package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

//go:generate echo "governance docs are loaded at runtime from docs/governance/"

// ResearchModel is the default cheap model used for research passes.
const ResearchModel = "claude-haiku-4.5"

// PlanModel is the mid-tier model used for plan passes (better reasoning for structure).
const PlanModel = "claude-sonnet-4"

// Pipeline orchestrates maturity transitions for brain entries.
// Notifier receives push events from the pipeline. Implemented by the web
// hub to broadcast over WebSocket.
type Notifier interface {
	Notify(eventType, entryID string, data any)
}

type Pipeline struct {
	store     *store.Store
	pool      *ai.AgentPool
	cfg       *config.Config
	wc        config.WorkspaceConfig
	notifier  Notifier
	codeDir   string // brain code dir (scripts/brain)
	workspace string // parent workspace root (scripture-study)
	ctx       context.Context
	cancel    context.CancelFunc
	review    reviewState
}

// SetNotifier configures a push notification sink (typically the WebSocket hub).
func (p *Pipeline) SetNotifier(n Notifier) {
	p.notifier = n
}

// notify sends a push event if a notifier is configured.
func (p *Pipeline) notify(eventType, entryID string, data any) {
	if p.notifier == nil {
		return
	}
	p.notifier.Notify(eventType, entryID, data)
}

// New creates a pipeline controller.
func New(st *store.Store, pool *ai.AgentPool, cfg *config.Config, wc config.WorkspaceConfig) *Pipeline {
	workspace := ""
	if cfg.BrainCodeDir != "" {
		scriptsDir := filepath.Dir(cfg.BrainCodeDir)
		workspace = filepath.Dir(scriptsDir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Pipeline{
		store:     st,
		pool:      pool,
		cfg:       cfg,
		wc:        wc,
		codeDir:   cfg.BrainCodeDir,
		workspace: workspace,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Stop cancels the pipeline's background goroutines (e.g., review loop).
func (p *Pipeline) Stop() {
	p.cancel()
}

// AdvanceAction is what to do with a pipeline entry.
type AdvanceAction string

const (
	ActionAdvance AdvanceAction = "advance"
	ActionRevise  AdvanceAction = "revise"
	ActionReject  AdvanceAction = "reject"
	ActionDefer   AdvanceAction = "defer"
)

// AdvanceRequest holds parameters for a pipeline advance operation.
type AdvanceRequest struct {
	EntryID   string        `json:"id"`
	Action    AdvanceAction `json:"action"`
	Feedback  string        `json:"feedback,omitempty"`  // human guidance for revision
	Scenarios []string      `json:"scenarios,omitempty"` // for specced stage
}

// AdvanceResult holds the outcome of an advance operation.
type AdvanceResult struct {
	EntryID     string `json:"id"`
	OldMaturity string `json:"old_maturity"`
	NewMaturity string `json:"new_maturity"`
	ScratchPath string `json:"scratch_path,omitempty"`
	Message     string `json:"message"`
}

// Advance processes a pipeline action for an entry.
func (p *Pipeline) Advance(ctx context.Context, req AdvanceRequest) (*AdvanceResult, error) {
	entry, err := p.store.DB().GetEntry(req.EntryID)
	if err != nil {
		return nil, fmt.Errorf("entry not found: %w", err)
	}

	if !classifier.PipelineCategories[entry.Category] {
		return nil, fmt.Errorf("entry %s (category: %s) is not a pipeline category", req.EntryID, entry.Category)
	}

	if entry.Notebook {
		return nil, fmt.Errorf("entry %s is in notebook mode — remove from notebook to use the pipeline", req.EntryID)
	}

	oldMaturity := entry.Maturity
	if oldMaturity == "" {
		oldMaturity = "raw"
	}

	switch req.Action {
	case ActionAdvance:
		result, err := p.advance(ctx, entry, oldMaturity, req)
		if err != nil {
			p.recordFailure(entry, oldMaturity, err)
			return nil, err
		}
		p.store.DB().ResetFailureCount(entry.ID)
		p.maybeAutoContinue(entry, result)
		return result, nil
	case ActionRevise:
		result, err := p.revise(ctx, entry, oldMaturity, req)
		if err != nil {
			p.recordFailure(entry, oldMaturity, err)
			return nil, err
		}
		p.store.DB().ResetFailureCount(entry.ID)
		p.maybeAutoContinue(entry, result)
		return result, nil
	case ActionReject:
		return p.reject(entry, oldMaturity)
	case ActionDefer:
		return p.deferEntry(entry, oldMaturity)
	default:
		return nil, fmt.Errorf("unknown action: %s", req.Action)
	}
}

func (p *Pipeline) advance(ctx context.Context, entry *store.Entry, oldMaturity string, req AdvanceRequest) (*AdvanceResult, error) {
	switch oldMaturity {
	case "raw":
		// raw → researched: run research pass
		return p.runResearch(ctx, entry, req.Feedback)
	case "researched":
		// researched → planned: run plan pass
		return p.runPlan(ctx, entry, req.Feedback)
	case "planned":
		// planned → specced: requires scenarios, generates proposal file
		if len(req.Scenarios) == 0 {
			return nil, fmt.Errorf("advancing to specced requires scenarios")
		}
		scenariosJSON := strings.Join(req.Scenarios, "\n- ")
		if err := p.store.DB().SetScenarios(entry.ID, "- "+scenariosJSON); err != nil {
			return nil, fmt.Errorf("setting scenarios: %w", err)
		}
		if err := p.store.DB().SetMaturity(entry.ID, "specced", ""); err != nil {
			return nil, fmt.Errorf("setting maturity: %w", err)
		}

		// Generate proposal file
		proposalPath := ""
		proposalMsg := ""
		pp, err := p.generateProposal(entry, req.Scenarios)
		if err != nil {
			log.Printf("warning: failed to generate proposal for %s: %v", entry.ID, err)
			proposalMsg = " (proposal generation failed — check logs)"
		} else {
			proposalPath = pp
			proposalMsg = fmt.Sprintf(" Proposal: %s", proposalPath)
		}

		return &AdvanceResult{
			EntryID:     entry.ID,
			OldMaturity: oldMaturity,
			NewMaturity: "specced",
			ScratchPath: proposalPath,
			Message:     fmt.Sprintf("Advanced to specced with %d scenarios.%s", len(req.Scenarios), proposalMsg),
		}, nil
	default:
		return nil, fmt.Errorf("cannot advance from %s — use the agent routing system for execution", oldMaturity)
	}
}

func (p *Pipeline) revise(ctx context.Context, entry *store.Entry, oldMaturity string, req AdvanceRequest) (*AdvanceResult, error) {
	if req.Feedback == "" {
		return nil, fmt.Errorf("revise requires feedback")
	}

	switch oldMaturity {
	case "researched":
		// Re-run research with feedback guidance
		return p.runResearch(ctx, entry, req.Feedback)
	case "planned":
		// Re-run plan with feedback guidance
		return p.runPlan(ctx, entry, req.Feedback)
	default:
		// Store the feedback as maturity notes
		notes := fmt.Sprintf("Revision requested: %s", req.Feedback)
		if err := p.store.DB().SetMaturity(entry.ID, oldMaturity, notes); err != nil {
			return nil, fmt.Errorf("setting maturity notes: %w", err)
		}
		return &AdvanceResult{
			EntryID:     entry.ID,
			OldMaturity: oldMaturity,
			NewMaturity: oldMaturity,
			Message:     "Revision feedback recorded",
		}, nil
	}
}

func (p *Pipeline) reject(entry *store.Entry, oldMaturity string) (*AdvanceResult, error) {
	if err := p.store.DB().SetMaturity(entry.ID, "raw", "Rejected — returned to raw"); err != nil {
		return nil, fmt.Errorf("setting maturity: %w", err)
	}
	return &AdvanceResult{
		EntryID:     entry.ID,
		OldMaturity: oldMaturity,
		NewMaturity: "raw",
		Message:     "Rejected — returned to raw",
	}, nil
}

func (p *Pipeline) deferEntry(entry *store.Entry, oldMaturity string) (*AdvanceResult, error) {
	notes := fmt.Sprintf("Deferred at %s on %s", oldMaturity, time.Now().Format("2006-01-02"))
	if err := p.store.DB().SetMaturity(entry.ID, oldMaturity, notes); err != nil {
		return nil, fmt.Errorf("setting maturity notes: %w", err)
	}
	return &AdvanceResult{
		EntryID:     entry.ID,
		OldMaturity: oldMaturity,
		NewMaturity: oldMaturity,
		Message:     "Deferred — will revisit later",
	}, nil
}

// maybeAutoContinue fires an auto-advance goroutine if the entry has auto_continue enabled
// and the new maturity is eligible for automatic advancement (researched or planned).
// Stops before specced — verification always requires human.
func (p *Pipeline) maybeAutoContinue(entry *store.Entry, result *AdvanceResult) {
	if !entry.AutoContinue {
		return
	}
	if result.NewMaturity != "researched" && result.NewMaturity != "planned" {
		return
	}
	go func() {
		time.Sleep(2 * time.Second) // Brief pause for WebSocket delivery
		ctx := context.Background()
		_, err := p.Advance(ctx, AdvanceRequest{EntryID: entry.ID, Action: ActionAdvance})
		if err != nil {
			log.Printf("auto-continue failed for %s: %v", entry.ID, err)
		}
	}()
}

// recordFailure tracks a pipeline failure: increments the counter, posts a session message,
// and escalates if the entry has failed too many times.
func (p *Pipeline) recordFailure(entry *store.Entry, stage string, err error) {
	reason := err.Error()
	count, countErr := p.store.DB().IncrementFailureCount(entry.ID, reason)
	if countErr != nil {
		log.Printf("warning: failed to increment failure count for %s: %v", entry.ID, countErr)
	}

	msg := fmt.Sprintf("⚠️ %s pass failed: %v\n\nYou can:\n- **Advance** to retry\n- **Revise** with feedback\n- **Reject** to start over\n- **Defer** to revisit later", stage, err)
	if count >= 3 {
		msg += fmt.Sprintf("\n\n🔴 This entry has failed %d consecutive times. Something structural may be wrong.", count)
	}

	p.store.DB().AddSessionMessage(entry.ID, "system", msg)
	p.notify("message.new", entry.ID, nil)
}

// runResearch executes the research pass for an entry.
func (p *Pipeline) runResearch(ctx context.Context, entry *store.Entry, feedback string) (*AdvanceResult, error) {
	if p.pool == nil {
		return nil, fmt.Errorf("agent pool not available — research pass requires Copilot SDK")
	}

	// Determine scratch file path
	slug := slugify(entry.Title)
	var scratchPath string
	if entry.Category == "study" {
		scratchPath = filepath.Join("study", ".scratch", slug+".md")
	} else {
		scratchPath = filepath.Join(".spec", "scratch", slug, "main.md")
	}

	// Load governance document
	govDoc := ""
	govPath := filepath.Join(p.codeDir, "docs", "governance", "research-covenant.md")
	if data, err := os.ReadFile(govPath); err == nil {
		govDoc = string(data)
	} else {
		log.Printf("warning: research governance doc not found at %s: %v", govPath, err)
	}

	// Build research prompt
	body := entry.Body
	if body == "" {
		body = entry.Title
	}

	absPath := scratchPath
	if p.workspace != "" {
		absPath = filepath.Join(p.workspace, scratchPath)
	}

	// Build project context if entry belongs to a project
	projectCtx := FormatProjectContext(p.BuildProjectContext(entry))

	prompt := buildResearchPrompt(entry, body, absPath, feedback, projectCtx)

	// Build system message: base instructions + governance doc + research instructions
	systemMsg := "You are a research assistant for the brain pipeline.\n\n"
	if baseInstr := p.loadBaseInstructions(); baseInstr != "" {
		systemMsg += "## Workspace Context\n\n" + baseInstr + "\n\n---\n\n"
	}
	if govDoc != "" {
		systemMsg += "## Your Governance Covenant\n\n" + govDoc + "\n\n---\n\n"
	}
	systemMsg += `Your job is to research the captured thought below and write a structured summary to a scratch file.

Rules:
1. Search internal workspace first (existing studies, proposals, brain entries, code)
2. Search external sources second (web, articles)
3. Write ALL findings to the scratch file path provided
4. Never decide or recommend — surface findings and questions
5. Label sources: [WORKSPACE], [WEB], [SYNTHESIS]
6. If you find nothing relevant, say so honestly

Token budget guidance:
- Be targeted in searches. Use specific keywords, not broad patterns.
- When reading files, request only the lines you need (use startLine/endLine).
- Prefer MCP tools (gospel_search, brain_search, web_search) over raw grep/glob when possible — they return focused results.
- Write findings to the scratch file incrementally as you discover them. Don't wait until the end.
- If you've found enough context on a subtopic, move on. Exhaustive coverage is less important than covering all sections.`

	// Create agent with cheap model and research-specific config
	agentCfg := ai.AgentConfig{
		Model:         ResearchModel,
		SystemMessage: systemMsg,
		MCPServers:    p.mcpDefsForCategory(entry.Category),
		WorkingDir:    p.workspace,
		AgentName:     "research",
		AllowedWritePaths: map[string][]string{
			"research": {"study/.scratch", ".spec/scratch"},
		},
		TokenWarningThreshold: 100000,
		PremiumRequestCost:    0.33, // Haiku 4.5
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)

	log.Printf("Research pass starting for %s (%s) → %s", entry.ID, entry.Title, scratchPath)

	response, err := agent.Ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("research agent failed: %w", err)
	}

	// Track premium request cost
	if err := p.store.DB().IncrementPremiumRequests(entry.ID, agentCfg.PremiumRequestCost); err != nil {
		log.Printf("warning: failed to track cost for %s: %v", entry.ID, err)
	}

	log.Printf("Research pass complete for %s (%d chars response)", entry.ID, len(response))

	// Update entry maturity and scratch path
	if err := p.store.DB().SetScratchPath(entry.ID, scratchPath); err != nil {
		log.Printf("warning: failed to set scratch path for %s: %v", entry.ID, err)
	}
	if err := p.store.DB().SetMaturity(entry.ID, "researched", ""); err != nil {
		return nil, fmt.Errorf("setting maturity: %w", err)
	}

	p.notify("entry.updated", entry.ID, map[string]string{"maturity": "researched"})

	// Build a richer message: summarize open questions from the scratch file
	message := fmt.Sprintf("Research pass complete. Findings at %s", scratchPath)
	if summary := extractQuestionSummary(absPath); summary != "" {
		message += "\n\n" + summary
	}

	// Sabbath path: pause for human review unless auto-continue is on
	if !entry.AutoContinue {
		p.store.DB().UpdateRouteStatus(entry.ID, "your_turn")
		p.store.DB().AddSessionMessage(entry.ID, "agent",
			"Research complete. Review the findings before I continue to planning.\n\n"+message)
		p.notify("entry.updated", entry.ID, map[string]string{"route_status": "your_turn"})
		p.notify("message.new", entry.ID, nil)
	}

	return &AdvanceResult{
		EntryID:     entry.ID,
		OldMaturity: entry.Maturity,
		NewMaturity: "researched",
		ScratchPath: scratchPath,
		Message:     message,
	}, nil
}

// extractQuestionSummary reads the scratch file and extracts the "Open Questions"
// section into a compact summary for the auto-advance message.
func extractQuestionSummary(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	var questions []string
	var categories []string
	inQuestions := false
	currentCategory := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect "Open Questions" heading (## or ###)
		if strings.Contains(strings.ToLower(trimmed), "open question") && strings.HasPrefix(trimmed, "#") {
			inQuestions = true
			continue
		}

		// Stop at the next heading of equal or higher level
		if inQuestions && strings.HasPrefix(trimmed, "## ") && !strings.Contains(strings.ToLower(trimmed), "open question") {
			break
		}

		if !inQuestions {
			continue
		}

		// Sub-category heading (### or **)
		if strings.HasPrefix(trimmed, "### ") || (strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**")) {
			cat := strings.TrimPrefix(trimmed, "### ")
			cat = strings.Trim(cat, "*")
			if cat != "" {
				currentCategory = cat
				if !containsString(categories, currentCategory) {
					categories = append(categories, currentCategory)
				}
			}
			continue
		}

		// Numbered question line
		if len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && strings.Contains(trimmed[:3], ".") {
			questions = append(questions, trimmed)
			if currentCategory != "" && !containsString(categories, currentCategory) {
				categories = append(categories, currentCategory)
			}
		}
	}

	if len(questions) == 0 {
		return ""
	}

	summary := fmt.Sprintf("**%d open questions** for you", len(questions))
	if len(categories) > 0 {
		summary += " about " + strings.Join(categories, ", ")
	}
	summary += ". Your answers will drive the planning phase."
	return summary
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// buildMCPDefs returns MCP server definitions from config for agent use.
// Includes all servers — use mcpDefsForCategory for a leaner set.
func (p *Pipeline) buildMCPDefs() map[string]ai.MCPDef {
	if p.cfg.MCPServers == nil {
		return nil
	}
	defs := make(map[string]ai.MCPDef)
	for name, def := range p.cfg.MCPServers {
		defs[name] = ai.MCPDef{
			Command: def.Command,
			Args:    def.Args,
			Env:     def.Env,
			Cwd:     def.Cwd,
		}
	}
	return defs
}

// mcpDefsForCategory returns a subset of MCP servers appropriate for the
// entry's category. Non-study entries skip gospel-mcp, gospel-vec,
// webster-mcp, and byu-citations to save ~30-50k tokens of tool definitions.
func (p *Pipeline) mcpDefsForCategory(category string) map[string]ai.MCPDef {
	if p.cfg.MCPServers == nil {
		return nil
	}

	// Core servers every research/plan agent needs
	core := map[string]bool{
		"becoming":   true, // ibeco.me tools (brain_search, brain_get, practices, etc.)
		"search-mcp": true, // web search
		"yt-mcp":     true, // youtube for tech research
	}

	// Study-related entries also get gospel tools
	studyExtras := map[string]bool{
		"gospel-mcp":    true,
		"gospel-vec":    true,
		"webster-mcp":   true,
		"byu-citations": true,
	}

	isStudy := category == "study"

	defs := make(map[string]ai.MCPDef)
	for name, def := range p.cfg.MCPServers {
		if core[name] || (isStudy && studyExtras[name]) {
			defs[name] = ai.MCPDef{
				Command: def.Command,
				Args:    def.Args,
				Env:     def.Env,
				Cwd:     def.Cwd,
			}
		}
	}
	return defs
}

// runPlan executes the plan pass for a researched entry.
func (p *Pipeline) runPlan(ctx context.Context, entry *store.Entry, feedback string) (*AdvanceResult, error) {
	if p.pool == nil {
		return nil, fmt.Errorf("agent pool not available — plan pass requires Copilot SDK")
	}

	// Determine scratch file path (same convention as research)
	slug := slugify(entry.Title)
	var scratchPath string
	if entry.ScratchPath != "" {
		scratchPath = entry.ScratchPath
	} else if entry.Category == "study" {
		scratchPath = filepath.Join("study", ".scratch", slug+".md")
	} else {
		scratchPath = filepath.Join(".spec", "scratch", slug, "main.md")
	}

	// Load existing scratch file contents (research findings)
	existingScratch := ""
	absPath := scratchPath
	if p.workspace != "" {
		absPath = filepath.Join(p.workspace, scratchPath)
	}
	if data, err := os.ReadFile(absPath); err == nil {
		existingScratch = string(data)
	}

	// Load governance document
	govDoc := ""
	govPath := filepath.Join(p.codeDir, "docs", "governance", "plan-covenant.md")
	if data, err := os.ReadFile(govPath); err == nil {
		govDoc = string(data)
	} else {
		log.Printf("warning: plan governance doc not found at %s: %v", govPath, err)
	}

	// Build plan prompt
	body := entry.Body
	if body == "" {
		body = entry.Title
	}

	prompt := buildPlanPrompt(entry, body, absPath, existingScratch, feedback, FormatProjectContext(p.BuildProjectContext(entry)))

	// Build system message: base instructions + governance doc + plan instructions
	systemMsg := "You are a plan architect for the brain pipeline.\n\n"
	if baseInstr := p.loadBaseInstructions(); baseInstr != "" {
		systemMsg += "## Workspace Context\n\n" + baseInstr + "\n\n---\n\n"
	}
	if govDoc != "" {
		systemMsg += "## Your Governance Covenant\n\n" + govDoc + "\n\n---\n\n"
	}
	systemMsg += `Your job is to take a researched brain entry and produce a structured plan with scenarios.

Rules:
1. Read the existing scratch file — it contains research findings. Build on them, don't redo them.
2. Produce concrete scenarios (testable acceptance criteria)
3. Identify decisions the human must make — present options, don't choose
4. Estimate scope in sessions (1 session = 2-4 hours of focused work)
5. Reference actual files, packages, and patterns from the workspace
6. APPEND your plan section to the scratch file — do not overwrite existing content
7. If the research is thin or missing, flag that as a blocker`

	// Create agent with Sonnet model and plan-specific config
	agentCfg := ai.AgentConfig{
		Model:         PlanModel,
		SystemMessage: systemMsg,
		MCPServers:    p.mcpDefsForCategory(entry.Category),
		WorkingDir:    p.workspace,
		AgentName:     "plan",
		AllowedWritePaths: map[string][]string{
			"plan": {".spec/scratch", ".spec/proposals", "study/.scratch"},
		},
		TokenWarningThreshold: 150000,
		PremiumRequestCost:    1.0, // Sonnet 4
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)

	log.Printf("Plan pass starting for %s (%s) → %s", entry.ID, entry.Title, scratchPath)

	response, err := agent.Ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("plan agent failed: %w", err)
	}

	// Track premium request cost
	if err := p.store.DB().IncrementPremiumRequests(entry.ID, agentCfg.PremiumRequestCost); err != nil {
		log.Printf("warning: failed to track cost for %s: %v", entry.ID, err)
	}

	log.Printf("Plan pass complete for %s (%d chars response)", entry.ID, len(response))

	// Update entry maturity and scratch path
	if entry.ScratchPath == "" {
		if err := p.store.DB().SetScratchPath(entry.ID, scratchPath); err != nil {
			log.Printf("warning: failed to set scratch path for %s: %v", entry.ID, err)
		}
	}
	if err := p.store.DB().SetMaturity(entry.ID, "planned", ""); err != nil {
		return nil, fmt.Errorf("setting maturity: %w", err)
	}

	p.notify("entry.updated", entry.ID, map[string]string{"maturity": "planned"})

	planMessage := fmt.Sprintf("Plan pass complete. Plan appended to %s", scratchPath)

	// Sabbath path: pause for human review unless auto-continue is on
	if !entry.AutoContinue {
		p.store.DB().UpdateRouteStatus(entry.ID, "your_turn")
		p.store.DB().AddSessionMessage(entry.ID, "agent",
			"Plan complete. Review before adding scenarios.\n\n"+planMessage)
		p.notify("entry.updated", entry.ID, map[string]string{"route_status": "your_turn"})
		p.notify("message.new", entry.ID, nil)
	}

	return &AdvanceResult{
		EntryID:     entry.ID,
		OldMaturity: entry.Maturity,
		NewMaturity: "planned",
		ScratchPath: scratchPath,
		Message:     planMessage,
	}, nil
}

func buildPlanPrompt(entry *store.Entry, body, scratchPath, existingScratch, feedback, projectCtx string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Create a structured plan for the following brain entry:\n\n")
	fmt.Fprintf(&sb, "**Title:** %s\n", entry.Title)
	fmt.Fprintf(&sb, "**Category:** %s\n", entry.Category)
	fmt.Fprintf(&sb, "**Content:** %s\n\n", body)

	if len(entry.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n\n", strings.Join(entry.Tags, ", "))
	}

	if projectCtx != "" {
		sb.WriteString(projectCtx)
		sb.WriteString("\n")
	}

	if existingScratch != "" {
		fmt.Fprintf(&sb, "**Existing scratch file (research findings):**\n\n```markdown\n%s\n```\n\n", existingScratch)
	} else {
		fmt.Fprintf(&sb, "**Warning:** No existing scratch file found. Research may not have been run, or it created no output. Flag this in your plan.\n\n")
	}

	if feedback != "" {
		fmt.Fprintf(&sb, "**Human guidance:** %s\n\n", feedback)
	}

	fmt.Fprintf(&sb, "APPEND your plan to this file: `%s`\n\n", scratchPath)
	fmt.Fprintf(&sb, "Your plan section must include:\n")
	fmt.Fprintf(&sb, "1. **Scope** — estimated sessions and complexity\n")
	fmt.Fprintf(&sb, "2. **What to Build** — concrete deliverables (packages, files, endpoints)\n")
	fmt.Fprintf(&sb, "3. **Phases** — ordered, each independently deliverable\n")
	fmt.Fprintf(&sb, "4. **Scenarios** — testable acceptance criteria (\"when X, then Y\")\n")
	fmt.Fprintf(&sb, "5. **Decisions Needed** — choice points with options and trade-offs\n")
	fmt.Fprintf(&sb, "6. **Risks** — what could go wrong\n")
	fmt.Fprintf(&sb, "7. **Dependencies** — what must exist first\n")

	return sb.String()
}

// generateProposal creates a proposal file from a specced entry with scenarios.
func (p *Pipeline) generateProposal(entry *store.Entry, scenarios []string) (string, error) {
	slug := slugify(entry.Title)
	proposalPath := filepath.Join(".spec", "proposals", slug+".md")

	absPath := proposalPath
	if p.workspace != "" {
		absPath = filepath.Join(p.workspace, proposalPath)
	}

	// Ensure directory exists
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating proposal directory: %w", err)
	}

	// Read scratch file if available
	scratchContent := ""
	if entry.ScratchPath != "" {
		scratchAbsPath := entry.ScratchPath
		if !filepath.IsAbs(scratchAbsPath) && p.workspace != "" {
			scratchAbsPath = filepath.Join(p.workspace, scratchAbsPath)
		}
		if data, err := os.ReadFile(scratchAbsPath); err == nil {
			scratchContent = string(data)
		}
	}

	// Build proposal content
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", entry.Title)
	fmt.Fprintf(&sb, "**Category:** %s\n", entry.Category)
	fmt.Fprintf(&sb, "**Status:** specced\n")
	fmt.Fprintf(&sb, "**Created:** %s\n", entry.Created.Format("2006-01-02"))
	fmt.Fprintf(&sb, "**Specced:** %s\n\n", time.Now().Format("2006-01-02"))

	if len(entry.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n\n", strings.Join(entry.Tags, ", "))
	}

	fmt.Fprintf(&sb, "---\n\n")
	fmt.Fprintf(&sb, "## Summary\n\n%s\n\n", entry.Body)

	fmt.Fprintf(&sb, "## Scenarios\n\n")
	for _, s := range scenarios {
		fmt.Fprintf(&sb, "- %s\n", s)
	}
	fmt.Fprintf(&sb, "\n")

	if scratchContent != "" {
		fmt.Fprintf(&sb, "---\n\n## Research & Plan\n\n")
		fmt.Fprintf(&sb, "*From scratch file: %s*\n\n", entry.ScratchPath)
		fmt.Fprintf(&sb, "%s\n", scratchContent)
	}

	if err := os.WriteFile(absPath, []byte(sb.String()), 0644); err != nil {
		return "", fmt.Errorf("writing proposal file: %w", err)
	}

	return proposalPath, nil
}

func buildResearchPrompt(entry *store.Entry, body, scratchPath, feedback, projectCtx string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Research the following captured thought:\n\n")
	fmt.Fprintf(&sb, "**Title:** %s\n", entry.Title)
	fmt.Fprintf(&sb, "**Category:** %s\n", entry.Category)
	fmt.Fprintf(&sb, "**Content:** %s\n\n", body)

	if len(entry.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n\n", strings.Join(entry.Tags, ", "))
	}

	if projectCtx != "" {
		sb.WriteString(projectCtx)
		sb.WriteString("\n")
	}

	if feedback != "" {
		fmt.Fprintf(&sb, "**Additional guidance from human:** %s\n\n", feedback)
	}

	fmt.Fprintf(&sb, "Write your findings to this file: `%s`\n\n", scratchPath)
	fmt.Fprintf(&sb, "IMPORTANT: Create this file with a skeleton FIRST (all 5 headings), then fill in sections as you find information. Do not wait until the end to write.\n\n")
	fmt.Fprintf(&sb, "Use this structure:\n")
	fmt.Fprintf(&sb, "1. **What This Is About** — 1-2 sentence summary\n")
	fmt.Fprintf(&sb, "2. **What Already Exists** — search workspace for related studies, proposals, brain entries\n")
	fmt.Fprintf(&sb, "3. **External Context** — web search for articles, tools, prior art\n")
	fmt.Fprintf(&sb, "4. **Open Questions** — what needs human input before this can move forward\n")
	fmt.Fprintf(&sb, "5. **Raw Sources** — links to everything referenced\n")

	return sb.String()
}

// slugify converts a title to a filesystem-safe slug.
func slugify(title string) string {
	s := strings.ToLower(title)
	s = regexp.MustCompile(`[^a-z0-9\s-]`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`[\s]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		// Don't end on a hyphen
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "untitled"
	}
	return s
}
