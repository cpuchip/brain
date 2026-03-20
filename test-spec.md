# Spec: Add markdown_link to gospel_get responses

## Context

The `gospel_search` tool already returns a `markdown_link` field in its results (e.g., `[Alma 32:21](../gospel-library/eng/scriptures/bofm/alma/32.md)`). However, the `gospel_get` tool does NOT include this field in its `GetResponse` struct, even though it has the `file_path` available.

This is inconsistent — after using `gospel_get` to retrieve a verse or talk, the user has to manually construct the markdown link from `file_path` and `reference`.

## Task

Add the `markdown_link` field to `GetResponse` in `gospel-mcp` so that the `gospel_get` tool returns pre-formatted markdown links, matching how `gospel_search` already works.

## Files

The relevant code is in `scripts/gospel-mcp/`:
- `internal/tools/tools.go` — `GetResponse` struct definition
- `internal/tools/get.go` — All the response construction sites
- `internal/tools/search.go` — Has `generateMarkdownLink()` and `generateTalkMarkdownLink()` helper functions (reuse these)

## Requirements

1. Add `MarkdownLink string` field to `GetResponse` struct in `tools.go`
2. Populate `MarkdownLink` in every `GetResponse` construction in `get.go`, using the existing `generateMarkdownLink()` / `generateTalkMarkdownLink()` helpers
3. Make sure the `json` tag is `"markdown_link"`
