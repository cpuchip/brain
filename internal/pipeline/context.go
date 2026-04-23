// Package pipeline — project context injection for agent prompts.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/store"
)

// loadBaseInstructions reads and trims the workspace's copilot-instructions.md
// for injection as Layer 0 in pipeline agent system messages.
// Returns empty string if the file doesn't exist or workspace is not set.
func (p *Pipeline) loadBaseInstructions() string {
	if p.workspace == "" {
		return ""
	}
	instrPath := filepath.Join(p.workspace, ".github", "copilot-instructions.md")
	data, err := os.ReadFile(instrPath)
	if err != nil {
		return ""
	}
	return trimBaseInstructions(string(data))
}

// trimBaseInstructions extracts the essentials from copilot-instructions.md:
// voice, covenant, core principles. Strips MCP tool tables, agent mode lists,
// session memory procedures, and other operational details that waste agent tokens.
func trimBaseInstructions(full string) string {
	lines := strings.Split(full, "\n")
	var kept []string
	skip := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip sections that are operational, not identity/voice
		if strings.HasPrefix(trimmed, "## MCP Tools") ||
			strings.HasPrefix(trimmed, "## Agent Modes") ||
			strings.HasPrefix(trimmed, "## Session Memory") ||
			strings.HasPrefix(trimmed, "## Running the Becoming App") ||
			strings.HasPrefix(trimmed, "## Living Documents") {
			skip = true
			continue
		}

		// Resume at next H2
		if skip && strings.HasPrefix(trimmed, "## ") {
			skip = false
		}
		if skip {
			continue
		}

		kept = append(kept, line)
	}

	result := strings.Join(kept, "\n")
	// Hard cap at ~8000 chars (~2000 tokens)
	if len(result) > 8000 {
		result = result[:8000] + "\n...(trimmed for token budget)"
	}
	return result
}

// ProjectContext holds pre-built project context for injection into agent prompts.
type ProjectContext struct {
	ProjectName    string
	Description    string
	WorkspaceType  string // "integrated", "subfolder", "external"
	WorkspacePath  string // relative path from workspace root (e.g. "projects/space-center")
	SiblingEntries []siblingEntry
	ContextDoc     string // content of project context_file if set
}

type siblingEntry struct {
	Title       string
	Maturity    string
	RouteStatus string
}

// BuildProjectContext assembles project context for an entry that belongs to a project.
// Returns nil if the entry has no project assignment, or if the project is
// non-pipeline (manual). Non-pipeline projects must not feed any data into
// agent prompts — they're the user's private workspace.
func (p *Pipeline) BuildProjectContext(entry *store.Entry) *ProjectContext {
	if entry.ProjectID == nil {
		return nil
	}

	project, err := p.store.DB().GetProject(*entry.ProjectID)
	if err != nil {
		return nil
	}
	if !project.PipelineEnabled {
		return nil
	}

	ctx := &ProjectContext{
		ProjectName:   project.Name,
		Description:   project.Description,
		WorkspaceType: project.WorkspaceType,
		WorkspacePath: project.WorkspacePath,
	}

	// Load sibling entries (same project, limit 20, titles + maturity only)
	siblings, err := p.store.DB().ListEntriesByProject(*entry.ProjectID)
	if err == nil {
		for i, s := range siblings {
			if i >= 20 {
				break
			}
			if s.ID == entry.ID {
				continue // skip self
			}
			ctx.SiblingEntries = append(ctx.SiblingEntries, siblingEntry{
				Title:       s.Title,
				Maturity:    s.Maturity,
				RouteStatus: s.RouteStatus,
			})
		}
	}

	// Load context file if configured
	if project.ContextFile != "" && p.workspace != "" {
		absPath := project.ContextFile
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(p.workspace, absPath)
		}
		if data, err := os.ReadFile(absPath); err == nil {
			doc := string(data)
			if len(doc) > 8000 {
				doc = doc[:8000] + "\n...(truncated)"
			}
			ctx.ContextDoc = doc
		}
	}

	return ctx
}

// FormatProjectContext renders project context as a prompt section.
// Returns empty string if ctx is nil.
func FormatProjectContext(ctx *ProjectContext) string {
	if ctx == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n---\n**Project:** %s\n", ctx.ProjectName))
	if ctx.Description != "" {
		desc := ctx.Description
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("**Description:** %s\n", desc))
	}

	if ctx.WorkspacePath != "" {
		sb.WriteString(fmt.Sprintf("**Project directory:** %s\n", filepath.ToSlash(ctx.WorkspacePath)))
		sb.WriteString("All project files (scratch, proposals, docs) should be created within this directory.\n")
	}
	if ctx.WorkspaceType == "external" {
		sb.WriteString("This is an external project with its own git repository.\n")
	}

	if len(ctx.SiblingEntries) > 0 {
		sb.WriteString("\n**Related entries in this project:**\n")
		for _, s := range ctx.SiblingEntries {
			status := s.Maturity
			if s.RouteStatus != "" {
				status += "/" + s.RouteStatus
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", status, s.Title))
		}
	}

	if ctx.ContextDoc != "" {
		sb.WriteString(fmt.Sprintf("\n**Project context document:**\n%s\n", ctx.ContextDoc))
	}

	sb.WriteString("---\n")
	return sb.String()
}

// resolveWorkDir returns the working directory for pipeline agents operating on
// an entry. For external projects, this is the project's own directory. For
// subfolder and integrated projects, agents work from the workspace root.
func (p *Pipeline) resolveWorkDir(entry *store.Entry) string {
	if entry.ProjectID == nil {
		return p.workspace
	}
	project, err := p.store.DB().GetProject(*entry.ProjectID)
	if err != nil || project == nil {
		return p.workspace
	}
	if project.WorkspaceType == "external" && project.WorkspacePath != "" {
		abs := project.WorkspacePath
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.workspace, abs)
		}
		// Only use if the directory actually exists
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs
		}
	}
	return p.workspace
}

// projectRelPath returns a path prefixed with the project's workspace path
// if the entry belongs to a project with one. Otherwise returns the path unchanged.
func (p *Pipeline) projectRelPath(entry *store.Entry, relPath string) string {
	if entry.ProjectID == nil {
		return relPath
	}
	project, err := p.store.DB().GetProject(*entry.ProjectID)
	if err != nil || project == nil || project.WorkspacePath == "" {
		return relPath
	}
	return filepath.Join(project.WorkspacePath, relPath)
}
