// Package mcp provides an MCP server that exposes brain entries as searchable tools.
// This allows any VS Code workspace to query the brain's memory via the MCP protocol.
package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server wraps the MCP server and brain store.
type Server struct {
	mcpServer    *server.MCPServer
	store        *store.Store
	workspaceDir string // workspace root for scratch file reading
}

// New creates a new MCP server with brain tools.
// workspaceDir is optional — needed for brain_review to read scratch files.
func New(st *store.Store, workspaceDir ...string) *Server {
	mcpServer := server.NewMCPServer(
		"brain-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	s := &Server{
		mcpServer: mcpServer,
		store:     st,
	}
	if len(workspaceDir) > 0 {
		s.workspaceDir = workspaceDir[0]
	}

	s.registerTools()
	return s
}

// Serve starts the MCP server on stdin/stdout.
func (s *Server) Serve() error {
	return server.ServeStdio(s.mcpServer)
}

var readOnly = boolPtr(true)
var notDestructive = boolPtr(false)
var idempotent = boolPtr(true)
var notOpenWorld = boolPtr(false)

func boolPtr(b bool) *bool { return &b }

var readOnlyAnnotation = mcp.WithToolAnnotation(mcp.ToolAnnotation{
	ReadOnlyHint:    readOnly,
	DestructiveHint: notDestructive,
	IdempotentHint:  idempotent,
	OpenWorldHint:   notOpenWorld,
})

func (s *Server) registerTools() {
	s.mcpServer.AddTool(
		mcp.NewTool("brain_search",
			mcp.WithDescription("Search your brain's memory. Uses semantic (vector) search when available, with text search as fallback. Returns matching thoughts with titles, categories, and snippets."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("What to search for"),
			),
			mcp.WithString("category",
				mcp.Description("Filter by category: people, actions, ideas, study, journal, projects, inbox"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum results to return (default: 10)"),
			),
			readOnlyAnnotation,
		),
		s.handleSearch,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("brain_recent",
			mcp.WithDescription("Get recent brain entries, newest first. Optionally filter by category."),
			mcp.WithString("category",
				mcp.Description("Filter by category: people, actions, ideas, study, journal, projects, inbox"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum results to return (default: 10)"),
			),
			readOnlyAnnotation,
		),
		s.handleRecent,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("brain_get",
			mcp.WithDescription("Get a specific brain entry by ID. Returns full details including body text and all category-specific fields."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("The entry UUID"),
			),
			readOnlyAnnotation,
		),
		s.handleGet,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("brain_stats",
			mcp.WithDescription("Get brain statistics: entry counts by category, total entries, and vector store status."),
			readOnlyAnnotation,
		),
		s.handleStats,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("brain_tags",
			mcp.WithDescription("List all tags used across brain entries, with usage counts."),
			readOnlyAnnotation,
		),
		s.handleTags,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("brain_queue",
			mcp.WithDescription("View the brain pipeline — entries grouped by maturity stage (raw, researched, planned, specced, executing, verified). Shows ideas, projects, and study entries that are progressing through the pipeline. Use this to see what needs attention, what's ready to act on, and what's still forming."),
			mcp.WithString("stage",
				mcp.Description("Filter to a specific maturity stage: raw, researched, planned, specced, executing, verified"),
			),
			mcp.WithString("category",
				mcp.Description("Filter by category: ideas, projects, study"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum entries per stage (default: 10)"),
			),
			readOnlyAnnotation,
		),
		s.handleQueue,
	)

	var mutatingAnnotation = mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: notDestructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   notOpenWorld,
	})

	s.mcpServer.AddTool(
		mcp.NewTool("brain_advance",
			mcp.WithDescription("Advance a pipeline entry through maturity stages: raw → researched → planned → specced. "+
				"Actions: advance (next stage), revise (re-do current stage with feedback), reject (back to raw), defer (pause). "+
				"Only works for pipeline categories (ideas, projects, study). "+
				"When advancing from planned → specced, provide scenarios (testable acceptance criteria)."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("The entry UUID"),
			),
			mcp.WithString("action",
				mcp.Required(),
				mcp.Description("What to do: advance, revise, reject, defer"),
			),
			mcp.WithString("feedback",
				mcp.Description("Human guidance for revise action, or notes for advance"),
			),
			mcp.WithString("scenarios",
				mcp.Description("Newline-separated list of testable scenarios (required when advancing from planned → specced)"),
			),
			mutatingAnnotation,
		),
		s.handleAdvance,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("brain_review",
			mcp.WithDescription("Review a pipeline entry with its scratch file contents. Returns the entry details, maturity stage, and inline scratch file contents for human review before advancing."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("The entry UUID"),
			),
			mcp.WithString("include_scratch",
				mcp.Description("Include scratch file contents inline (default: true). Set to 'false' to skip."),
			),
			readOnlyAnnotation,
		),
		s.handleReview,
	)
}

func (s *Server) handleSearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := request.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("query parameter is required"), nil
	}

	limit := 10
	if v, ok := request.GetArguments()["limit"]; ok {
		if n, ok := v.(float64); ok && n > 0 {
			limit = int(n)
		}
	}

	category, _ := request.GetArguments()["category"].(string)

	var b strings.Builder

	// Try semantic search first
	vec := s.store.Vec()
	if vec != nil && vec.Enabled() {
		var results []store.SearchResult
		var searchErr error
		if category != "" {
			results, searchErr = vec.SearchWithCategory(ctx, query, category, limit)
		} else {
			results, searchErr = vec.Search(ctx, query, limit)
		}

		if searchErr == nil && len(results) > 0 {
			fmt.Fprintf(&b, "## Semantic Search: %q\n\n", query)
			if category != "" {
				fmt.Fprintf(&b, "Category filter: %s\n\n", category)
			}
			for i, r := range results {
				fmt.Fprintf(&b, "%d. **%s** (%.0f%% match)\n", i+1, r.Metadata["title"], r.Similarity*100)
				fmt.Fprintf(&b, "   - ID: `%s`\n", r.EntryID)
				fmt.Fprintf(&b, "   - Category: %s\n", r.Metadata["category"])
				snippet := truncate(r.Content, 200)
				fmt.Fprintf(&b, "   - %s\n\n", snippet)
			}
			return mcp.NewToolResultText(b.String()), nil
		}
	}

	// Fallback to text search
	entries, err := s.store.DB().SearchText(query, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	if len(entries) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No results found for %q", query)), nil
	}

	fmt.Fprintf(&b, "## Text Search: %q\n\n", query)
	for i, e := range entries {
		if category != "" && e.Category != category {
			continue
		}
		fmt.Fprintf(&b, "%d. **%s**\n", i+1, e.Title)
		fmt.Fprintf(&b, "   - ID: `%s`\n", e.ID)
		fmt.Fprintf(&b, "   - Category: %s\n", e.Category)
		fmt.Fprintf(&b, "   - Created: %s\n\n", e.Created.Format("2006-01-02 15:04"))
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleRecent(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := 10
	if v, ok := request.GetArguments()["limit"]; ok {
		if n, ok := v.(float64); ok && n > 0 {
			limit = int(n)
		}
	}

	category, _ := request.GetArguments()["category"].(string)

	var entries []*store.Entry
	var err error

	if category != "" {
		entries, err = s.store.DB().ListCategory(category)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("listing category failed: %v", err)), nil
		}
		if len(entries) > limit {
			entries = entries[:limit]
		}
	} else {
		entries, err = s.store.DB().ListAll(limit, 0)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("listing entries failed: %v", err)), nil
		}
	}

	if len(entries) == 0 {
		msg := "No entries found"
		if category != "" {
			msg = fmt.Sprintf("No entries in category %q", category)
		}
		return mcp.NewToolResultText(msg), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Recent Entries")
	if category != "" {
		fmt.Fprintf(&b, " (%s)", category)
	}
	fmt.Fprintf(&b, "\n\n")

	for i, e := range entries {
		fmt.Fprintf(&b, "%d. **%s**\n", i+1, e.Title)
		fmt.Fprintf(&b, "   - ID: `%s`\n", e.ID)
		fmt.Fprintf(&b, "   - Category: %s\n", e.Category)
		fmt.Fprintf(&b, "   - Source: %s\n", e.Source)
		fmt.Fprintf(&b, "   - Created: %s\n\n", e.Created.Format("2006-01-02 15:04"))
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("id parameter is required"), nil
	}

	entry, err := s.store.ReadEntry(id)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("entry not found: %v", err)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", entry.Title)
	fmt.Fprintf(&b, "- **Category:** %s\n", entry.Category)
	fmt.Fprintf(&b, "- **Created:** %s\n", entry.Created.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "- **Updated:** %s\n", entry.Updated.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "- **Confidence:** %.0f%%\n", entry.Confidence*100)
	fmt.Fprintf(&b, "- **Source:** %s\n", entry.Source)

	if len(entry.Tags) > 0 {
		fmt.Fprintf(&b, "- **Tags:** %s\n", strings.Join(entry.Tags, ", "))
	}

	// Category-specific fields
	if entry.Name != "" {
		fmt.Fprintf(&b, "- **Person:** %s\n", entry.Name)
	}
	if entry.Context != "" {
		fmt.Fprintf(&b, "- **Context:** %s\n", entry.Context)
	}
	if entry.FollowUps != "" {
		fmt.Fprintf(&b, "- **Follow-ups:** %s\n", entry.FollowUps)
	}
	if entry.Status != "" {
		fmt.Fprintf(&b, "- **Status:** %s\n", entry.Status)
	}
	if entry.NextAction != "" {
		fmt.Fprintf(&b, "- **Next Action:** %s\n", entry.NextAction)
	}
	if entry.OneLiner != "" {
		fmt.Fprintf(&b, "- **One-liner:** %s\n", entry.OneLiner)
	}
	if entry.DueDate != "" {
		fmt.Fprintf(&b, "- **Due:** %s\n", entry.DueDate)
	}
	if entry.ActionDone {
		fmt.Fprintf(&b, "- **Done:** yes\n")
	}
	if entry.References != "" {
		fmt.Fprintf(&b, "- **References:** %s\n", entry.References)
	}
	if entry.Insight != "" {
		fmt.Fprintf(&b, "- **Insight:** %s\n", entry.Insight)
	}
	if entry.Mood != "" {
		fmt.Fprintf(&b, "- **Mood:** %s\n", entry.Mood)
	}
	if entry.Gratitude != "" {
		fmt.Fprintf(&b, "- **Gratitude:** %s\n", entry.Gratitude)
	}

	fmt.Fprintf(&b, "\n---\n\n%s\n", entry.Body)
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleStats(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	stats, err := s.store.DB().Stats()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("stats failed: %v", err)), nil
	}

	total := 0
	var b strings.Builder
	fmt.Fprintf(&b, "## Brain Statistics\n\n")
	fmt.Fprintf(&b, "| Category | Count |\n|----------|-------|\n")
	for cat, count := range stats {
		fmt.Fprintf(&b, "| %s | %d |\n", cat, count)
		total += count
	}
	fmt.Fprintf(&b, "| **Total** | **%d** |\n\n", total)

	vec := s.store.Vec()
	if vec != nil && vec.Enabled() {
		fmt.Fprintf(&b, "Vector store: %d documents (model: %s)\n", vec.Count(ctx), vec.Model())
	} else {
		fmt.Fprintf(&b, "Vector store: disabled\n")
	}

	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleTags(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tags, err := s.store.DB().ListTags()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("listing tags failed: %v", err)), nil
	}

	if len(tags) == 0 {
		return mcp.NewToolResultText("No tags found"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Tags\n\n")
	fmt.Fprintf(&b, "| Tag | Count |\n|-----|-------|\n")
	for tag, count := range tags {
		fmt.Fprintf(&b, "| %s | %d |\n", tag, count)
	}
	return mcp.NewToolResultText(b.String()), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// stageOrder defines display order for pipeline stages.
var stageOrder = []string{"raw", "researched", "planned", "specced", "executing", "verified"}

func (s *Server) handleQueue(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := 10
	if v, ok := request.GetArguments()["limit"]; ok {
		if n, ok := v.(float64); ok && n > 0 {
			limit = int(n)
		}
	}

	stage, _ := request.GetArguments()["stage"].(string)
	category, _ := request.GetArguments()["category"].(string)

	pipeline, err := s.store.DB().ListPipeline(stage, category, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("pipeline query failed: %v", err)), nil
	}

	// Count total entries
	total := 0
	for _, entries := range pipeline {
		total += len(entries)
	}

	if total == 0 {
		msg := "No pipeline entries found"
		if stage != "" {
			msg += fmt.Sprintf(" at stage %q", stage)
		}
		if category != "" {
			msg += fmt.Sprintf(" in category %q", category)
		}
		return mcp.NewToolResultText(msg), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Pipeline Queue")
	if category != "" {
		fmt.Fprintf(&b, " (%s)", category)
	}
	fmt.Fprintf(&b, "\n\n")
	fmt.Fprintf(&b, "**%d entries** across pipeline stages\n\n", total)

	for _, st := range stageOrder {
		entries, ok := pipeline[st]
		if !ok || len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s (%d)\n\n", strings.ToUpper(st[:1])+st[1:], len(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "- **%s** `[%s]`\n", e.Title, e.Category)
			fmt.Fprintf(&b, "  - ID: `%s`\n", e.ID)
			fmt.Fprintf(&b, "  - Updated: %s\n", e.Updated.Format("2006-01-02"))
			if e.ScratchPath != "" {
				fmt.Fprintf(&b, "  - Scratch: %s\n", e.ScratchPath)
			}
		}
		fmt.Fprintf(&b, "\n")
	}

	return mcp.NewToolResultText(b.String()), nil
}

// validActions for brain_advance.
var validActions = map[string]bool{
	"advance": true,
	"revise":  true,
	"reject":  true,
	"defer":   true,
}

// pipelineCats defines which categories participate in the pipeline.
var pipelineCats = map[string]bool{
	"ideas":    true,
	"projects": true,
	"study":    true,
}

func nextStage(current string) (string, bool) {
	for i, s := range stageOrder {
		if s == current && i+1 < len(stageOrder) {
			return stageOrder[i+1], true
		}
	}
	return "", false
}

func (s *Server) handleAdvance(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("id parameter is required"), nil
	}

	action, err := request.RequireString("action")
	if err != nil {
		return mcp.NewToolResultError("action parameter is required"), nil
	}

	if !validActions[action] {
		return mcp.NewToolResultError(fmt.Sprintf("invalid action %q — use: advance, revise, reject, defer", action)), nil
	}

	entry, err := s.store.DB().GetEntry(id)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("entry not found: %v", err)), nil
	}

	if !pipelineCats[entry.Category] {
		return mcp.NewToolResultError(fmt.Sprintf("entry %s (category: %s) is not a pipeline category — only ideas, projects, study", id, entry.Category)), nil
	}

	currentMaturity := entry.Maturity
	if currentMaturity == "" {
		currentMaturity = "raw"
	}

	feedback, _ := request.GetArguments()["feedback"].(string)
	scenariosStr, _ := request.GetArguments()["scenarios"].(string)

	var b strings.Builder

	switch action {
	case "advance":
		next, ok := nextStage(currentMaturity)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("cannot advance beyond %s", currentMaturity)), nil
		}

		// planned → specced requires scenarios
		if currentMaturity == "planned" {
			if scenariosStr == "" {
				return mcp.NewToolResultError("advancing from planned → specced requires scenarios (newline-separated list of testable acceptance criteria)"), nil
			}
			scenarios := strings.Split(scenariosStr, "\n")
			var cleaned []string
			for _, s := range scenarios {
				s = strings.TrimSpace(s)
				if s != "" {
					cleaned = append(cleaned, s)
				}
			}
			if len(cleaned) == 0 {
				return mcp.NewToolResultError("scenarios must contain at least one non-empty line"), nil
			}
			scenariosFormatted := "- " + strings.Join(cleaned, "\n- ")
			if err := s.store.DB().SetScenarios(entry.ID, scenariosFormatted); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to set scenarios: %v", err)), nil
			}
		}

		notes := ""
		if feedback != "" {
			notes = feedback
		}
		if err := s.store.DB().SetMaturity(entry.ID, next, notes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to advance: %v", err)), nil
		}

		fmt.Fprintf(&b, "## Advanced: %s → %s\n\n", currentMaturity, next)
		fmt.Fprintf(&b, "**%s** `[%s]`\n\n", entry.Title, entry.Category)
		if next == "researched" {
			fmt.Fprintf(&b, "Entry marked as researched. To trigger an AI research pass, use `brain advance %s` from the CLI or web UI.\n", id)
		}
		if next == "planned" {
			fmt.Fprintf(&b, "Entry marked as planned. To trigger an AI plan pass, use `brain advance %s` from the CLI or web UI.\n", id)
		}

	case "revise":
		if feedback == "" {
			return mcp.NewToolResultError("revise requires feedback — what should change?"), nil
		}
		notes := fmt.Sprintf("Revision requested: %s", feedback)
		if err := s.store.DB().SetMaturity(entry.ID, currentMaturity, notes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to record revision: %v", err)), nil
		}
		fmt.Fprintf(&b, "## Revision Recorded\n\n")
		fmt.Fprintf(&b, "**%s** stays at %s\n\n", entry.Title, currentMaturity)
		fmt.Fprintf(&b, "Feedback: %s\n", feedback)

	case "reject":
		if err := s.store.DB().SetMaturity(entry.ID, "raw", "Rejected — returned to raw"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to reject: %v", err)), nil
		}
		fmt.Fprintf(&b, "## Rejected: %s → raw\n\n", currentMaturity)
		fmt.Fprintf(&b, "**%s** returned to raw.\n", entry.Title)

	case "defer":
		notes := fmt.Sprintf("Deferred at %s", currentMaturity)
		if feedback != "" {
			notes += ": " + feedback
		}
		if err := s.store.DB().SetMaturity(entry.ID, currentMaturity, notes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to defer: %v", err)), nil
		}
		fmt.Fprintf(&b, "## Deferred\n\n")
		fmt.Fprintf(&b, "**%s** paused at %s. Will revisit later.\n", entry.Title, currentMaturity)
	}

	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleReview(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("id parameter is required"), nil
	}

	entry, err := s.store.ReadEntry(id)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("entry not found: %v", err)), nil
	}

	includeScratch := true
	if v, ok := request.GetArguments()["include_scratch"].(string); ok && v == "false" {
		includeScratch = false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Review: %s\n\n", entry.Title)
	fmt.Fprintf(&b, "- **Category:** %s\n", entry.Category)
	fmt.Fprintf(&b, "- **Created:** %s\n", entry.Created.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "- **Updated:** %s\n", entry.Updated.Format("2006-01-02 15:04"))

	maturity := entry.Maturity
	if maturity == "" {
		maturity = "raw"
	}
	fmt.Fprintf(&b, "- **Maturity:** %s\n", maturity)

	if entry.MaturityNotes != "" {
		fmt.Fprintf(&b, "- **Notes:** %s\n", entry.MaturityNotes)
	}
	if entry.ScratchPath != "" {
		fmt.Fprintf(&b, "- **Scratch:** %s\n", entry.ScratchPath)
	}
	if entry.Scenarios != "" {
		fmt.Fprintf(&b, "- **Scenarios:** %s\n", entry.Scenarios)
	}
	if len(entry.Tags) > 0 {
		fmt.Fprintf(&b, "- **Tags:** %s\n", strings.Join(entry.Tags, ", "))
	}

	fmt.Fprintf(&b, "\n## Body\n\n%s\n", entry.Body)

	// Include scratch file if requested and available
	if includeScratch && entry.ScratchPath != "" && s.workspaceDir != "" {
		absPath := entry.ScratchPath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(s.workspaceDir, absPath)
		}
		data, readErr := os.ReadFile(absPath)
		if readErr == nil {
			fmt.Fprintf(&b, "\n---\n\n## Scratch File: %s\n\n%s\n", entry.ScratchPath, string(data))
		} else {
			fmt.Fprintf(&b, "\n---\n\n*Scratch file not found at %s*\n", entry.ScratchPath)
		}
	}

	return mcp.NewToolResultText(b.String()), nil
}
