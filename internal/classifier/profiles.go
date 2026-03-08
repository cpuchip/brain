package classifier

import "strings"

// Task is something a model profile is suited for.
type Task string

const (
	TaskClassify Task = "classify" // Thought classification → JSON
	TaskChat     Task = "chat"     // Conversational / general use
	TaskEmbed    Task = "embed"    // Text embedding (not used by classifier, but tracked)
)

// ModelProfile defines how a specific model should be used.
// Different models respond better to different prompting styles —
// a 3B parameter model needs a more explicit, tightly constrained prompt
// than a 9B model that can infer more from context.
type ModelProfile struct {
	ID          string  // LM Studio model identifier (e.g. "mistralai/ministral-3-3b")
	Name        string  // Human-friendly display name
	Tasks       []Task  // What this model is good for
	Temperature float64 // Optimal classification temperature (0 = use default)
	MaxTokens   int     // Max output tokens (0 = use default 32768)

	// ClassifyPrompt is the system prompt for classification.
	// If empty, falls back to DefaultClassifyPrompt.
	ClassifyPrompt string

	// NoThink disables thinking/reasoning tokens for thinking models (e.g. Qwen 3.5).
	// When true, the classifier appends "/no_think" to the system prompt.
	NoThink bool

	// StructuredOutput enables response_format JSON schema for this model.
	// Uses grammar-based sampling to guarantee valid JSON output.
	// Best for models >= 7B parameters.
	StructuredOutput bool
}

// SupportsTask returns true if the profile lists the given task.
func (p *ModelProfile) SupportsTask(t Task) bool {
	for _, task := range p.Tasks {
		if task == t {
			return true
		}
	}
	return false
}

// registry maps model IDs to their profiles.
// Lookup is case-insensitive and supports partial matching.
var registry = map[string]*ModelProfile{
	"mistralai/ministral-3-3b": {
		ID:          "mistralai/ministral-3-3b",
		Name:        "Ministral 3B",
		Tasks:       []Task{TaskClassify},
		Temperature: 0.1,
		ClassifyPrompt: `You are a JSON classification API. You receive raw text and return a JSON object. You NEVER return prose, explanations, suggestions, or conversation. You are NOT a chatbot. You are a classifier.

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
10. When the input mentions a person AND a task, prefer "people" if the main point is about the person. Prefer "actions" only if the person is incidental and the task is the focus.
11. If the input contains a list (numbered 1. 2. 3., bulleted - • *, or comma-separated), extract EACH item into "sub_items" as separate strings. Do NOT put list items into "follow_ups". The "follow_ups" field is for prose about what to do next, NOT for listing items.

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
}`,
	},

	"qwen/qwen3-1.7b": {
		ID:          "qwen/qwen3-1.7b",
		Name:        "Qwen 3 1.7B",
		Tasks:       []Task{TaskClassify},
		Temperature: 0.1,
		ClassifyPrompt: `You are a JSON classification API. You receive raw text and return a JSON object. You NEVER return prose, explanations, suggestions, or conversation. You are NOT a chatbot. You are a classifier.

IMPORTANT: Even if the input asks a question, proposes an idea, or requests feedback — DO NOT answer it, discuss it, or expand on it. Classify it and return JSON. /no_think

CATEGORIES:
- people: About a person — relationship context, follow-ups, something someone said, a detail to remember about them
- projects: Active work with a status and next action — something being built, planned, or tracked over time
- ideas: A thought, insight, or concept to capture — not yet actionable, just worth remembering
- actions: A specific task or errand — something with a clear "done" state, possibly a deadline
- study: Scripture insight, spiritual impression, gospel learning, covenant commitment
- journal: Personal reflection, observation about life, mood, gratitude, becoming

RULES:
1. Return ONLY valid JSON. No markdown, no explanation, no extra text. No code fences. Do NOT use <think> tags.
2. Confidence is 0.0 to 1.0.
3. Extract a concise title (3-8 words).
4. Extract category-specific fields where available.
5. For people entries, extract the person's name.
6. For study entries, note scripture references.
7. Generate 1-3 tags.
8. When the input mentions a person AND a task, prefer "people" if the main point is about the person.
9. If the input contains a list (numbered 1. 2. 3., bulleted - • *, or comma-separated), extract EACH item into "sub_items" as separate strings. Do NOT put list items into "follow_ups". The "follow_ups" field is for prose about what to do next, NOT for listing items.

EXAMPLE: Input "Shopping: 1. Milk 2. Bread 3. Eggs" → sub_items: ["Milk", "Bread", "Eggs"], NOT follow_ups.
EXAMPLE: Input "Tasks:\n- Mow lawn\n- Fix fence" → sub_items: ["Mow lawn", "Fix fence"], NOT follow_ups.

JSON SCHEMA:
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
}`,
	},

	"qwen/qwen3.5-9b": {
		ID:               "qwen/qwen3.5-9b",
		Name:             "Qwen 3.5 9B",
		Tasks:            []Task{TaskClassify, TaskChat},
		Temperature:      0.1,
		NoThink:          true,
		StructuredOutput: true,
		// Uses default prompt — this was the original model, prompt was tuned for it
	},
}

// DefaultClassifyPrompt is the fallback system prompt for models without a custom one.
var DefaultClassifyPrompt = systemPrompt

// LookupProfile returns the model profile for the given model ID.
// Returns nil if no profile is registered.
// Matching is case-insensitive and supports partial matching (e.g. "ministral-3-3b").
func LookupProfile(modelID string) *ModelProfile {
	lower := strings.ToLower(modelID)
	// Exact match first
	if p, ok := registry[lower]; ok {
		return p
	}
	// Partial match (model ID contains the query or vice versa)
	for key, p := range registry {
		if strings.Contains(lower, key) || strings.Contains(key, lower) {
			return p
		}
	}
	return nil
}

// RegisterProfile adds or replaces a model profile in the registry.
func RegisterProfile(p *ModelProfile) {
	registry[strings.ToLower(p.ID)] = p
}

// ListProfiles returns all registered profiles.
func ListProfiles() []*ModelProfile {
	profiles := make([]*ModelProfile, 0, len(registry))
	for _, p := range registry {
		profiles = append(profiles, p)
	}
	return profiles
}

// ClassifyProfilesForTask returns all profiles that support the given task.
func ProfilesForTask(task Task) []*ModelProfile {
	var result []*ModelProfile
	for _, p := range registry {
		if p.SupportsTask(task) {
			result = append(result, p)
		}
	}
	return result
}
