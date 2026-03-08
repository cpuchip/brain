package classifier

import (
	"encoding/json"
	"testing"
)

func TestRescueSubItems_NestedInFields(t *testing.T) {
	// Model puts sub_items inside fields instead of top level
	raw := `{
		"category": "projects",
		"confidence": 0.9,
		"title": "Deploy Steps",
		"fields": {
			"status": "active",
			"next_action": "Run tests",
			"sub_items": ["Run tests", "Build Docker image", "Push to registry"]
		},
		"tags": ["deploy"]
	}`

	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}

	if len(result.SubItems) != 0 {
		t.Fatalf("expected 0 sub_items before rescue, got %d", len(result.SubItems))
	}

	rescueSubItems(&result, []byte(raw))

	if len(result.SubItems) != 3 {
		t.Fatalf("expected 3 sub_items after rescue, got %d: %v", len(result.SubItems), result.SubItems)
	}
	if result.SubItems[0] != "Run tests" {
		t.Errorf("expected first item 'Run tests', got %q", result.SubItems[0])
	}
}

func TestRescueSubItems_FollowUpsCommaList(t *testing.T) {
	result := Result{
		Category: "actions",
		Fields: Fields{
			FollowUps: "Mow the lawn, Fix the fence, Clean the garage, Wash the car",
		},
	}

	rescueSubItems(&result, mustMarshal(t, result))

	if len(result.SubItems) != 4 {
		t.Fatalf("expected 4 sub_items, got %d: %v", len(result.SubItems), result.SubItems)
	}
	if result.Fields.FollowUps != "" {
		t.Errorf("expected follow_ups cleared, got %q", result.Fields.FollowUps)
	}
}

func TestRescueSubItems_ReferencesCommaList(t *testing.T) {
	result := Result{
		Category: "study",
		Fields: Fields{
			References: "Alma 32:21, Hebrews 11:1, Ether 12:6, Moroni 7:33",
		},
	}

	rescueSubItems(&result, mustMarshal(t, result))

	if len(result.SubItems) != 4 {
		t.Fatalf("expected 4 sub_items, got %d: %v", len(result.SubItems), result.SubItems)
	}
	// references field should be preserved (still valid as summary)
	if result.Fields.References == "" {
		t.Error("expected references field to be preserved")
	}
}

func TestRescueSubItems_FollowUpsNewlineList(t *testing.T) {
	result := Result{
		Category: "actions",
		Fields: Fields{
			FollowUps: "1. Milk\n2. Bread\n3. Eggs\n4. Butter",
		},
	}

	rescueSubItems(&result, mustMarshal(t, result))

	if len(result.SubItems) != 4 {
		t.Fatalf("expected 4 sub_items, got %d: %v", len(result.SubItems), result.SubItems)
	}
	if result.SubItems[0] != "Milk" {
		t.Errorf("expected 'Milk', got %q", result.SubItems[0])
	}
}

func TestRescueSubItems_NoRescueWhenPresent(t *testing.T) {
	result := Result{
		Category: "actions",
		SubItems: []string{"already", "here"},
		Fields: Fields{
			FollowUps: "some, other, things, listed",
		},
	}

	rescueSubItems(&result, mustMarshal(t, result))

	if len(result.SubItems) != 2 {
		t.Fatalf("should not modify existing sub_items, got %d", len(result.SubItems))
	}
}

func TestRescueSubItems_NoRescueShortList(t *testing.T) {
	result := Result{
		Category: "actions",
		Fields: Fields{
			FollowUps: "just two items, not enough",
		},
	}

	rescueSubItems(&result, mustMarshal(t, result))

	if len(result.SubItems) != 0 {
		t.Fatalf("should not rescue fewer than 3 items, got %d", len(result.SubItems))
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
