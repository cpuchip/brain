// Package mcp provides an MCP server that exposes brain entries as searchable tools.
// This allows any VS Code workspace to query the brain's memory via the MCP protocol.
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/cpuchip/brain/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server wraps the MCP server and brain store.
type Server struct {
	mcpServer *server.MCPServer
	store     *store.Store
}

// New creates a new MCP server with brain tools.
func New(st *store.Store) *Server {
	mcpServer := server.NewMCPServer(
		"brain-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	s := &Server{
		mcpServer: mcpServer,
		store:     st,
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
