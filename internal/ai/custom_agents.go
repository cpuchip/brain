package ai

import (
	"log"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/cpuchip/brain/internal/config"
)

// agentInferConfig controls which agents the SDK can auto-delegate to.
// Agents listed here with infer=true will be considered by the SDK
// when routing interactive requests to the best-matching agent.
var agentInferConfig = map[string]bool{
	"study":   true,
	"journal": true,
	"plan":    true,
	"dev":     false, // dev tasks need explicit delegation
	"eval":    false, // specialized evaluation workflow
	"review":  false,
	"lesson":  false,
	"talk":    false,
}

// BuildCustomAgents creates SDK CustomAgentConfig entries from workspace agent definitions.
// These are wired into the default agent's session so the SDK can auto-delegate
// interactive requests to the right specialized agent.
func BuildCustomAgents(wc config.WorkspaceConfig) []copilot.CustomAgentConfig {
	if len(wc.Agents) == 0 {
		return nil
	}

	var agents []copilot.CustomAgentConfig
	for name, def := range wc.Agents {
		infer := agentInferConfig[name] // false if not in map
		agent := copilot.CustomAgentConfig{
			Name:        name,
			DisplayName: agentDisplayName(name),
			Description: def.Description,
			Prompt:      BuildSystemMessage(wc, name),
			Infer:       &infer,
		}
		agents = append(agents, agent)
	}

	log.Printf("Built %d custom agents for SDK delegation", len(agents))
	return agents
}

func agentDisplayName(name string) string {
	switch name {
	case "study":
		return "Study Agent"
	case "journal":
		return "Journal Agent"
	case "plan":
		return "Plan Agent"
	case "dev":
		return "Dev Agent"
	case "eval":
		return "Eval Agent"
	case "review":
		return "Review Agent"
	case "lesson":
		return "Lesson Agent"
	case "talk":
		return "Talk Agent"
	case "sabbath":
		return "Sabbath Agent"
	case "teaching":
		return "Teaching Agent"
	case "podcast":
		return "Podcast Agent"
	case "story":
		return "Story Agent"
	case "debug":
		return "Debug Agent"
	case "ux":
		return "UX Agent"
	default:
		return name + " Agent"
	}
}
