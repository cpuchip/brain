package pipeline

import "testing"

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare object", `{"action":"advance"}`, `{"action":"advance"}`},
		{"bare array", `["a","b"]`, `["a","b"]`},
		{"code fence json", "```json\n{\"action\":\"advance\"}\n```", `{"action":"advance"}`},
		{"code fence no lang", "```\n{\"action\":\"advance\"}\n```", `{"action":"advance"}`},
		{"text before object", "Here is the result: {\"action\":\"advance\"}", `{"action":"advance"}`},
		{"text before array", "Scenarios:\n[\"build\",\"test\"]", `["build","test"]`},
		{"whitespace", "  {\"x\":1}  ", `{"x":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseGateResponse(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantAction string
		wantErr    bool
	}{
		{
			"advance",
			`{"action":"advance","reasoning":"output is complete"}`,
			"advance", false,
		},
		{
			"revise with feedback",
			`{"action":"revise","reasoning":"incomplete","feedback":"add more detail"}`,
			"revise", false,
		},
		{
			"surface",
			`{"action":"surface","reasoning":"needs human decision"}`,
			"surface", false,
		},
		{
			"invalid action",
			`{"action":"skip","reasoning":"test"}`,
			"", true,
		},
		{
			"invalid json",
			`not json at all`,
			"", true,
		},
		{
			"wrapped in code fence",
			"```json\n{\"action\":\"advance\",\"reasoning\":\"good\"}\n```",
			"advance", false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, _, _, err := parseGateResponse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if action != tt.wantAction {
				t.Errorf("action = %q, want %q", action, tt.wantAction)
			}
		})
	}
}

func TestParseScenariosResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		count   int
		wantErr bool
	}{
		{"simple array", `["builds without errors","tests pass"]`, 2, false},
		{"code fence", "```json\n[\"a\",\"b\",\"c\"]\n```", 3, false},
		{"invalid json", `not an array`, 0, true},
		{"empty array", `[]`, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scenarios, err := parseScenariosResponse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(scenarios) != tt.count {
				t.Errorf("len = %d, want %d", len(scenarios), tt.count)
			}
		})
	}
}

func TestParseVerifyResponse(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantPassed bool
		wantErr    bool
	}{
		{
			"all passed",
			`{"all_passed":true,"reasoning":"everything works","results":[{"scenario":"builds","passed":true,"notes":"ok"}]}`,
			true, false,
		},
		{
			"some failed",
			`{"all_passed":false,"reasoning":"build fails","results":[{"scenario":"builds","passed":false,"notes":"error"}]}`,
			false, false,
		},
		{
			"invalid json",
			`totally not json`,
			false, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed, _, _, err := parseVerifyResponse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if passed != tt.wantPassed {
				t.Errorf("passed = %v, want %v", passed, tt.wantPassed)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is longer than ten", 10, "this is lo..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncate(tt.input, tt.max)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
			}
		})
	}
}
