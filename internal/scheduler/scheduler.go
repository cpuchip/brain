// Package scheduler runs scheduled brain tasks on configured intervals.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/store"
)

// EntryCreator creates entries and routes them to agents. Implemented by web.Server.
type EntryCreator interface {
	CreateAndRouteEntry(title, body, category, agentName string, projectID *int) (string, error)
}

// Scheduler checks for due tasks and executes them.
type Scheduler struct {
	db      *store.DB
	creator EntryCreator
	ctx     context.Context
	cancel  context.CancelFunc
}

// New creates a scheduler.
func New(db *store.DB, creator EntryCreator) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db:      db,
		creator: creator,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start begins the scheduler loop, checking every minute for due tasks.
func (s *Scheduler) Start() {
	go s.loop()
	log.Println("Scheduler: started (checking every 60s)")
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.cancel()
}

func (s *Scheduler) loop() {
	// Initial check after a short delay
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.checkAndRun()
			timer.Reset(60 * time.Second)
		}
	}
}

func (s *Scheduler) checkAndRun() {
	tasks, err := s.db.ListActiveScheduledTasks()
	if err != nil {
		log.Printf("Scheduler: error listing tasks: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, task := range tasks {
		// If next_run_at is nil, compute it from schedule
		if task.NextRunAt == nil {
			next := computeNextRun(task.Schedule, now)
			if err := s.db.SetTaskLastRun(task.ID, now, next); err != nil {
				log.Printf("Scheduler: error initializing task %d next_run: %v", task.ID, err)
			}
			continue
		}

		// Is it due?
		if now.Before(*task.NextRunAt) {
			continue
		}

		s.runTask(task, now)
	}
}

func (s *Scheduler) runTask(task *store.ScheduledTask, now time.Time) {
	log.Printf("Scheduler: running task %d (%s)", task.ID, task.Name)

	run, err := s.db.CreateTaskRun(task.ID)
	if err != nil {
		log.Printf("Scheduler: error creating run for task %d: %v", task.ID, err)
		return
	}

	// Create an entry from the task prompt and route it to the agent
	title := fmt.Sprintf("[Scheduled] %s", task.Name)
	entryID, err := s.creator.CreateAndRouteEntry(title, task.Prompt, "ideas", task.AgentName, task.ProjectID)
	if err != nil {
		log.Printf("Scheduler: task %d failed: %v", task.ID, err)
		_ = s.db.CompleteTaskRun(run.ID, "failed", "", "", err.Error())
		return
	}

	_ = s.db.CompleteTaskRun(run.ID, "complete", entryID, "Entry created and routed", "")

	// Update next run
	next := computeNextRun(task.Schedule, now)
	_ = s.db.SetTaskLastRun(task.ID, now, next)

	log.Printf("Scheduler: task %d complete, entry %s created, next run at %s", task.ID, entryID, next.Format(time.RFC3339))
}

// computeNextRun calculates the next run time from a schedule string.
// Supported formats: "hourly", "daily", "daily:HH:MM", "weekly:dayname", "weekly:dayname:HH:MM"
func computeNextRun(schedule string, from time.Time) time.Time {
	parts := strings.Split(strings.ToLower(schedule), ":")

	switch parts[0] {
	case "hourly":
		return from.Add(1 * time.Hour)

	case "daily":
		next := from.Add(24 * time.Hour)
		if len(parts) >= 3 {
			// daily:HH:MM
			hour, min := parseHHMM(parts[1], parts[2])
			next = time.Date(from.Year(), from.Month(), from.Day(), hour, min, 0, 0, from.Location())
			if !next.After(from) {
				next = next.Add(24 * time.Hour)
			}
		}
		return next

	case "weekly":
		dayOffset := 7 // default: 7 days from now
		if len(parts) >= 2 {
			targetDay := parseDayOfWeek(parts[1])
			currentDay := from.Weekday()
			dayOffset = (int(targetDay) - int(currentDay) + 7) % 7
			if dayOffset == 0 {
				dayOffset = 7
			}
		}
		next := from.AddDate(0, 0, dayOffset)
		if len(parts) >= 4 {
			hour, min := parseHHMM(parts[2], parts[3])
			next = time.Date(next.Year(), next.Month(), next.Day(), hour, min, 0, 0, next.Location())
		} else {
			next = time.Date(next.Year(), next.Month(), next.Day(), 8, 0, 0, 0, next.Location())
		}
		return next

	default:
		// Fallback: daily
		return from.Add(24 * time.Hour)
	}
}

func parseHHMM(hStr, mStr string) (int, int) {
	h, m := 8, 0
	if v := parseInt(hStr); v >= 0 && v < 24 {
		h = v
	}
	if v := parseInt(mStr); v >= 0 && v < 60 {
		m = v
	}
	return h, m
}

func parseInt(s string) int {
	v := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			v = v*10 + int(c-'0')
		}
	}
	return v
}

func parseDayOfWeek(s string) time.Weekday {
	switch strings.ToLower(s) {
	case "sunday", "sun":
		return time.Sunday
	case "monday", "mon":
		return time.Monday
	case "tuesday", "tue":
		return time.Tuesday
	case "wednesday", "wed":
		return time.Wednesday
	case "thursday", "thu":
		return time.Thursday
	case "friday", "fri":
		return time.Friday
	case "saturday", "sat":
		return time.Saturday
	default:
		return time.Monday
	}
}
