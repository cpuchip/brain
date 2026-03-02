package store

import (
	"time"
)

// Entry is a classified thought stored as a markdown file with YAML front matter.
type Entry struct {
	// Front matter
	Title      string    `yaml:"title"`
	Category   string    `yaml:"category"`
	Created    time.Time `yaml:"created"`
	Updated    time.Time `yaml:"updated"`
	Tags       []string  `yaml:"tags,omitempty"`
	Confidence float64   `yaml:"confidence"`

	// Category-specific fields
	// People
	Name      string `yaml:"name,omitempty"`
	Context   string `yaml:"context,omitempty"`
	FollowUps string `yaml:"follow_ups,omitempty"`

	// Projects
	Status     string `yaml:"status,omitempty"`
	NextAction string `yaml:"next_action,omitempty"`

	// Ideas
	OneLiner string `yaml:"one_liner,omitempty"`

	// Actions
	DueDate    string `yaml:"due_date,omitempty"`
	ActionDone bool   `yaml:"done,omitempty"`

	// Study
	References string `yaml:"references,omitempty"`
	Insight    string `yaml:"insight,omitempty"`

	// Journal
	Mood      string `yaml:"mood,omitempty"`
	Gratitude string `yaml:"gratitude,omitempty"`

	// Review
	NeedsReview bool `yaml:"needs_review,omitempty"`

	// Body (not in front matter — stored as markdown body)
	Body string `yaml:"-"`
}

// AuditRecord logs what the brain did with a capture.
type AuditRecord struct {
	Timestamp   time.Time `yaml:"timestamp"`
	RawText     string    `yaml:"raw_text"`
	Category    string    `yaml:"category"`
	Title       string    `yaml:"title"`
	Confidence  float64   `yaml:"confidence"`
	NeedsReview bool      `yaml:"needs_review"`
	FilePath    string    `yaml:"file_path"`
	Tags        []string  `yaml:"tags,omitempty"`
}
