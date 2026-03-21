package ai

import (
	"testing"

	"github.com/cpuchip/brain/internal/config"
)

func TestLookupRoute(t *testing.T) {
	tests := []struct {
		category  string
		wantAgent string
		wantMode  RouteMode
	}{
		{"study", "study", RouteModeSuggest},
		{"journal", "journal", RouteModeSuggest},
		{"ideas", "plan", RouteModeSuggest},
		{"projects", "", RouteModeNone},
		{"actions", "", RouteModeNone},
		{"people", "", RouteModeNone},
		{"unknown", "", RouteModeNone},
	}

	for _, tt := range tests {
		r := LookupRoute(tt.category)
		if r.AgentName != tt.wantAgent {
			t.Errorf("LookupRoute(%q).AgentName = %q, want %q", tt.category, r.AgentName, tt.wantAgent)
		}
		if r.Mode != tt.wantMode {
			t.Errorf("LookupRoute(%q).Mode = %q, want %q", tt.category, r.Mode, tt.wantMode)
		}
	}
}

func TestRouteRenderPrompt(t *testing.T) {
	r := RouteRule{
		AgentName:      "study",
		Mode:           RouteModeSuggest,
		PromptTemplate: "Study this insight: {{.Body}}",
	}

	data := RoutePromptData{Title: "Test", Body: "Grace is given freely"}
	got := r.RenderPrompt(data)
	want := "Study this insight: Grace is given freely"
	if got != want {
		t.Errorf("RenderPrompt() = %q, want %q", got, want)
	}
}

func TestRouteRenderPromptEmpty(t *testing.T) {
	r := RouteRule{Mode: RouteModeNone}
	data := RoutePromptData{Body: "raw text"}
	got := r.RenderPrompt(data)
	if got != "raw text" {
		t.Errorf("RenderPrompt() with empty template = %q, want %q", got, "raw text")
	}
}

func TestBuildSystemMessageWithAgent(t *testing.T) {
	wc := config.WorkspaceConfig{
		BaseInstructions: "Base instructions here",
		Agents: map[string]config.AgentDef{
			"study": {
				Name:        "study",
				Description: "Deep study",
				Prompt:      "You are the study agent.",
			},
		},
	}

	msg := BuildSystemMessage(wc, "study")
	if msg == "" {
		t.Error("BuildSystemMessage returned empty string")
	}
	// Should contain agent prompt
	if !containsStr(msg, "You are the study agent.") {
		t.Error("BuildSystemMessage should include agent prompt")
	}
	// Should also include base instructions
	if !containsStr(msg, "Base instructions here") {
		t.Error("BuildSystemMessage should include base instructions")
	}
}

func TestBuildSystemMessageDefault(t *testing.T) {
	wc := config.WorkspaceConfig{
		BaseInstructions: "Base instructions here",
	}

	msg := BuildSystemMessage(wc, "")
	if msg != "Base instructions here" {
		t.Errorf("BuildSystemMessage with no agent = %q, want base instructions", msg)
	}
}

func TestBuildSystemMessageFallback(t *testing.T) {
	wc := config.WorkspaceConfig{}
	msg := BuildSystemMessage(wc, "")
	if msg == "" {
		t.Error("BuildSystemMessage should return fallback when no workspace config")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
