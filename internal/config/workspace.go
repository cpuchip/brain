package config

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceConfig holds parsed .github/ workspace configuration.
type WorkspaceConfig struct {
	// BaseInstructions is the content of .github/copilot-instructions.md
	BaseInstructions string

	// Agents maps agent name → parsed agent definition
	Agents map[string]AgentDef

	// SkillsDir is the absolute path to .github/skills/ (empty if not found)
	SkillsDir string
}

// AgentDef is a parsed .github/agents/*.agent.md file.
type AgentDef struct {
	Name        string // from filename: study.agent.md → "study"
	Description string // from YAML frontmatter
	Prompt      string // full file content (frontmatter + body) — the SDK needs the whole thing
}

// LoadWorkspace reads .github/ workspace configuration from the given directory.
// Returns a zero-value WorkspaceConfig if the directory doesn't have .github/.
func LoadWorkspace(workspaceDir string) WorkspaceConfig {
	if workspaceDir == "" {
		return WorkspaceConfig{}
	}

	ghDir := filepath.Join(workspaceDir, ".github")
	if _, err := os.Stat(ghDir); err != nil {
		return WorkspaceConfig{}
	}

	var wc WorkspaceConfig

	// Load copilot-instructions.md
	instrPath := filepath.Join(ghDir, "copilot-instructions.md")
	if data, err := os.ReadFile(instrPath); err == nil {
		wc.BaseInstructions = string(data)
		log.Printf("Workspace: loaded copilot-instructions.md (%d bytes)", len(data))
	}

	// Load agents
	agentsDir := filepath.Join(ghDir, "agents")
	if entries, err := os.ReadDir(agentsDir); err == nil {
		wc.Agents = make(map[string]AgentDef)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".agent.md") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".agent.md")
			data, err := os.ReadFile(filepath.Join(agentsDir, entry.Name()))
			if err != nil {
				log.Printf("Workspace: warning: could not read agent %s: %v", entry.Name(), err)
				continue
			}
			content := string(data)
			desc := parseDescription(content)
			wc.Agents[name] = AgentDef{
				Name:        name,
				Description: desc,
				Prompt:      content,
			}
		}
		log.Printf("Workspace: loaded %d agents", len(wc.Agents))
	}

	// Check for skills directory
	skillsDir := filepath.Join(ghDir, "skills")
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
		wc.SkillsDir = skillsDir
		// Count skills
		if entries, err := os.ReadDir(skillsDir); err == nil {
			count := 0
			for _, e := range entries {
				if e.IsDir() {
					count++
				}
			}
			log.Printf("Workspace: skills directory found (%d skills)", count)
		}
	}

	return wc
}

// parseDescription extracts the description field from YAML frontmatter.
// Handles both --- fenced and ```chatagent fenced formats.
func parseDescription(content string) string {
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	isChatagent := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inFrontmatter {
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "```chatagent") {
				isChatagent = true
				continue // next line should be ---
			}
			if trimmed == "---" {
				if isChatagent {
					// This is the --- inside ```chatagent block
					inFrontmatter = true
				} else {
					// Standard YAML frontmatter
					inFrontmatter = true
				}
				continue
			}
			// Non-empty, non-fence line before frontmatter — no frontmatter
			return ""
		}

		// Inside frontmatter — look for description
		if strings.HasPrefix(trimmed, "description:") {
			desc := strings.TrimPrefix(trimmed, "description:")
			desc = strings.TrimSpace(desc)
			desc = strings.Trim(desc, "'\"")
			return desc
		}

		// End of frontmatter
		if trimmed == "---" || trimmed == "```" {
			return ""
		}
	}
	return ""
}
