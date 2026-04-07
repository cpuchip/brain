package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/store"
)

// ReviewStatus holds observable state of the nudge bot for the API.
type ReviewStatus struct {
	Enabled        bool      `json:"enabled"`
	Paused         bool      `json:"paused"`
	WakeHours      []int     `json:"wake_hours"`
	LastRunAt      time.Time `json:"last_run_at,omitempty"`
	NextRunAt      time.Time `json:"next_run_at,omitempty"`
	LastNudgeCount int       `json:"last_nudge_count"`
	TotalNudges    int       `json:"total_nudges"`
	TotalCost      float64   `json:"total_cost"`
	UserPresent    bool      `json:"user_present"`
}

// reviewState tracks mutable nudge bot state (mutex-protected).
type reviewState struct {
	mu             sync.Mutex
	enabled        bool
	paused         bool
	wakeHours      []int
	lastRunAt      time.Time
	nextRunAt      time.Time
	lastNudgeCount int
	totalNudges    int
	totalCost      float64
	lastActivityAt time.Time // last user API activity
}

// ReviewConfig holds thresholds for the push-back review loop.
type ReviewConfig struct {
	RawStaleAfter        time.Duration // how long before a raw entry is considered stale
	ResearchedStaleAfter time.Duration // how long before a researched entry is stale
	CompleteStaleAfter   time.Duration // how long before a complete (agent-done) entry is stale
	WakeHours            []int         // hours of day (local time) to run scans (e.g. 7, 11, 15, 19)
	Enabled              bool
}

// DefaultReviewConfig returns sensible defaults.
func DefaultReviewConfig() ReviewConfig {
	return ReviewConfig{
		RawStaleAfter:        24 * time.Hour,
		ResearchedStaleAfter: 48 * time.Hour,
		CompleteStaleAfter:   24 * time.Hour,
		WakeHours:            []int{7, 11, 15, 19},
		Enabled:              true,
	}
}

// nextWakeTime returns the next scheduled scan time based on WakeHours.
func nextWakeTime(now time.Time, hours []int) time.Time {
	for _, h := range hours {
		candidate := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if candidate.After(now) {
			return candidate
		}
	}
	// All today's hours have passed — first hour tomorrow
	tomorrow := now.AddDate(0, 0, 1)
	return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), hours[0], 0, 0, 0, now.Location())
}

// StartReviewLoop launches a background goroutine that scans for stale pipeline
// entries at fixed waking hours and posts AI-generated nudge questions.
func (p *Pipeline) StartReviewLoop(cfg ReviewConfig) {
	if !cfg.Enabled || p.pool == nil {
		log.Printf("Pipeline review loop: disabled (enabled=%v, pool=%v)", cfg.Enabled, p.pool != nil)
		return
	}
	if len(cfg.WakeHours) == 0 {
		log.Printf("Pipeline review loop: no wake hours configured, disabling")
		return
	}

	// Initialize review state
	p.review.mu.Lock()
	p.review.enabled = true
	p.review.wakeHours = cfg.WakeHours
	p.review.lastActivityAt = time.Now() // assume user is present at startup
	p.review.mu.Unlock()

	go func() {
		for {
			next := nextWakeTime(time.Now(), cfg.WakeHours)
			delay := time.Until(next)

			p.review.mu.Lock()
			p.review.nextRunAt = next
			p.review.mu.Unlock()

			log.Printf("Pipeline review loop: next scan at %s (in %v)", next.Format("15:04"), delay.Round(time.Minute))

			timer := time.NewTimer(delay)
			select {
			case <-p.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				p.runReviewScan(cfg)
			}
		}
	}()

	log.Printf("Pipeline review loop: started (wake hours: %v)", cfg.WakeHours)
}

// GetReviewStatus returns current nudge bot state for the API.
func (p *Pipeline) GetReviewStatus() ReviewStatus {
	p.review.mu.Lock()
	defer p.review.mu.Unlock()
	return ReviewStatus{
		Enabled:        p.review.enabled,
		Paused:         p.review.paused,
		WakeHours:      p.review.wakeHours,
		LastRunAt:      p.review.lastRunAt,
		NextRunAt:      p.review.nextRunAt,
		LastNudgeCount: p.review.lastNudgeCount,
		TotalNudges:    p.review.totalNudges,
		TotalCost:      p.review.totalCost,
		UserPresent:    time.Since(p.review.lastActivityAt) < 2*time.Hour,
	}
}

// SetReviewPaused pauses or resumes the nudge bot.
func (p *Pipeline) SetReviewPaused(paused bool) {
	p.review.mu.Lock()
	p.review.paused = paused
	p.review.mu.Unlock()
	if paused {
		log.Printf("Pipeline review: paused by user")
	} else {
		log.Printf("Pipeline review: resumed by user")
	}
}

// TouchActivity records that the user made an API request (for presence detection).
func (p *Pipeline) TouchActivity() {
	p.review.mu.Lock()
	p.review.lastActivityAt = time.Now()
	p.review.mu.Unlock()
}

func (p *Pipeline) runReviewScan(cfg ReviewConfig) {
	p.review.mu.Lock()
	paused := p.review.paused
	lastActivity := p.review.lastActivityAt
	p.review.mu.Unlock()

	if paused {
		log.Printf("Pipeline review: skipping scan (paused)")
		return
	}

	// Presence check: skip if no API activity in 2 hours
	if time.Since(lastActivity) > 2*time.Hour {
		log.Printf("Pipeline review: skipping scan (no user activity for %v)", time.Since(lastActivity).Round(time.Minute))
		return
	}

	now := time.Now().UTC()

	entries, err := p.store.DB().ListStaleEntries(
		now.Add(-cfg.RawStaleAfter),
		now.Add(-cfg.ResearchedStaleAfter),
		now.Add(-cfg.CompleteStaleAfter),
	)
	if err != nil {
		log.Printf("Pipeline review: scan error: %v", err)
		return
	}

	nudgeCount := 0
	if len(entries) > 0 {
		log.Printf("Pipeline review: found %d stale entries to nudge", len(entries))

		for _, entry := range entries {
			if err := p.nudgeEntry(entry); err != nil {
				log.Printf("Pipeline review: nudge failed for %s: %v", entry.ID, err)
			} else {
				nudgeCount++
			}
		}
	}

	// Update stats
	p.review.mu.Lock()
	p.review.lastRunAt = time.Now()
	p.review.lastNudgeCount = nudgeCount
	p.review.totalNudges += nudgeCount
	p.review.totalCost += float64(nudgeCount) * 0.33 // Haiku cost per nudge
	p.review.mu.Unlock()
}

func (p *Pipeline) nudgeEntry(entry *store.Entry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}

	// Load scratch file content if it exists
	scratchContent := ""
	if entry.ScratchPath != "" && p.workspace != "" {
		absPath := filepath.Join(p.workspace, entry.ScratchPath)
		if data, err := os.ReadFile(absPath); err == nil {
			s := string(data)
			if len(s) > 2000 {
				s = s[:2000] + "\n...(truncated)"
			}
			scratchContent = s
		}
	}

	// Load recent session messages for context
	msgs, _ := p.store.DB().ListSessionMessages(entry.ID)
	var conversationCtx string
	if len(msgs) > 0 {
		start := len(msgs) - 3
		if start < 0 {
			start = 0
		}
		for _, m := range msgs[start:] {
			conversationCtx += fmt.Sprintf("[%s]: %s\n", m.Role, m.Content)
		}
	}

	prompt := buildNudgePrompt(entry, maturity, scratchContent, conversationCtx, FormatProjectContext(p.BuildProjectContext(entry)))

	// Build system message with base instructions + review covenant
	systemMsg := p.buildNudgeSystemMessage()

	// Use cheap model — this is a nudge, not deep work
	agentCfg := ai.AgentConfig{
		Model:         ResearchModel, // Haiku — 0.33 premium requests
		SystemMessage: systemMsg,
		WorkingDir:    p.workspace,
		AgentName:     "review",
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)

	response, err := agent.Ask(ctx, prompt)
	if err != nil {
		return fmt.Errorf("review agent failed: %w", err)
	}

	// Track premium request cost (Haiku = 0.33)
	if err := p.store.DB().IncrementPremiumRequests(entry.ID, 0.33); err != nil {
		log.Printf("warning: failed to track cost for %s: %v", entry.ID, err)
	}

	// Increment nudge count
	if err := p.store.DB().IncrementNudgeCount(entry.ID); err != nil {
		log.Printf("warning: failed to increment nudge count for %s: %v", entry.ID, err)
	}

	// Post the nudge as a session message
	if _, err := p.store.DB().AddSessionMessage(entry.ID, "agent", response); err != nil {
		return fmt.Errorf("posting nudge message: %w", err)
	}

	// Set route_status to your_turn so it shows up in the dashboard
	// Mark agent_route as "review" so frontend can distinguish AI push-back from normal routing
	if err := p.store.DB().SetAgentRoute(entry.ID, "review", "your_turn"); err != nil {
		return fmt.Errorf("setting agent_route: %w", err)
	}

	p.notify("message.new", entry.ID, map[string]string{"role": "agent"})
	p.notify("entry.updated", entry.ID, map[string]string{"route_status": "your_turn"})

	log.Printf("Pipeline review: nudged %s (%s, %s maturity)", entry.ID, entry.Title, maturity)
	return nil
}

func buildNudgePrompt(entry *store.Entry, maturity, scratchContent, conversationCtx, projectCtx string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Entry: %s\n", entry.Title))
	sb.WriteString(fmt.Sprintf("Category: %s\n", entry.Category))
	sb.WriteString(fmt.Sprintf("Maturity: %s\n", maturity))
	sb.WriteString(fmt.Sprintf("Body:\n%s\n", entry.Body))

	if projectCtx != "" {
		sb.WriteString(projectCtx)
	}

	if scratchContent != "" {
		sb.WriteString(fmt.Sprintf("\nResearch notes:\n%s\n", scratchContent))
	}

	if conversationCtx != "" {
		sb.WriteString(fmt.Sprintf("\nRecent conversation:\n%s\n", conversationCtx))
	}

	switch maturity {
	case "raw":
		sb.WriteString("\nThis entry is raw and hasn't been touched in over 24 hours. Generate 2-3 specific questions that would help clarify this entry enough to run a research pass. Focus on: what's the actual goal? what would success look like? what's the scope?")
	case "researched":
		sb.WriteString("\nThis entry has been researched but hasn't progressed in 48+ hours. Based on the research notes, generate 2-3 specific questions or suggestions that would help move it to a plan. Focus on: what decisions are needed? what's blocking progress? what's the next concrete step?")
	default:
		sb.WriteString("\nThe agent finished working on this entry but the human hasn't reviewed it. Summarize what was done and ask 1-2 specific questions about what to do next.")
	}

	return sb.String()
}

// buildNudgeSystemMessage constructs the review agent's system prompt with
// base instructions (Layer 0) + review covenant (Layer 1) + nudge rules.
func (p *Pipeline) buildNudgeSystemMessage() string {
	msg := nudgeSystemPrompt + "\n\n"

	if baseInstr := p.loadBaseInstructions(); baseInstr != "" {
		msg += "## Workspace Context\n\n" + baseInstr + "\n\n---\n\n"
	}

	// Load review covenant
	govPath := filepath.Join(p.codeDir, "docs", "governance", "review-covenant.md")
	if data, err := os.ReadFile(govPath); err == nil {
		msg += "## Your Governance Covenant\n\n" + string(data) + "\n\n"
	}

	return msg
}

const nudgeSystemPrompt = `You are a project review assistant. Your job is to nudge stale brain entries forward by asking specific, actionable questions.

Rules:
1. Be brief — 2-4 sentences max, plus your questions
2. Be specific to THIS entry — generic questions waste time
3. Don't suggest solutions — ask questions that surface what the human already knows
4. Don't be patronizing or use filler phrases
5. Format as plain text, not markdown headers. Use numbered questions.
6. If there's research/scratch content, reference specific findings in your questions`
