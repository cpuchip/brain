package pipeline

import "testing"

func TestToolCallSummary_FilePath(t *testing.T) {
	args := map[string]any{"filePath": "/workspace/src/main.go", "content": "package main"}
	got := toolCallSummary("create_file", args)
	if got != "/workspace/src/main.go" {
		t.Errorf("toolCallSummary(create_file) = %q, want file path", got)
	}
}

func TestToolCallSummary_Query(t *testing.T) {
	args := map[string]any{"query": "SELECT * FROM entries WHERE maturity = 'raw'"}
	got := toolCallSummary("sql", args)
	if got != "SELECT * FROM entries WHERE maturity = 'raw'" {
		t.Errorf("toolCallSummary(sql) = %q, want query", got)
	}
}

func TestToolCallSummary_LongQueryTruncated(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "word "
	}
	args := map[string]any{"query": long}
	got := toolCallSummary("grep_search", args)
	if len(got) > 85 { // 80 + "…"
		t.Errorf("toolCallSummary should truncate long query, got len=%d", len(got))
	}
}

func TestToolCallSummary_FallbackFirstString(t *testing.T) {
	args := map[string]any{"something": "hello world"}
	got := toolCallSummary("unknown_tool", args)
	if got != "hello world" {
		t.Errorf("toolCallSummary(unknown) = %q, want fallback string", got)
	}
}

func TestToolCallSummary_NonMapArgs(t *testing.T) {
	got := toolCallSummary("something", "not a map")
	if got != "" {
		t.Errorf("toolCallSummary with non-map = %q, want empty", got)
	}
}

func TestToolCallSummary_EmptyMap(t *testing.T) {
	args := map[string]any{}
	got := toolCallSummary("noop", args)
	if got != "" {
		t.Errorf("toolCallSummary with empty map = %q, want empty", got)
	}
}
