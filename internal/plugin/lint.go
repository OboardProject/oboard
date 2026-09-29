package plugin

import (
	"regexp"
	"sort"
)

var (
	sdkCallPattern = regexp.MustCompile(`\boboard\s*\.\s*([A-Za-z]+)\s*\.\s*([A-Za-z]+)\s*\(`)
	envUsePattern  = regexp.MustCompile(`\benv\s*(?:\.\s*(?:get|raw|has)\s*\(\s*["']([A-Z][A-Z0-9_]*)["']|\.\s*([A-Z][A-Z0-9_]*)\b|\[\s*["']([A-Z][A-Z0-9_]*)["']\s*\])`)
)

// LintSource is a text-level editor hint: it lists SDK methods whose
// capability the manifest does not declare and declared variables the code
// never mentions. It never parses into or executes plugin code, and it is
// not a security control; the gateway enforces capabilities on every call.
func LintSource(manifest Manifest, source string) (undeclared []string, unused []string) {
	missing := map[string]bool{}
	for _, match := range sdkCallPattern.FindAllStringSubmatch(source, 512) {
		method := match[1] + "." + match[2]
		if match[1] == "crypto" {
			continue
		}
		spec, ok := CapabilityForMethod(method)
		if !ok {
			continue
		}
		if !manifest.HasCapability(spec.Name) {
			missing[spec.Name] = true
		}
	}
	used := map[string]bool{}
	for _, match := range envUsePattern.FindAllStringSubmatch(source, 1024) {
		for _, name := range match[1:] {
			if name != "" {
				used[name] = true
			}
		}
	}
	undeclared = []string{}
	for name := range missing {
		undeclared = append(undeclared, name)
	}
	sort.Strings(undeclared)
	unused = []string{}
	for _, field := range manifest.Environment {
		if !used[field.Name] {
			unused = append(unused, field.Name)
		}
	}
	return undeclared, unused
}
