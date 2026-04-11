package steward

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/cpuchip/brain/internal/store"
)

// Nudger can send AI-generated nudge questions to stale entries.
// Implemented by the pipeline package — the steward owns the scheduling
// loop but delegates actual nudge execution (which needs the agent pool).
type Nudger interface {
	NudgeEntry(entry *store.Entry) error
}

// NudgeConfig holds thresholds and schedule for stale-entry nudging.
type NudgeConfig struct {
	RawStaleAfter        time.Duration // raw entries older than this get nudged
	ResearchedStaleAfter time.Duration // researched entries older than this get nudged
	CompleteStaleAfter   time.Duration // agent-complete entries waiting for review
	WakeHours            []int         // local hours to run scans (e.g. 7, 11, 15, 19)
	Enabled              bool
	PresenceTimeout      time.Duration // skip nudges if no user activity within this window
}

// DefaultNudgeConfig returns sensible defaults matching the prior nudge bot.
func DefaultNudgeConfig() NudgeConfig {
	return NudgeConfig{
		RawStaleAfter:        24 * time.Hour,
		ResearchedStaleAfter: 48 * time.Hour,
		CompleteStaleAfter:   24 * time.Hour,
		WakeHours:            []int{7, 11, 15, 19},
		Enabled:              true,
		PresenceTimeout:      2 * time.Hour,
	}
}

// NudgeStatus holds observable state for the nudge bot API.
type NudgeStatus struct {
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

// nudgeState tracks mutable nudge state (mutex-protected).
type nudgeState struct {
	mu             sync.Mutex
	enabled        bool
	paused         bool
	wakeHours      []int
	lastRunAt      time.Time
	nextRunAt      time.Time
	lastNudgeCount int
	totalNudges    int
	totalCost      float64
	lastActivityAt time.Time
}

// SetNudger configures the nudge executor (typically the pipeline).
func (s *Steward) SetNudger(n Nudger) {
	s.nudger = n
}

// GetNudgeStatus returns current nudge bot state for the API.
func (s *Steward) GetNudgeStatus() NudgeStatus {
	s.nudge.mu.Lock()
	defer s.nudge.mu.Unlock()
	return NudgeStatus{
		Enabled:        s.nudge.enabled,
		Paused:         s.nudge.paused,
		WakeHours:      s.nudge.wakeHours,
		LastRunAt:      s.nudge.lastRunAt,
		NextRunAt:      s.nudge.nextRunAt,
		LastNudgeCount: s.nudge.lastNudgeCount,
		TotalNudges:    s.nudge.totalNudges,
		TotalCost:      s.nudge.totalCost,
		UserPresent:    time.Since(s.nudge.lastActivityAt) < s.nudgeCfg.PresenceTimeout,
	}
}

// SetNudgePaused pauses or resumes the nudge bot.
func (s *Steward) SetNudgePaused(paused bool) {
	s.nudge.mu.Lock()
	s.nudge.paused = paused
	s.nudge.mu.Unlock()
	if paused {
		log.Printf("steward: nudge bot paused by user")
	} else {
		log.Printf("steward: nudge bot resumed by user")
	}
}

// TouchActivity records that the user made an API request (presence detection).
func (s *Steward) TouchActivity() {
	s.nudge.mu.Lock()
	s.nudge.lastActivityAt = time.Now()
	s.nudge.mu.Unlock()
}

// StartWatchLoop launches the steward's unified background loop.
// It replaces the pipeline's separate review loop — one goroutine instead of two.
// The loop fires at configured wake hours and:
//   - Scans for stale entries and nudges them (absorbing the old nudge bot)
//   - (Future phases can add more periodic tasks here)
func (s *Steward) StartWatchLoop(cfg NudgeConfig) {
	if !cfg.Enabled || s.nudger == nil {
		log.Printf("steward: watch loop disabled (nudge_enabled=%v, nudger=%v)", cfg.Enabled, s.nudger != nil)
		return
	}
	if len(cfg.WakeHours) == 0 {
		log.Printf("steward: no wake hours configured, disabling watch loop")
		return
	}

	s.nudgeCfg = cfg

	// Initialize nudge state
	s.nudge.mu.Lock()
	s.nudge.enabled = true
	s.nudge.wakeHours = cfg.WakeHours
	s.nudge.lastActivityAt = time.Now() // assume user present at startup
	s.nudge.mu.Unlock()

	go func() {
		for {
			next := nextWakeTime(time.Now(), cfg.WakeHours)
			delay := time.Until(next)

			s.nudge.mu.Lock()
			s.nudge.nextRunAt = next
			s.nudge.mu.Unlock()

			log.Printf("steward: next watch scan at %s (in %v)", next.Format("15:04"), delay.Round(time.Minute))

			timer := time.NewTimer(delay)
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				s.runWatchScan()
			}
		}
	}()

	log.Printf("steward: watch loop started (wake hours: %v)", cfg.WakeHours)
}

// nextWakeTime returns the next scheduled scan time based on wake hours.
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

// runWatchScan is the steward's unified periodic scan.
func (s *Steward) runWatchScan() {
	s.nudge.mu.Lock()
	paused := s.nudge.paused
	lastActivity := s.nudge.lastActivityAt
	s.nudge.mu.Unlock()

	if paused {
		log.Printf("steward: skipping watch scan (paused)")
		return
	}

	// Presence check: skip if no API activity within the timeout window
	if time.Since(lastActivity) > s.nudgeCfg.PresenceTimeout {
		log.Printf("steward: skipping watch scan (no user activity for %v)", time.Since(lastActivity).Round(time.Minute))
		return
	}

	// --- Nudge stale entries ---
	now := time.Now().UTC()
	entries, err := s.store.DB().ListStaleEntries(
		now.Add(-s.nudgeCfg.RawStaleAfter),
		now.Add(-s.nudgeCfg.ResearchedStaleAfter),
		now.Add(-s.nudgeCfg.CompleteStaleAfter),
	)
	if err != nil {
		log.Printf("steward: stale entry scan error: %v", err)
		return
	}

	nudgeCount := 0
	if len(entries) > 0 {
		log.Printf("steward: found %d stale entries to nudge", len(entries))
		for _, entry := range entries {
			if err := s.nudger.NudgeEntry(entry); err != nil {
				log.Printf("steward: nudge failed for %s: %v", entry.ID, err)
			} else {
				nudgeCount++
			}
		}
	}

	// Update stats
	s.nudge.mu.Lock()
	s.nudge.lastRunAt = time.Now()
	s.nudge.lastNudgeCount = nudgeCount
	s.nudge.totalNudges += nudgeCount
	s.nudge.totalCost += float64(nudgeCount) * 0.33 // Haiku cost per nudge
	s.nudge.mu.Unlock()

	s.recordAction(Action{
		EntryID:    "",
		Timestamp:  time.Now(),
		ActionType: "watch_scan",
		Notes:      fmt.Sprintf("Scanned stale entries: %d found, %d nudged", len(entries), nudgeCount),
	})
}
