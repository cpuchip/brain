package store

import (
	"time"
)

// Project groups related entries under a named goal/workstream.
type Project struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"` // active, paused, archived
	Emoji       string    `json:"emoji,omitempty"`
	ContextFile string    `json:"context_file,omitempty"` // optional path to project-level context doc for agents
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Computed (not stored)
	EntryCount int `json:"entry_count,omitempty"`
}

// Entry is a classified thought stored in SQLite with optional vector embedding.
type Entry struct {
	// Identity
	ID       string    `json:"id" yaml:"id,omitempty"`
	Title    string    `json:"title" yaml:"title"`
	Category string    `json:"category" yaml:"category"`
	Created  time.Time `json:"created_at" yaml:"created"`
	Updated  time.Time `json:"updated_at" yaml:"updated"`
	Tags     []string  `json:"tags,omitempty" yaml:"tags,omitempty"`

	// Project association
	ProjectID *int `json:"project_id,omitempty" yaml:"project_id,omitempty"`

	// Classification
	Confidence  float64 `json:"confidence" yaml:"confidence"`
	NeedsReview bool    `json:"needs_review" yaml:"needs_review,omitempty"`
	Source      string  `json:"source" yaml:"source,omitempty"` // relay, discord, cli, web, app

	// Category-specific fields
	// People
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	Context   string `json:"context,omitempty" yaml:"context,omitempty"`
	FollowUps string `json:"follow_ups,omitempty" yaml:"follow_ups,omitempty"`

	// Projects
	Status     string `json:"status,omitempty" yaml:"status,omitempty"`
	NextAction string `json:"next_action,omitempty" yaml:"next_action,omitempty"`

	// Ideas
	OneLiner string `json:"one_liner,omitempty" yaml:"one_liner,omitempty"`

	// Actions
	DueDate    string `json:"due_date,omitempty" yaml:"due_date,omitempty"`
	ActionDone bool   `json:"action_done,omitempty" yaml:"done,omitempty"`

	// Study
	References string `json:"references,omitempty" yaml:"references,omitempty"`
	Insight    string `json:"insight,omitempty" yaml:"insight,omitempty"`

	// Journal
	Mood      string `json:"mood,omitempty" yaml:"mood,omitempty"`
	Gratitude string `json:"gratitude,omitempty" yaml:"gratitude,omitempty"`

	// Body (not in front matter — stored as markdown body for archive export)
	Body string `json:"body" yaml:"-"`

	// OriginalBody preserves the raw input text at creation time.
	// Never modified by classification — survives title/body rewrites.
	OriginalBody string `json:"original_body,omitempty" yaml:"-"`

	// Agent routing (populated post-classification)
	AgentRoute          string  `json:"agent_route,omitempty" yaml:"agent_route,omitempty"`
	RouteStatus         string  `json:"route_status,omitempty" yaml:"route_status,omitempty"`
	AgentOutput         string  `json:"agent_output,omitempty" yaml:"agent_output,omitempty"`
	TokensUsed          int64   `json:"tokens_used,omitempty" yaml:"tokens_used,omitempty"`
	PremiumRequestsUsed float64 `json:"premium_requests_used,omitempty" yaml:"premium_requests_used,omitempty"`

	// Pipeline maturity (populated post-classification for ideas/projects/study)
	Maturity        string `json:"maturity,omitempty" yaml:"maturity,omitempty"`                       // raw, researched, planned, specced, executing, verified
	MaturityUpdated string `json:"maturity_updated_at,omitempty" yaml:"maturity_updated_at,omitempty"` // RFC3339
	ScratchPath     string `json:"scratch_path,omitempty" yaml:"scratch_path,omitempty"`
	Scenarios       string `json:"scenarios,omitempty" yaml:"scenarios,omitempty"` // JSON array of testable scenarios
	MaturityNotes   string `json:"maturity_notes,omitempty" yaml:"maturity_notes,omitempty"`

	// Failure tracking (pipeline atonement)
	FailureCount      int    `json:"failure_count,omitempty" yaml:"failure_count,omitempty"`
	LastFailureReason string `json:"last_failure_reason,omitempty" yaml:"last_failure_reason,omitempty"`

	// Auto-continue mode (delegation vs sabbath path)
	AutoContinue bool `json:"auto_continue,omitempty" yaml:"auto_continue,omitempty"`

	// Sub-tasks (loaded separately from subtasks table)
	SubTasks []SubTask `json:"subtasks,omitempty" yaml:"subtasks,omitempty"`
}

// SubTask is a checkable item within an entry.
type SubTask struct {
	ID        string    `json:"id"`
	EntryID   string    `json:"entry_id"`
	Text      string    `json:"text"`
	Done      bool      `json:"done"`
	SortOrder int       `json:"sort_order"`
	Created   time.Time `json:"created_at"`
	Updated   time.Time `json:"updated_at"`
}

// SessionMessage records one turn in an iterative agent conversation.
type SessionMessage struct {
	ID        int       `json:"id"`
	EntryID   string    `json:"entry_id"`
	Role      string    `json:"role"` // "human" or "agent"
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditRecord logs what the brain did with a capture.
type AuditRecord struct {
	Timestamp   time.Time `json:"timestamp" yaml:"timestamp"`
	RawText     string    `json:"raw_text" yaml:"raw_text"`
	Category    string    `json:"category" yaml:"category"`
	Title       string    `json:"title" yaml:"title"`
	Confidence  float64   `json:"confidence" yaml:"confidence"`
	NeedsReview bool      `json:"needs_review" yaml:"needs_review"`
	Source      string    `json:"source" yaml:"source,omitempty"`
	FilePath    string    `json:"file_path,omitempty" yaml:"file_path"`
	Tags        []string  `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// ScheduledTask defines a recurring brain task.
type ScheduledTask struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Schedule    string     `json:"schedule"` // cron-like: "daily", "weekly:monday", "hourly", or cron expr
	ProjectID   *int       `json:"project_id,omitempty"`
	AgentName   string     `json:"agent_name"` // which agent handles this
	Prompt      string     `json:"prompt"`     // what to do
	Status      string     `json:"status"`     // active, paused
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TaskRun records a single execution of a scheduled task.
type TaskRun struct {
	ID        int        `json:"id"`
	TaskID    int        `json:"task_id"`
	Status    string     `json:"status"`             // running, complete, failed
	EntryID   string     `json:"entry_id,omitempty"` // entry created by this run
	Output    string     `json:"output,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// ActivityEvent is a recent event for the dashboard activity feed.
type ActivityEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // entry_created, entry_routed, agent_completed, task_run, project_created
	Title     string    `json:"title"`
	EntryID   string    `json:"entry_id,omitempty"`
	ProjectID *int      `json:"project_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
