package classifier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cpuchip/brain/internal/ai"
)

// Result is the structured output from classification.
type Result struct {
	Category   string   `json:"category"`   // people, projects, ideas, actions, study, journal
	Confidence float64  `json:"confidence"` // 0.0 - 1.0
	Title      string   `json:"title"`      // Generated title for the entry
	Fields     Fields   `json:"fields"`     // Category-specific extracted fields
	Tags       []string `json:"tags"`       // Auto-generated tags
}

// Fields holds the category-specific extracted data.
type Fields struct {
	// People
	Name      string `json:"name,omitempty"`
	Context   string `json:"context,omitempty"`
	FollowUps string `json:"follow_ups,omitempty"`

	// Projects
	Status     string `json:"status,omitempty"` // active, waiting, blocked, someday, done
	NextAction string `json:"next_action,omitempty"`

	// Ideas
	OneLiner string `json:"one_liner,omitempty"`

	// Actions
	DueDate string `json:"due_date,omitempty"`

	// Study
	References string `json:"references,omitempty"`
	Insight    string `json:"insight,omitempty"`

	// Journal
	Mood      string `json:"mood,omitempty"`
	Gratitude string `json:"gratitude,omitempty"`

	// Shared
	Notes string `json:"notes,omitempty"`
}

// Classifier routes raw thoughts into categories using AI.
type Classifier struct {
	client    *ai.Client
	threshold float64
}

// New creates a new Classifier.
func New(client *ai.Client, confidenceThreshold float64) *Classifier {
	return &Classifier{
		client:    client,
		threshold: confidenceThreshold,
	}
}

// Classify takes raw text and returns a classification result.
func (c *Classifier) Classify(ctx context.Context, rawText string) (*Result, error) {
	messages := []ai.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: rawText},
	}

	respBytes, err := c.client.CompleteJSON(ctx, messages, 0.1)
	if err != nil {
		return nil, fmt.Errorf("classification failed: %w", err)
	}

	var result Result
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing classification result: %w", err)
	}

	// Validate category
	validCategories := map[string]bool{
		"people": true, "projects": true, "ideas": true,
		"actions": true, "study": true, "journal": true,
	}
	if !validCategories[result.Category] {
		result.Category = "inbox"
		result.Confidence = 0.0
	}

	return &result, nil
}

// NeedsReview returns true if the result is below the confidence threshold.
func (c *Classifier) NeedsReview(result *Result) bool {
	return result.Confidence < c.threshold
}

// Threshold returns the current confidence threshold.
func (c *Classifier) Threshold() float64 {
	return c.threshold
}

// FormatConfirmation generates the human-readable confirmation message.
func FormatConfirmation(result *Result, needsReview bool) string {
	if needsReview {
		return fmt.Sprintf(
			"I couldn't confidently classify this (confidence: %.0f%%).\n"+
				"Saved to **inbox** for review.\n"+
				"Reply with `fix: <category>` (people/projects/ideas/actions/study/journal) to reclassify.",
			result.Confidence*100,
		)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Filed as **%s**: %s", result.Category, result.Title))
	sb.WriteString(fmt.Sprintf(" (confidence: %.0f%%)", result.Confidence*100))

	if result.Fields.NextAction != "" {
		sb.WriteString(fmt.Sprintf("\nNext action: %s", result.Fields.NextAction))
	}

	if len(result.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("\nTags: %s", strings.Join(result.Tags, ", ")))
	}

	sb.WriteString("\nReply `fix: <category>` if I got it wrong.")
	return sb.String()
}

// AuditEntry creates a structured audit log entry for this classification.
func AuditEntry(rawText string, result *Result, needsReview bool) map[string]any {
	return map[string]any{
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"raw_text":     rawText,
		"category":     result.Category,
		"title":        result.Title,
		"confidence":   result.Confidence,
		"needs_review": needsReview,
		"tags":         result.Tags,
	}
}

const systemPrompt = `You are a JSON classification API. You receive raw text and return a JSON object. You NEVER return prose, explanations, suggestions, or conversation. You are NOT a chatbot. You are a classifier.

IMPORTANT: Even if the input asks a question, proposes an idea, or requests feedback — DO NOT answer it, discuss it, or expand on it. Classify it and return JSON.

CATEGORIES:
- people: About a person — relationship context, follow-ups, something someone said, a detail to remember about them
- projects: Active work with a status and next action — something being built, planned, or tracked over time
- ideas: A thought, insight, or concept to capture — not yet actionable, just worth remembering. Feature ideas, "what if" questions, and brainstorms go here.
- actions: A specific task or errand — something with a clear "done" state, possibly a deadline
- study: Scripture insight, spiritual impression, gospel learning, covenant commitment
- journal: Personal reflection, observation about life, mood, gratitude, becoming

RULES:
1. Return ONLY valid JSON. No markdown, no explanation, no extra text. No code fences.
2. Confidence is 0.0 to 1.0 — how sure you are about the category.
3. If confidence would be below 0.5, still classify but set confidence honestly.
4. Extract a concise title (3-8 words) that captures the essence.
5. Extract category-specific fields where available.
6. For projects, always try to extract a concrete next_action (executable, not vague).
7. For people, extract the person's name if mentioned.
8. For study, note any scripture references.
9. Generate 1-3 relevant tags.

JSON SCHEMA (return exactly this structure, nothing else):
{
  "category": "string",
  "confidence": 0.0,
  "title": "string",
  "fields": {
    "name": "string or omit",
    "context": "string or omit",
    "follow_ups": "string or omit",
    "status": "active|waiting|blocked|someday|done or omit",
    "next_action": "string or omit",
    "one_liner": "string or omit",
    "due_date": "YYYY-MM-DD or omit",
    "references": "string or omit",
    "insight": "string or omit",
    "mood": "string or omit",
    "gratitude": "string or omit",
    "notes": "string or omit"
  },
  "tags": ["string"]
}`
