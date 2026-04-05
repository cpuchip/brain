// Package pipeline — project context injection for agent prompts.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/store"
)

// ProjectContext holds pre-built project context for injection into agent prompts.
type ProjectContext struct {
	ProjectName    string
	Description    string
	SiblingEntries []siblingEntry
	ContextDoc     string // content of project context_file if set
}

type siblingEntry struct {
	Title       string
	Maturity    string
	RouteStatus string
}

// BuildProjectContext assembles project context for an entry that belongs to a project.
// Returns nil if the entry has no project assignment.
func (p *Pipeline) BuildProjectContext(entry *store.Entry) *ProjectContext {
	if entry.ProjectID == nil {
		return nil
	}

	project, err := p.store.DB().GetProject(*entry.ProjectID)
	if err != nil {
		return nil
	}

	ctx := &ProjectContext{
		ProjectName: project.Name,
		Description: project.Description,
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
			if len(doc) > 3000 {
				doc = doc[:3000] + "\n...(truncated)"
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
