package core

import "strings"

func ResolveEffectiveNodeName(sourceName string, globalOverride, planOverride *string) string {
	if planOverride != nil {
		return strings.TrimSpace(*planOverride)
	}
	if globalOverride != nil {
		return strings.TrimSpace(*globalOverride)
	}
	return strings.TrimSpace(sourceName)
}
