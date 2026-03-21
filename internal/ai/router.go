package ai

import (
	"bytes"
	"text/template"
)

// RouteMode controls how entries are routed to agents after classification.
type RouteMode string

const (
	// RouteModeNone means no agent involvement — just store.
	RouteModeNone RouteMode = "none"
	// RouteModeSuggest means the entry is marked as agent-eligible for human approval.
	RouteModeSuggest RouteMode = "suggest"
	// RouteModeAuto means the entry is routed immediately (requires governance).
	RouteModeAuto RouteMode = "auto"
)

// RouteRule maps a classifier category to an agent and routing behavior.
type RouteRule struct {
	AgentName      string    // which agent handles this category
	Mode           RouteMode // how routing is triggered
	PromptTemplate string    // Go template for the agent prompt
}

// RouteEntry holds the routing information attached to an entry after classification.
type RouteEntry struct {
	AgentName string // which agent should handle this (empty = no agent)
	Status    string // "" | "suggested" | "pending" | "running" | "complete" | "failed"
}

// Route status constants.
const (
	RouteStatusSuggested = "suggested"
	RouteStatusPending   = "pending"
	RouteStatusRunning   = "running"
	RouteStatusComplete  = "complete"
	RouteStatusFailed    = "failed"
	RouteStatusDismissed = "dismissed"
)

// DefaultRoutes maps classifier categories to agents.
// Default mode is "suggest" for categories with clean agent mappings,
// "none" for categories without a natural agent.
var DefaultRoutes = map[string]RouteRule{
	"study":    {AgentName: "study", Mode: RouteModeSuggest, PromptTemplate: "Study this insight: {{.Body}}"},
	"journal":  {AgentName: "journal", Mode: RouteModeSuggest, PromptTemplate: "Reflect on this: {{.Body}}"},
	"ideas":    {AgentName: "plan", Mode: RouteModeSuggest, PromptTemplate: "Evaluate this idea: {{.Body}}"},
	"projects": {AgentName: "", Mode: RouteModeNone},
	"actions":  {AgentName: "", Mode: RouteModeNone},
	"people":   {AgentName: "", Mode: RouteModeNone},
}

// RoutePromptData is the data available to route prompt templates.
type RoutePromptData struct {
	Title string
	Body  string
}

// RenderPrompt executes the route's prompt template with the given data.
// Returns the raw body if no template is configured.
func (r *RouteRule) RenderPrompt(data RoutePromptData) string {
	if r.PromptTemplate == "" {
		return data.Body
	}
	t, err := template.New("prompt").Parse(r.PromptTemplate)
	if err != nil {
		return data.Body
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return data.Body
	}
	return buf.String()
}

// LookupRoute returns the routing rule for a category.
// Returns a zero-value rule with RouteModeNone if the category isn't mapped.
func LookupRoute(category string) RouteRule {
	if r, ok := DefaultRoutes[category]; ok {
		return r
	}
	return RouteRule{Mode: RouteModeNone}
}
