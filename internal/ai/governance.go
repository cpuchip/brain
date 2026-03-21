package ai

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
)

type governancePolicy struct {
	agentName         string
	allowedWriteRoots []string
}

func buildGovernance(cfg AgentConfig) governancePolicy {
	name := cfg.AgentName
	if name == "" {
		name = "_default"
	}

	relPaths := defaultAllowedWritePaths(name)
	if cfg.AllowedWritePaths != nil {
		if override, ok := cfg.AllowedWritePaths[name]; ok && len(override) > 0 {
			relPaths = override
		}
	}

	roots := make([]string, 0, len(relPaths))
	for _, p := range relPaths {
		if p == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cfg.WorkingDir, p)
		}
		roots = append(roots, filepath.Clean(abs))
	}

	return governancePolicy{
		agentName:         name,
		allowedWriteRoots: roots,
	}
}

func defaultAllowedWritePaths(agentName string) []string {
	switch agentName {
	case "study":
		return []string{"study", "study/.scratch"}
	case "lesson":
		return []string{"lessons"}
	case "journal":
		return []string{"journal"}
	case "plan":
		return []string{".spec/proposals", ".spec/scratch"}
	case "dev":
		return []string{"scripts", ".spec", "docs"}
	case "eval":
		return []string{"study/yt"}
	case "review":
		return []string{"study/talks"}
	case "talk":
		return []string{"callings"}
	default:
		return []string{"study", "lessons", "journal", "callings", ".spec", "docs", "scripts"}
	}
}

func (g governancePolicy) PreToolDecision(input copilot.PreToolUseHookInput) *copilot.PreToolUseHookOutput {
	if isDestructiveToolCall(input.ToolName, input.ToolArgs) {
		return &copilot.PreToolUseHookOutput{
			PermissionDecision:       "deny",
			PermissionDecisionReason: "Destructive operations are blocked by governance",
		}
	}

	if !isWriteTool(input.ToolName) {
		return &copilot.PreToolUseHookOutput{PermissionDecision: "allow"}
	}

	paths := extractPathCandidates(input.ToolArgs)
	for _, p := range paths {
		if !g.isPathAllowed(p, input.Cwd) {
			return &copilot.PreToolUseHookOutput{
				PermissionDecision:       "deny",
				PermissionDecisionReason: fmt.Sprintf("Agent %s cannot write to %s", g.agentName, p),
			}
		}
	}

	return &copilot.PreToolUseHookOutput{PermissionDecision: "allow"}
}

func (g governancePolicy) isPathAllowed(pathValue, cwd string) bool {
	if pathValue == "" {
		return true
	}
	candidate := pathValue
	if !filepath.IsAbs(candidate) {
		base := cwd
		if base == "" {
			base = "."
		}
		candidate = filepath.Join(base, candidate)
	}
	candidate = filepath.Clean(candidate)

	for _, root := range g.allowedWriteRoots {
		if candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isWriteTool(toolName string) bool {
	name := strings.ToLower(toolName)
	if strings.Contains(name, "delete") || strings.Contains(name, "edit") || strings.Contains(name, "patch") || strings.Contains(name, "write") || strings.Contains(name, "create") || strings.Contains(name, "rename") {
		return true
	}
	return name == "run_in_terminal" || strings.HasSuffix(name, ".run_in_terminal")
}

func isDestructiveToolCall(toolName string, args any) bool {
	name := strings.ToLower(toolName)
	if strings.Contains(name, "delete") {
		return true
	}

	cmd := strings.ToLower(extractStringKey(args, "command"))
	if cmd == "" {
		return false
	}

	patterns := []string{
		`\bgit\s+reset\s+--hard\b`,
		`\bgit\s+checkout\s+--\b`,
		`\brm\s+-rf\b`,
		`\bdel\s+/[sqf]+\b`,
		`\bformat\s+[a-z]:\b`,
		`\bdrop\s+database\b`,
		`\btruncate\s+table\b`,
	}

	for _, p := range patterns {
		if regexp.MustCompile(p).MatchString(cmd) {
			return true
		}
	}
	return false
}

func extractPathCandidates(v any) []string {
	var out []string
	walkAny(v, func(k, val string) {
		switch strings.ToLower(k) {
		case "path", "filepath", "dirpath", "old_path", "new_path", "workspacefolder":
			if val != "" {
				out = append(out, val)
			}
		}
	})
	return out
}

func extractStringKey(v any, key string) string {
	target := strings.ToLower(key)
	found := ""
	walkAny(v, func(k, val string) {
		if found != "" {
			return
		}
		if strings.ToLower(k) == target {
			found = val
		}
	})
	return found
}

func walkAny(v any, fn func(key, val string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if s, ok := child.(string); ok {
				fn(k, s)
			}
			walkAny(child, fn)
		}
	case []any:
		for _, child := range t {
			walkAny(child, fn)
		}
	}
}
