package recorder

import (
	"sort"
	"strings"
)

func RankLocators(locators []Locator) []Locator {
	ranked := make([]Locator, 0, len(locators))
	for _, locator := range locators {
		if normalizeLocator(locator) == (Locator{}) {
			continue
		}
		ranked = append(ranked, normalizeLocator(locator))
	}

	sort.SliceStable(ranked, func(i int, j int) bool {
		left := locatorPriority(ranked[i])
		right := locatorPriority(ranked[j])
		if left == right {
			return len(locatorKey(ranked[i])) < len(locatorKey(ranked[j]))
		}

		return left < right
	})

	return dedupeLocators(ranked)
}

func ExtractLocators(payload map[string]interface{}) []Locator {
	if payload == nil {
		return nil
	}

	raw, ok := payload["locators"]
	if !ok {
		return nil
	}

	rawLocators, ok := raw.([]interface{})
	if !ok {
		return nil
	}

	locators := make([]Locator, 0, len(rawLocators))
	for _, item := range rawLocators {
		asMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		locator := Locator{
			Kind:  strings.TrimSpace(toString(asMap["kind"])),
			Value: strings.TrimSpace(toString(asMap["value"])),
			Role:  strings.TrimSpace(toString(asMap["role"])),
			Name:  strings.TrimSpace(toString(asMap["name"])),
			Match: strings.TrimSpace(toString(asMap["match"])),
		}
		locators = append(locators, locator)
	}

	return RankLocators(locators)
}

func normalizeLocator(locator Locator) Locator {
	locator.Kind = strings.ToLower(strings.TrimSpace(locator.Kind))
	locator.Value = strings.TrimSpace(locator.Value)
	locator.Role = strings.TrimSpace(locator.Role)
	locator.Name = strings.TrimSpace(locator.Name)
	locator.Match = strings.TrimSpace(strings.ToLower(locator.Match))

	switch locator.Kind {
	case "role":
		if locator.Role == "" {
			return Locator{}
		}
		return locator
	case "css", "xpath", "dompath", "text":
		if locator.Value == "" {
			return Locator{}
		}
		if locator.Kind == "text" && locator.Match == "" {
			locator.Match = "contains"
		}
		return locator
	default:
		return Locator{}
	}
}

func locatorPriority(locator Locator) int {
	switch locator.Kind {
	case "role":
		return 1
	case "text":
		return 2
	case "css":
		return 3
	case "xpath":
		return 4
	case "dompath":
		return 5
	default:
		return 99
	}
}

func locatorKey(locator Locator) string {
	parts := []string{locator.Kind, locator.Value, locator.Role, locator.Name, locator.Match}
	return strings.Join(parts, "|")
}

func dedupeLocators(locators []Locator) []Locator {
	seen := make(map[string]struct{}, len(locators))
	unique := make([]Locator, 0, len(locators))

	for _, locator := range locators {
		key := locatorKey(locator)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, locator)
	}

	return unique
}

func toString(input interface{}) string {
	if input == nil {
		return ""
	}

	if asString, ok := input.(string); ok {
		return asString
	}

	return ""
}
