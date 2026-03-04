package recorder

import "testing"

func TestRankLocatorsOrdersByReplayPriority(t *testing.T) {
	t.Parallel()

	locators := []Locator{
		{Kind: "dompath", Value: "html > body > div:nth-child(2)"},
		{Kind: "css", Value: "button.primary"},
		{Kind: "role", Role: "button", Name: "Submit"},
		{Kind: "text", Value: "Submit", Match: "contains"},
		{Kind: "xpath", Value: "//button[1]"},
	}

	ranked := RankLocators(locators)
	if len(ranked) != 5 {
		t.Fatalf("expected 5 locators, got %d", len(ranked))
	}

	gotKinds := []string{ranked[0].Kind, ranked[1].Kind, ranked[2].Kind, ranked[3].Kind, ranked[4].Kind}
	wantKinds := []string{"role", "text", "css", "xpath", "dompath"}

	for idx := range wantKinds {
		if gotKinds[idx] != wantKinds[idx] {
			t.Fatalf("unexpected locator ranking at index %d: want %s, got %s", idx, wantKinds[idx], gotKinds[idx])
		}
	}
}

func TestExtractLocatorsNormalizesAndDedupes(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		"locators": []interface{}{
			map[string]interface{}{"kind": " css ", "value": "button.primary"},
			map[string]interface{}{"kind": "css", "value": "button.primary"},
			map[string]interface{}{"kind": "role", "role": "button", "name": "Save"},
			map[string]interface{}{"kind": "text", "value": "Save"},
			map[string]interface{}{"kind": "invalid", "value": "ignored"},
		},
	}

	locators := ExtractLocators(payload)
	if len(locators) != 3 {
		t.Fatalf("expected 3 locators after dedupe/filter, got %d", len(locators))
	}

	if locators[0].Kind != "role" {
		t.Fatalf("expected first locator to be role, got %s", locators[0].Kind)
	}
	if locators[1].Kind != "text" {
		t.Fatalf("expected second locator to be text, got %s", locators[1].Kind)
	}
	if locators[2].Kind != "css" {
		t.Fatalf("expected third locator to be css, got %s", locators[2].Kind)
	}
}
