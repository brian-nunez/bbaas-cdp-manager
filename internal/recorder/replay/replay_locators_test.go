package replay

import "testing"

func TestExtractLocatorsForTypePrefersSpecificSelectors(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		"locators": []interface{}{
			map[string]interface{}{"kind": "role", "role": "textbox", "name": ""},
			map[string]interface{}{"kind": "css", "value": "input[name='password']"},
			map[string]interface{}{"kind": "xpath", "value": "//input[@name='password']"},
			map[string]interface{}{"kind": "text", "value": "Password"},
		},
	}

	locators := extractLocators(payload, "type")
	if len(locators) != 2 {
		t.Fatalf("expected 2 locators after filtering, got %d", len(locators))
	}

	if locators[0].Kind != "css" {
		t.Fatalf("expected first locator kind css, got %s", locators[0].Kind)
	}
	if locators[1].Kind != "xpath" {
		t.Fatalf("expected second locator kind xpath, got %s", locators[1].Kind)
	}
}

func TestExtractLocatorsForClickKeepsRolePriority(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		"locators": []interface{}{
			map[string]interface{}{"kind": "css", "value": "button#submit"},
			map[string]interface{}{"kind": "role", "role": "button", "name": "Submit"},
		},
	}

	locators := extractLocators(payload, "click")
	if len(locators) != 2 {
		t.Fatalf("expected 2 locators, got %d", len(locators))
	}

	if locators[0].Kind != "role" {
		t.Fatalf("expected role locator first for click actions, got %s", locators[0].Kind)
	}
}
