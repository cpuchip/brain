package store

import (
	"time"
)

// Entry is a classified thought stored in SQLite with optional vector embedding.
type Entry struct {
	// Identity
	ID       string    `json:"id" yaml:"id,omitempty"`
	Title    string    `json:"title" yaml:"title"`
	Category string    `json:"category" yaml:"category"`
	Created  time.Time `json:"created_at" yaml:"created"`
	Updated  time.Time `json:"updated_at" yaml:"updated"`
	Tags     []string  `json:"tags,omitempty" yaml:"tags,omitempty"`

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
