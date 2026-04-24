package config

// Model is a single entry in the model catalog. One source of truth for
// display names, cost multipliers, Discord preset aliases, and whether the
// model participates in the steward's auto-escalation chain.
type Model struct {
	ID             string  `json:"id"`                        // canonical id, e.g. "claude-opus-4.7"
	DisplayName    string  `json:"display_name"`              // human-friendly name
	Family         string  `json:"family"`                    // "claude" | "gpt" | "gemini" | "other"
	Cost           float64 `json:"cost"`                      // premium-request multiplier
	PresetKey      string  `json:"preset_key,omitempty"`      // Discord alias (raptor, haiku, sonnet, ...)
	InEscalation   bool    `json:"in_escalation"`             // part of auto-escalation chain
	EscalationRank int     `json:"escalation_rank,omitempty"` // 0=cheap, 1=mid, 2=top
}

// Catalog is the authoritative list of models the brain knows about.
// Everything else (AvailableModels, modelCosts, EscalationChain, the UI dropdown)
// derives from this slice.
var Catalog = []Model{
	{ID: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5", Family: "claude", Cost: 0.33, PresetKey: "haiku", InEscalation: true, EscalationRank: 0},
	{ID: "claude-sonnet-4.6", DisplayName: "Claude Sonnet 4.6", Family: "claude", Cost: 1.0, PresetKey: "sonnet", InEscalation: true, EscalationRank: 1},
	{ID: "claude-opus-4.7", DisplayName: "Claude Opus 4.7", Family: "claude", Cost: 7.5, InEscalation: true, EscalationRank: 2},
	{ID: "gpt-5", DisplayName: "GPT-5", Family: "gpt", Cost: 1.0, PresetKey: "gpt5"},
	{ID: "gpt-5-mini", DisplayName: "GPT-5 Mini", Family: "gpt", Cost: 0.0, PresetKey: "gpt-mini"},
	{ID: "gemini-3-flash", DisplayName: "Gemini 3 Flash", Family: "gemini", Cost: 0.33, PresetKey: "flash"},
	{ID: "raptor-mini", DisplayName: "Raptor Mini", Family: "other", Cost: 0.0, PresetKey: "raptor"},
}

// StageDefaults maps a pipeline stage to the default model id for that stage.
// Exposed via /api/models so the UI can pick sensible defaults without
// hardcoding model strings.
var StageDefaults = map[string]string{
	"research":   "claude-sonnet-4.6",
	"plan":       "claude-opus-4.7",
	"spec":       "claude-sonnet-4.6",
	"execute":    "claude-sonnet-4.6",
	"verify":     "claude-haiku-4.5",
	"revise":     "claude-sonnet-4.6",
	"commission": "claude-opus-4.7", // user picks this from the dialog
}

// LookupModel returns the catalog entry for a model id, or nil if not found.
func LookupModel(id string) *Model {
	for i := range Catalog {
		if Catalog[i].ID == id {
			return &Catalog[i]
		}
	}
	return nil
}
