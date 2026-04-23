package steward

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

// CommissionRunner extends PipelineRetrier with gate evaluation capabilities
// needed for commissioned work. The pipeline implements this interface.
type CommissionRunner interface {
	PipelineRetrier

	// EvaluateGate evaluates the output of a pipeline stage and recommends next action.
	// Returns action ("advance", "revise", "surface"), reasoning, and feedback (for revise).
	EvaluateGate(ctx context.Context, entryID, model string) (action, reasoning, feedback string, err error)

	// GenerateScenarios creates testable acceptance criteria for a planned entry.
	GenerateScenarios(ctx context.Context, entryID, model string) ([]string, error)

	// EvaluateAndVerify checks execution output against scenarios and verifies the entry.
	// Returns whether all scenarios passed and reasoning.
	EvaluateAndVerify(ctx context.Context, entryID, model string) (passed bool, reasoning string, err error)
}

// commissionState tracks running commissions.
type commissionState struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc // commission ID → cancel func
	runner  CommissionRunner
}

// SetCommissionRunner configures the pipeline operator for commissions.
func (s *Steward) SetCommissionRunner(r CommissionRunner) {
	s.commission.mu.Lock()
	s.commission.runner = r
	s.commission.mu.Unlock()
}

// CreateCommission creates and starts a commission for a single entry.
func (s *Steward) CreateCommission(entryID, intent, authority, model string, maxCost float64) (*store.Commission, error) {
	// Validate entry exists
	entry, err := s.store.DB().GetEntry(entryID)
	if err != nil {
		return nil, fmt.Errorf("entry not found: %w", err)
	}

	// Check for existing active commission on this entry
	existing, _ := s.store.DB().GetActiveCommissionForEntry(entryID)
	if existing != nil {
		return nil, fmt.Errorf("entry already has an active commission: %s", existing.ID)
	}

	if authority == "" {
		authority = "advance_and_execute"
	}
	if model == "" {
		model = "claude-opus-4.7"
	}
	if maxCost <= 0 {
		maxCost = 50.0
	}

	c := &store.Commission{
		EntryID:   entryID,
		ProjectID: entry.ProjectID,
		Intent:    intent,
		Scope:     "single",
		Authority: authority,
		Model:     model,
		MaxCost:   maxCost,
		Status:    "active",
	}

	if err := s.store.DB().CreateCommission(c); err != nil {
		return nil, fmt.Errorf("create commission: %w", err)
	}

	// Post session message about the commission
	msg := fmt.Sprintf("📜 **Steward: Commission received** — shepherding this entry from %s to verified.\n\n"+
		"**Intent:** %s\n"+
		"**Model:** %s\n"+
		"**Budget:** %.1f premium requests\n"+
		"**Authority:** %s\n\n"+
		"The steward will advance through each stage, making judgment calls at every gate.",
		entry.Maturity, intent, model, maxCost, authority)
	s.store.DB().AddSessionMessage(entryID, "system", msg)
	s.notify("message.new", entryID, nil)

	s.recordAction(Action{
		EntryID:    entryID,
		Timestamp:  time.Now(),
		ActionType: "commission_start",
		Notes:      fmt.Sprintf("Commission %s started: %s → verified (budget: %.1f)", c.ID, entry.Maturity, maxCost),
	})

	// Start the commission goroutine
	go s.runCommission(c.ID)

	return c, nil
}

// GetCommission returns a commission by ID with its decisions.
func (s *Steward) GetCommission(id string) (*store.Commission, error) {
	return s.store.DB().GetCommission(id)
}

// ListCommissions returns all commissions.
func (s *Steward) ListCommissions() ([]*store.Commission, error) {
	return s.store.DB().ListCommissions()
}

// PauseCommission pauses a running commission.
func (s *Steward) PauseCommission(id string) error {
	c, err := s.store.DB().GetCommission(id)
	if err != nil {
		return err
	}
	if c.Status != "active" {
		return fmt.Errorf("commission is %s, not active", c.Status)
	}
	if err := s.store.DB().UpdateCommissionStatus(id, "paused"); err != nil {
		return err
	}

	s.store.DB().AddSessionMessage(c.EntryID, "system", "⏸️ **Steward: Commission paused** by user.")
	s.notify("message.new", c.EntryID, nil)

	s.recordAction(Action{
		EntryID:    c.EntryID,
		Timestamp:  time.Now(),
		ActionType: "commission_pause",
		Notes:      fmt.Sprintf("Commission %s paused", id),
	})
	log.Printf("steward: commission %s paused", id)
	return nil
}

// ResumeCommission resumes a paused commission.
func (s *Steward) ResumeCommission(id string) error {
	c, err := s.store.DB().GetCommission(id)
	if err != nil {
		return err
	}
	if c.Status != "paused" {
		return fmt.Errorf("commission is %s, not paused", c.Status)
	}
	if err := s.store.DB().UpdateCommissionStatus(id, "active"); err != nil {
		return err
	}

	s.store.DB().AddSessionMessage(c.EntryID, "system", "▶️ **Steward: Commission resumed** — continuing from where we left off.")
	s.notify("message.new", c.EntryID, nil)

	s.recordAction(Action{
		EntryID:    c.EntryID,
		Timestamp:  time.Now(),
		ActionType: "commission_resume",
		Notes:      fmt.Sprintf("Commission %s resumed", id),
	})

	// Restart the goroutine
	go s.runCommission(id)

	log.Printf("steward: commission %s resumed", id)
	return nil
}

// RevokeCommission cancels a commission and stops its goroutine.
func (s *Steward) RevokeCommission(id string) error {
	c, err := s.store.DB().GetCommission(id)
	if err != nil {
		return err
	}
	if c.Status != "active" && c.Status != "paused" {
		return fmt.Errorf("commission is %s, cannot revoke", c.Status)
	}

	if err := s.store.DB().UpdateCommissionStatus(id, "revoked"); err != nil {
		return err
	}

	// Cancel the goroutine if running
	s.commission.mu.Lock()
	if cancel, ok := s.commission.running[id]; ok {
		cancel()
		delete(s.commission.running, id)
	}
	s.commission.mu.Unlock()

	s.store.DB().AddSessionMessage(c.EntryID, "system",
		"🛑 **Steward: Commission revoked** — the steward has stood down. Entry is now under your control.")
	s.notify("message.new", c.EntryID, nil)
	s.notify("entry.updated", c.EntryID, nil)

	s.recordAction(Action{
		EntryID:    c.EntryID,
		Timestamp:  time.Now(),
		ActionType: "commission_revoke",
		Notes:      fmt.Sprintf("Commission %s revoked", id),
	})

	log.Printf("steward: commission %s revoked", id)
	return nil
}

// runCommission is the main commission goroutine. It shepherds a single entry
// from its current maturity through to verified, making gate decisions at
// each stage. This is the Ammon loop — faithful service earning trust.
func (s *Steward) runCommission(commissionID string) {
	// Register this goroutine
	ctx, cancel := context.WithCancel(s.ctx)
	s.commission.mu.Lock()
	if s.commission.running == nil {
		s.commission.running = make(map[string]context.CancelFunc)
	}
	s.commission.running[commissionID] = cancel
	runner := s.commission.runner
	s.commission.mu.Unlock()

	defer func() {
		cancel()
		s.commission.mu.Lock()
		delete(s.commission.running, commissionID)
		s.commission.mu.Unlock()
	}()

	if runner == nil {
		log.Printf("steward: commission %s cannot run — no CommissionRunner configured", commissionID)
		s.store.DB().UpdateCommissionStatus(commissionID, "failed")
		return
	}

	c, err := s.store.DB().GetCommission(commissionID)
	if err != nil {
		log.Printf("steward: commission %s not found: %v", commissionID, err)
		return
	}

	entryID := c.EntryID
	log.Printf("steward: commission %s starting for entry %s (intent: %s)", commissionID, entryID, c.Intent)

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			log.Printf("steward: commission %s cancelled", commissionID)
			return
		default:
		}

		// Re-read commission status (may have been paused/revoked)
		c, err = s.store.DB().GetCommission(commissionID)
		if err != nil {
			log.Printf("steward: commission %s read error: %v", commissionID, err)
			return
		}

		if c.Status == "paused" {
			log.Printf("steward: commission %s is paused — goroutine exiting (will restart on resume)", commissionID)
			return
		}
		if c.Status != "active" {
			log.Printf("steward: commission %s status is %s — stopping", commissionID, c.Status)
			return
		}

		// Cost check
		if c.CostUsed >= c.MaxCost {
			s.commissionFail(c, "budget_exceeded",
				fmt.Sprintf("Commission budget exhausted: %.1f/%.1f premium requests used.", c.CostUsed, c.MaxCost))
			return
		}

		// Read entry state
		entry, err := s.store.DB().GetEntry(entryID)
		if err != nil {
			log.Printf("steward: commission %s cannot read entry: %v", commissionID, err)
			s.commissionFail(c, "entry_error", fmt.Sprintf("Cannot read entry: %v", err))
			return
		}

		// Auto-reclassify non-pipeline entries (e.g. "inbox") so the pipeline can process them.
		if !classifier.PipelineCategories[entry.Category] {
			newCat := "ideas"
			log.Printf("steward: commission %s — entry category %q is not a pipeline category, reclassifying to %q", commissionID, entry.Category, newCat)
			if err := s.store.DB().Reclassify(entryID, newCat); err != nil {
				s.commissionFail(c, "reclassify_error", fmt.Sprintf("Cannot reclassify entry from %s to %s: %v", entry.Category, newCat, err))
				return
			}
			entry.Category = newCat
			s.notify("entry.updated", entryID, nil)
		}

		maturity := entry.Maturity
		if maturity == "" {
			maturity = "raw"
		}

		log.Printf("steward: commission %s — entry at %s", commissionID, maturity)

		// Stage dispatch
		var done bool
		switch maturity {
		case "raw":
			done, err = s.commissionAdvanceStage(ctx, c, entry, runner, "research")
		case "researched":
			done, err = s.commissionAdvanceStage(ctx, c, entry, runner, "plan")
		case "planned":
			done, err = s.commissionGenerateScenarios(ctx, c, entry, runner)
		case "specced":
			if c.Authority == "advance_only" {
				s.commissionSurface(c, "spec_ready",
					"Entry is specced with scenarios. Commission authority is advance_only — surfacing for execution decision.")
				return
			}
			done, err = s.commissionExecute(ctx, c, entry, runner)
		case "executing":
			done, err = s.commissionWaitForExecution(ctx, c, entry, runner)
		case "verified":
			s.commissionComplete(c)
			return
		default:
			s.commissionFail(c, "unknown_maturity",
				fmt.Sprintf("Unexpected maturity: %s", maturity))
			return
		}

		if err != nil {
			// Check if it's a surfacing (not a hard failure)
			if c.Status == "paused" {
				return
			}
			s.commissionFail(c, "stage_error", err.Error())
			return
		}

		if done {
			// This stage signalled completion (verified)
			return
		}

		// Brief pause between stages to avoid hammering
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// commissionAdvanceStage runs a pipeline stage (research or plan), then
// evaluates the gate to decide whether to advance, revise, or surface.
func (s *Steward) commissionAdvanceStage(ctx context.Context, c *store.Commission, entry *store.Entry, runner CommissionRunner, stage string) (done bool, err error) {
	entryID := entry.ID
	stageModel := s.modelForStage(c, stage)

	// Run the pipeline stage
	s.store.DB().AddSessionMessage(entryID, "system",
		fmt.Sprintf("📜 **Steward:** Running %s pass...", stage))
	s.notify("message.new", entryID, nil)

	advErr := runner.RetryAdvance(ctx, entryID, "", stageModel)
	if advErr != nil {
		// Record the failure as a decision
		s.recordDecision(c, entryID, stage, "fail", fmt.Sprintf("Stage failed: %v", advErr), 0, stageModel, "pipeline")
		return false, fmt.Errorf("%s stage failed: %w", stage, advErr)
	}

	// Track cost for the advance
	stageCost := s.modelCost(stageModel)
	s.addCommissionCost(c, stageCost)

	// Evaluate the gate: should we advance, revise, or surface?
	// Gate evaluation IS the steward's judgment call — uses commission's chosen model.
	action, reasoning, feedback, evalErr := runner.EvaluateGate(ctx, entryID, c.Model)
	if evalErr != nil {
		s.recordDecision(c, entryID, stage, "fail", fmt.Sprintf("Gate evaluation failed: %v", evalErr), stageCost, c.Model, "pipeline")
		return false, fmt.Errorf("gate evaluation failed: %w", evalErr)
	}

	// Track cost for the evaluation (steward judgment model)
	evalCost := s.modelCost(c.Model)
	s.addCommissionCost(c, evalCost)
	totalCost := stageCost + evalCost

	s.recordDecision(c, entryID, stage, action, reasoning, totalCost, c.Model, "eval")

	switch action {
	case "advance":
		s.store.DB().AddSessionMessage(entryID, "system",
			fmt.Sprintf("📜 **Steward:** %s gate → **advance** ✓\n\n%s", stage, reasoning))
		s.notify("message.new", entryID, nil)
		return false, nil // continue to next stage

	case "revise":
		// Loop cap — surface on third rejection.
		if c.RevisionCount >= 2 {
			s.commissionSurface(c, "loop_limit_exceeded",
				fmt.Sprintf("Verifier rejected the work %d times at %s gate. Surfacing for human review.\n\nLast feedback: %s",
					c.RevisionCount, stage, feedback))
			s.recordDecision(c, entryID, stage, "surface",
				fmt.Sprintf("Loop limit exceeded (%d revisions). Last feedback: %s", c.RevisionCount, feedback),
				0, c.Model, "eval")
			return false, nil
		}

		c.RevisionCount++
		if err := s.store.DB().UpdateCommissionRevisionCount(c.ID, c.RevisionCount); err != nil {
			log.Printf("steward: commission %s — failed to persist revision_count: %v", c.ID, err)
		}

		reviseModel := s.modelForStage(c, "revise")
		s.store.DB().AddSessionMessage(entryID, "system",
			fmt.Sprintf("📜 **Steward:** %s gate → **revise** 🔄 (%d/2)\n\n%s\n\nFeedback: %s", stage, c.RevisionCount, reasoning, feedback))
		s.notify("message.new", entryID, nil)

		// Run the revision
		revErr := runner.RetryAdvance(ctx, entryID, feedback, reviseModel)
		if revErr != nil {
			return false, fmt.Errorf("revision failed: %w", revErr)
		}
		revCost := s.modelCost(reviseModel)
		s.addCommissionCost(c, revCost)
		s.recordDecision(c, entryID, stage, "revise_complete",
			fmt.Sprintf("Revision %d/2 applied", c.RevisionCount), revCost, reviseModel, "pipeline")

		// After revision, re-evaluate (the next loop iteration will pick up the new state)
		return false, nil

	case "surface":
		s.commissionSurface(c, stage+"_concern", reasoning)
		return false, nil // goroutine will exit after surface

	default:
		return false, fmt.Errorf("unexpected gate action: %s", action)
	}
}

// commissionGenerateScenarios generates acceptance criteria and advances to specced.
func (s *Steward) commissionGenerateScenarios(ctx context.Context, c *store.Commission, entry *store.Entry, runner CommissionRunner) (done bool, err error) {
	entryID := entry.ID
	specModel := s.modelForStage(c, "spec")

	s.store.DB().AddSessionMessage(entryID, "system",
		"📜 **Steward:** Generating acceptance criteria (scenarios)...")
	s.notify("message.new", entryID, nil)

	scenarios, genErr := runner.GenerateScenarios(ctx, entryID, specModel)
	if genErr != nil {
		s.recordDecision(c, entryID, "spec", "fail", fmt.Sprintf("Scenario generation failed: %v", genErr), 0, specModel, "pipeline")
		return false, fmt.Errorf("scenario generation failed: %w", genErr)
	}

	scenCost := s.modelCost(specModel)
	s.addCommissionCost(c, scenCost)

	if len(scenarios) == 0 {
		s.recordDecision(c, entryID, "spec", "surface", "No scenarios generated — surfacing for human input", scenCost, specModel, "pipeline")
		s.commissionSurface(c, "no_scenarios", "The steward could not generate acceptance criteria. Please provide scenarios manually.")
		return false, nil
	}

	s.recordDecision(c, entryID, "spec", "advance",
		fmt.Sprintf("Generated %d scenarios and advanced to specced", len(scenarios)),
		scenCost, specModel, "pipeline")

	s.store.DB().AddSessionMessage(entryID, "system",
		fmt.Sprintf("📜 **Steward:** Generated %d scenarios → **specced** ✓", len(scenarios)))
	s.notify("message.new", entryID, nil)

	return false, nil
}

// commissionExecute kicks off execution for a specced entry.
func (s *Steward) commissionExecute(ctx context.Context, c *store.Commission, entry *store.Entry, runner CommissionRunner) (done bool, err error) {
	entryID := entry.ID
	execModel := s.modelForStage(c, "execute")

	s.store.DB().AddSessionMessage(entryID, "system",
		"📜 **Steward:** Starting execution...")
	s.notify("message.new", entryID, nil)

	execErr := runner.RetryExecute(ctx, entryID, "", execModel)
	if execErr != nil {
		s.recordDecision(c, entryID, "execute", "fail", fmt.Sprintf("Execution start failed: %v", execErr), 0, execModel, "pipeline")
		return false, fmt.Errorf("execution start failed: %w", execErr)
	}

	execCost := s.modelCost(execModel)
	s.addCommissionCost(c, execCost)

	s.recordDecision(c, entryID, "execute", "execute", "Execution started", execCost, execModel, "pipeline")

	// Don't return done — the next loop iteration will see maturity="executing"
	// and call commissionWaitForExecution
	return false, nil
}

// commissionWaitForExecution polls until execution completes, then evaluates.
func (s *Steward) commissionWaitForExecution(ctx context.Context, c *store.Commission, entry *store.Entry, runner CommissionRunner) (done bool, err error) {
	entryID := entry.ID
	verifyModel := s.modelForStage(c, "verify")

	// Poll until the entry is no longer executing
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(10 * time.Second):
		}

		current, readErr := s.store.DB().GetEntry(entryID)
		if readErr != nil {
			return false, fmt.Errorf("read entry during execution wait: %w", readErr)
		}

		// Still executing — keep waiting
		if current.Maturity == "executing" && current.RouteStatus == "agent" {
			continue
		}

		// Execution finished — check if it succeeded or failed
		if current.RouteStatus == "your_turn" && current.Maturity == "executing" {
			// Execution complete, waiting for verification
			break
		}

		// Maturity changed (e.g. back to planned due to failure)
		if current.Maturity != "executing" {
			s.recordDecision(c, entryID, "execute", "fail",
				fmt.Sprintf("Execution ended with maturity=%s (expected executing→your_turn)", current.Maturity), 0, s.modelForStage(c, "execute"), "pipeline")
			// Let the main loop re-evaluate from the new maturity
			return false, nil
		}

		break
	}

	// Evaluate the execution output (verify is hard-pinned to haiku)
	passed, reasoning, evalErr := runner.EvaluateAndVerify(ctx, entryID, verifyModel)
	if evalErr != nil {
		s.recordDecision(c, entryID, "verify", "fail", fmt.Sprintf("Verification evaluation failed: %v", evalErr), 0, verifyModel, "verify")
		return false, fmt.Errorf("verification evaluation failed: %w", evalErr)
	}

	verifyCost := s.modelCost(verifyModel)
	s.addCommissionCost(c, verifyCost)

	if passed {
		s.recordDecision(c, entryID, "verify", "complete", reasoning, verifyCost, verifyModel, "verify")
		s.store.DB().AddSessionMessage(entryID, "system",
			fmt.Sprintf("📜 **Steward:** Verification → **passed** ✓\n\n%s", reasoning))
		s.notify("message.new", entryID, nil)
		// Entry should now be verified — next loop iteration will call commissionComplete
		return false, nil
	}

	// Verification failed — apply same loop cap as gate-revise.
	if c.RevisionCount >= 2 {
		s.commissionSurface(c, "loop_limit_exceeded",
			fmt.Sprintf("Verifier rejected the execution %d times. Surfacing for human review.\n\nLast feedback: %s",
				c.RevisionCount, reasoning))
		s.recordDecision(c, entryID, "verify", "surface",
			fmt.Sprintf("Loop limit exceeded (%d revisions). Last feedback: %s", c.RevisionCount, reasoning),
			verifyCost, verifyModel, "verify")
		return false, nil
	}

	c.RevisionCount++
	if err := s.store.DB().UpdateCommissionRevisionCount(c.ID, c.RevisionCount); err != nil {
		log.Printf("steward: commission %s — failed to persist revision_count: %v", c.ID, err)
	}

	s.recordDecision(c, entryID, "verify", "revise", reasoning, verifyCost, verifyModel, "verify")
	s.store.DB().AddSessionMessage(entryID, "system",
		fmt.Sprintf("📜 **Steward:** Verification → **failed** (revision %d/2) — returning to planned.\n\n%s", c.RevisionCount, reasoning))
	s.notify("message.new", entryID, nil)
	// The EvaluateAndVerify should have already called Pipeline.Verify which
	// returns the entry to planned. The next loop iteration will pick that up.
	return false, nil
}

// commissionComplete marks a commission as successfully completed.
func (s *Steward) commissionComplete(c *store.Commission) {
	s.store.DB().UpdateCommissionStatus(c.ID, "completed")

	s.store.DB().AddSessionMessage(c.EntryID, "system",
		fmt.Sprintf("📜 **Steward: Commission complete** ✓\n\n"+
			"Entry has reached verified. Total cost: %.1f premium requests.\n"+
			"Decisions: %d gate evaluations.\n\n"+
			"The steward has rendered account.",
			c.CostUsed, len(c.Decisions)))
	s.notify("message.new", c.EntryID, nil)
	s.notify("entry.updated", c.EntryID, nil)

	s.recordAction(Action{
		EntryID:    c.EntryID,
		Timestamp:  time.Now(),
		ActionType: "commission_complete",
		Notes:      fmt.Sprintf("Commission %s completed — %.1f premium requests used", c.ID, c.CostUsed),
	})

	log.Printf("steward: commission %s completed for entry %s (cost: %.1f)", c.ID, c.EntryID, c.CostUsed)
}

// commissionFail marks a commission as failed.
func (s *Steward) commissionFail(c *store.Commission, reason, detail string) {
	s.store.DB().UpdateCommissionStatus(c.ID, "failed")

	msg := fmt.Sprintf("🛑 **Steward: Commission failed** — %s\n\n%s\n\n"+
		"Total cost: %.1f premium requests. The entry is now under your control.",
		reason, detail, c.CostUsed)
	s.store.DB().AddSessionMessage(c.EntryID, "system", msg)
	s.notify("message.new", c.EntryID, nil)
	s.notify("entry.updated", c.EntryID, nil)

	s.recordAction(Action{
		EntryID:    c.EntryID,
		Timestamp:  time.Now(),
		ActionType: "commission_fail",
		Notes:      fmt.Sprintf("Commission %s failed: %s — %s", c.ID, reason, detail),
	})

	log.Printf("steward: commission %s failed: %s", c.ID, reason)
}

// commissionSurface pauses the commission and surfaces a decision to the human.
func (s *Steward) commissionSurface(c *store.Commission, concern, detail string) {
	s.store.DB().UpdateCommissionStatus(c.ID, "paused")

	msg := fmt.Sprintf("🤚 **Steward: Surfacing for your input** — %s\n\n%s\n\n"+
		"The commission is paused. Resume it when you've provided direction.",
		concern, detail)
	s.store.DB().AddSessionMessage(c.EntryID, "system", msg)
	s.store.DB().UpdateRouteStatus(c.EntryID, "your_turn")
	s.notify("message.new", c.EntryID, nil)
	s.notify("entry.updated", c.EntryID, map[string]string{"route_status": "your_turn"})

	s.recordAction(Action{
		EntryID:    c.EntryID,
		Timestamp:  time.Now(),
		ActionType: "commission_surface",
		Notes:      fmt.Sprintf("Commission %s surfaced: %s", c.ID, concern),
	})

	log.Printf("steward: commission %s surfaced concern: %s", c.ID, concern)
}

// recordDecision persists a commission decision to the DB.
func (s *Steward) recordDecision(c *store.Commission, entryID, stage, action, reasoning string, cost float64, model, costType string) {
	dec := &store.CommissionDecision{
		CommissionID: c.ID,
		Timestamp:    time.Now(),
		EntryID:      entryID,
		Stage:        stage,
		Action:       action,
		Reasoning:    reasoning,
		Cost:         cost,
		Model:        model,
		CostType:     costType,
	}
	if err := s.store.DB().AddCommissionDecision(dec); err != nil {
		log.Printf("steward: failed to record decision for commission %s: %v", c.ID, err)
	}
}

// addCommissionCost increments the commission's cost tracker.
func (s *Steward) addCommissionCost(c *store.Commission, cost float64) {
	c.CostUsed += cost
	if err := s.store.DB().UpdateCommissionCost(c.ID, c.CostUsed); err != nil {
		log.Printf("steward: failed to update commission cost for %s: %v", c.ID, err)
	}
}

// modelCost returns the premium request cost for a model.
func (s *Steward) modelCost(model string) float64 {
	for _, tier := range s.cfg.EscalationChain {
		if tier.Model == model {
			return tier.Cost
		}
	}
	return 1.0 // default
}

// modelForStage returns the model to use for a given pipeline stage in a
// commission. Verify is hard-pinned to haiku regardless of catalog. Other
// stages use config.StageDefaults. The commission's Model field is reserved
// for steward judgment (gate evaluation) and is the fallback for unknown
// stages only.
func (s *Steward) modelForStage(c *store.Commission, stage string) string {
	if stage == "verify" {
		return "claude-haiku-4.5"
	}
	if m, ok := config.StageDefaults[stage]; ok {
		return m
	}
	return c.Model
}

// activeCommissionCount returns the number of currently running commission goroutines.
func (s *Steward) activeCommissionCount() int {
	s.commission.mu.Lock()
	defer s.commission.mu.Unlock()
	return len(s.commission.running)
}
