package ai

import "testing"

func TestStripThinkingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no thinking tags",
			input: `{"category":"study","confidence":0.95,"title":"Test"}`,
			want:  `{"category":"study","confidence":0.95,"title":"Test"}`,
		},
		{
			name:  "think tags with JSON after",
			input: "<think>\nI need to classify this...\nLet me think about categories.\n</think>\n\n{\"category\":\"journal\",\"confidence\":0.95,\"title\":\"Trust\",\"fields\":{},\"tags\":[\"trust\"]}",
			want:  `{"category":"journal","confidence":0.95,"title":"Trust","fields":{},"tags":["trust"]}`,
		},
		{
			name:  "unclosed think tag (token limit mid-thought)",
			input: "<think>\nI need to classify this but ran out of tokens...",
			want:  "",
		},
		{
			name:  "Thinking Process rendered format with JSON after",
			input: "Thinking Process:\n\n1. Analyze the request...\n2. Determine category...\n\n{\"category\":\"study\",\"confidence\":0.9,\"title\":\"Test\"}",
			want:  `{"category":"study","confidence":0.9,"title":"Test"}`,
		},
		{
			name:  "Thinking Process with no JSON",
			input: "Thinking Process:\n\n1. Analyze the request...\n2. Still thinking...",
			want:  "",
		},
		{
			name:  "think tags with whitespace around JSON",
			input: "<think>reasoning here</think>\n\n  {\"category\":\"actions\"}\n\n",
			want:  `{"category":"actions"}`,
		},
		{
			name:  "multiple think blocks",
			input: "<think>first thought</think>\n<think>second thought</think>\n{\"category\":\"ideas\"}",
			want:  `{"category":"ideas"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripThinkingContent(tt.input)
			if got != tt.want {
				t.Errorf("stripThinkingContent() =\n  %q\nwant:\n  %q", got, tt.want)
			}
		})
	}
}

func TestStripJSONFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no fences",
			input: `{"test": true}`,
			want:  `{"test": true}`,
		},
		{
			name:  "json fences",
			input: "```json\n{\"test\": true}\n```",
			want:  `{"test": true}`,
		},
		{
			name:  "plain fences",
			input: "```\n{\"test\": true}\n```",
			want:  `{"test": true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripJSONFences(tt.input)
			if got != tt.want {
				t.Errorf("stripJSONFences() = %q, want %q", got, tt.want)
			}
		})
	}
}
