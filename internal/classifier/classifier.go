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
	Category   string   `json:"category"`            // people, projects, ideas, actions, study, journal
	Confidence float64  `json:"confidence"`          // 0.0 - 1.0
	Title      string   `json:"title"`               // Generated title for the entry
	Fields     Fields   `json:"fields"`              // Category-specific extracted fields
	Tags       []string `json:"tags"`                // Auto-generated tags
	SubItems   []string `json:"sub_items,omitempty"` // extracted list items for subtask creation
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
	client    ai.Completer
	threshold float64
}

// New creates a new Classifier. The client can be any AI backend (Copilot SDK, LM Studio, etc.).
func New(client ai.Completer, confidenceThreshold float64) *Classifier {
	return &Classifier{
		client:    client,
		threshold: confidenceThreshold,
	}
}

// ActiveProfile returns the model profile for the current AI backend model.
// Returns nil if no profile is registered for the active model.
func (c *Classifier) ActiveProfile() *ModelProfile {
	return LookupProfile(c.client.Model())
}

// ClassificationSchema returns the response_format JSON schema for structured output.
// Used by both the production classifier and the eval tool.
func ClassificationSchema() map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "classification",
			"strict": "true",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"category": map[string]any{
						"type": "string",
						"enum": []string{"people", "projects", "ideas", "actions", "study", "journal"},
					},
					"confidence": map[string]any{"type": "number"},
					"title":      map[string]any{"type": "string"},
					"fields": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":        map[string]any{"type": "string"},
							"context":     map[string]any{"type": "string"},
							"follow_ups":  map[string]any{"type": "string"},
							"status":      map[string]any{"type": "string"},
							"next_action": map[string]any{"type": "string"},
							"one_liner":   map[string]any{"type": "string"},
							"due_date":    map[string]any{"type": "string"},
							"references":  map[string]any{"type": "string"},
							"insight":     map[string]any{"type": "string"},
							"mood":        map[string]any{"type": "string"},
							"gratitude":   map[string]any{"type": "string"},
							"notes":       map[string]any{"type": "string"},
						},
					},
					"tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"sub_items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"category", "confidence", "title", "fields", "tags", "sub_items"},
			},
		},
	}
}

// Classify takes raw text and returns a classification result.
func (c *Classifier) Classify(ctx context.Context, rawText string) (*Result, error) {
	// Select prompt and temperature from the active model's profile
	prompt := systemPrompt
	temp := 0.1
	noThink := false
	structuredOutput := false
	if p := c.ActiveProfile(); p != nil {
		if p.ClassifyPrompt != "" {
			prompt = p.ClassifyPrompt
		}
		if p.Temperature > 0 {
			temp = p.Temperature
		}
		noThink = p.NoThink
		structuredOutput = p.StructuredOutput
	}

	// Disable thinking tokens for classification — thinking models waste
	// the token budget on <think> blocks we don't need for a JSON response.
	if noThink && !strings.Contains(prompt, "/no_think") {
		prompt += "\n/no_think"
	}

	messages := []ai.ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: rawText},
	}

	var respBytes []byte
	var err error
	if structuredOutput {
		respBytes, err = c.client.CompleteStructuredJSON(ctx, messages, temp, ClassificationSchema())
	} else {
		respBytes, err = c.client.CompleteJSON(ctx, messages, temp)
	}
	if err != nil {
		return nil, fmt.Errorf("classification failed: %w", err)
	}

	var result Result
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing classification result: %w", err)
	}

	// Post-process: rescue sub_items from model errors.
	// Small models sometimes nest sub_items inside "fields" or put list items
	// in follow_ups/references instead. Fix it here rather than relying on
	// prompt compliance from every model.
	rescueSubItems(&result, respBytes)

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

// rescueSubItems handles common model errors where list items end up in the
// wrong place. Small models (1.7B-3B) frequently:
//   - Nest sub_items inside the "fields" object
//   - Put list items in follow_ups as a comma/newline string
//   - Put scripture references in the references field instead of sub_items
//
// This runs after standard JSON unmarshalling as a fixup pass.
func rescueSubItems(result *Result, rawJSON []byte) {
	if len(result.SubItems) > 0 {
		return // already have sub_items, nothing to rescue
	}

	// Case 1: sub_items nested inside fields (model puts it in wrong level)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &raw); err == nil {
		if fieldsRaw, ok := raw["fields"]; ok {
			var fieldsMap map[string]json.RawMessage
			if err := json.Unmarshal(fieldsRaw, &fieldsMap); err == nil {
				if subRaw, ok := fieldsMap["sub_items"]; ok {
					var nested []string
					if err := json.Unmarshal(subRaw, &nested); err == nil && len(nested) > 0 {
						result.SubItems = nested
						return
					}
				}
			}
		}
	}

	// Case 2: follow_ups contains what looks like a list
	// (comma-separated or newline-separated with 3+ items)
	if items := splitListField(result.Fields.FollowUps); len(items) >= 3 {
		result.SubItems = items
		result.Fields.FollowUps = "" // clear since we moved it
		return
	}

	// Case 3: references contains a list of scripture references
	// (comma-separated with 3+ items that look like references)
	if items := splitListField(result.Fields.References); len(items) >= 3 {
		result.SubItems = items
		// keep references field intact — it's still valid as a summary
		return
	}
}

// splitListField splits a string that might be a list into individual items.
// Handles newline-delimited, comma-delimited, and numbered/bulleted formats.
// Returns nil if the string doesn't look like a list (fewer than 3 items).
func splitListField(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var items []string

	// Try newline splitting first (most common model error format)
	if strings.Contains(s, "\n") {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			// Strip list markers: 1. 2. - • *
			line = strings.TrimLeft(line, "0123456789.")
			line = strings.TrimLeft(line, " ")
			line = strings.TrimPrefix(line, "- ")
			line = strings.TrimPrefix(line, "• ")
			line = strings.TrimPrefix(line, "* ")
			line = strings.TrimSpace(line)
			if line != "" {
				items = append(items, line)
			}
		}
	} else if strings.Contains(s, ",") {
		// Comma-separated: "item1, item2, item3"
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			// Handle "and" in last item: "item1, item2, and item3"
			part = strings.TrimPrefix(part, "and ")
			part = strings.TrimSpace(part)
			if part != "" {
				items = append(items, part)
			}
		}
	}

	if len(items) >= 3 {
		return items
	}
	return nil
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
10. If the input contains a list (numbered 1. 2. 3., bulleted - • *, or comma-separated), extract EACH item into "sub_items" as separate strings. Do NOT put list items into "follow_ups". The "follow_ups" field is for prose about what to do next, NOT for listing items.

JSON SCHEMA (return exactly this structure, nothing else):
{
  "category": "string",
  "confidence": 0.0,
  "title": "string",
  "fields": {
    "name": "string or omit",
    "context": "string or omit",
    "follow_ups": "string, prose only, NOT for list items",
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
  "tags": ["string"],
  "sub_items": ["REQUIRED for any list input — one string per item"]
}`
