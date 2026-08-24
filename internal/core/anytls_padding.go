package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	maxAnyTLSPaddingRules = 64
	maxAnyTLSPaddingSize  = 65535
)

// AnyTLSBalancedPaddingScheme is OBoard's default AnyTLS padding profile.
func AnyTLSBalancedPaddingScheme() []string {
	return []string{
		"stop=8",
		"0=64-128",
		"1=200-450",
		"2=450-650,c,700-1100,c,700-1100",
		"3=32-96,600-900",
		"4=450-850",
		"5=500-900",
		"6=550-950",
		"7=600-1000",
	}
}

// AnyTLSLargePaddingScheme is OBoard's lower-rule-count, larger-record profile.
func AnyTLSLargePaddingScheme() []string {
	return []string{
		"stop=3",
		"0=900-1400",
		"1=900-1400",
		"2=900-1400",
	}
}

// AnyTLSUpstreamDefaultPaddingScheme records the pinned sing-anytls fallback.
func AnyTLSUpstreamDefaultPaddingScheme() []string {
	return []string{
		"stop=8",
		"0=30-30",
		"1=100-400",
		"2=400-500,c,500-1000,c,500-1000,c,500-1000,c,500-1000",
		"3=9-9,500-1000",
		"4=500-1000",
		"5=500-1000",
		"6=500-1000",
		"7=500-1000",
	}
}

// ValidateAnyTLSPaddingScheme validates the strict subset emitted by OBoard.
// A missing or empty value keeps sing-anytls's upstream default.
func ValidateAnyTLSPaddingScheme(value any) error {
	lines, err := anyTLSPaddingLines(value)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > maxAnyTLSPaddingRules+1 {
		return fmt.Errorf("anytls padding_scheme supports at most %d numbered rules", maxAnyTLSPaddingRules)
	}

	stop := 0
	stopSeen := false
	numberedRules := map[int]bool{}
	for lineIndex, line := range lines {
		if line == "" || line != strings.TrimSpace(line) {
			return fmt.Errorf("anytls padding_scheme line %d must be non-empty and contain no surrounding whitespace", lineIndex+1)
		}
		if strings.Count(line, "=") != 1 {
			return fmt.Errorf("anytls padding_scheme line %d must use key=value", lineIndex+1)
		}
		key, rule, _ := strings.Cut(line, "=")
		if key == "stop" {
			if stopSeen {
				return errors.New("anytls padding_scheme must contain exactly one stop rule")
			}
			stop, err = parseAnyTLSPositiveDecimal(rule)
			if err != nil || stop > maxAnyTLSPaddingRules {
				return fmt.Errorf("anytls padding_scheme stop must be between 1 and %d", maxAnyTLSPaddingRules)
			}
			stopSeen = true
			continue
		}

		packetIndex, parseErr := strconv.Atoi(key)
		if parseErr != nil || packetIndex < 0 || packetIndex >= maxAnyTLSPaddingRules || strconv.Itoa(packetIndex) != key {
			return fmt.Errorf("anytls padding_scheme line %d has an invalid packet index", lineIndex+1)
		}
		if numberedRules[packetIndex] {
			return fmt.Errorf("anytls padding_scheme packet index %d is duplicated", packetIndex)
		}
		if err := validateAnyTLSPaddingRule(packetIndex, rule); err != nil {
			return err
		}
		numberedRules[packetIndex] = true
	}
	if !stopSeen {
		return errors.New("anytls padding_scheme requires a stop rule")
	}
	if len(numberedRules) == 0 {
		return errors.New("anytls padding_scheme requires at least one numbered rule")
	}
	for packetIndex := range numberedRules {
		if packetIndex >= stop {
			return fmt.Errorf("anytls padding_scheme packet index %d must be lower than stop=%d", packetIndex, stop)
		}
	}
	return nil
}

func anyTLSPaddingLines(value any) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		if typed == "" {
			return nil, nil
		}
		return strings.Split(strings.ReplaceAll(typed, "\r\n", "\n"), "\n"), nil
	case []string:
		return typed, nil
	case []any:
		lines := make([]string, 0, len(typed))
		for index, item := range typed {
			line, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("anytls padding_scheme item %d must be a string", index+1)
			}
			lines = append(lines, line)
		}
		return lines, nil
	default:
		return nil, errors.New("anytls padding_scheme must be a string or string array")
	}
}

func validateAnyTLSPaddingRule(packetIndex int, rule string) error {
	if rule == "" || rule != strings.TrimSpace(rule) {
		return fmt.Errorf("anytls padding_scheme packet %d has an empty or whitespace-padded rule", packetIndex)
	}
	tokens := strings.Split(rule, ",")
	if packetIndex == 0 && len(tokens) != 1 {
		return errors.New("anytls padding_scheme packet 0 must contain exactly one size range")
	}
	previousCheck := false
	for index, token := range tokens {
		if token == "c" {
			if packetIndex == 0 || index == 0 || index == len(tokens)-1 || previousCheck {
				return fmt.Errorf("anytls padding_scheme packet %d has an invalid c marker", packetIndex)
			}
			previousCheck = true
			continue
		}
		minimumText, maximumText, ok := strings.Cut(token, "-")
		if !ok || strings.Contains(maximumText, "-") {
			return fmt.Errorf("anytls padding_scheme packet %d size %q must use min-max", packetIndex, token)
		}
		minimum, minErr := parseAnyTLSPositiveDecimal(minimumText)
		maximum, maxErr := parseAnyTLSPositiveDecimal(maximumText)
		if minErr != nil || maxErr != nil || minimum > maximum || maximum > maxAnyTLSPaddingSize {
			return fmt.Errorf("anytls padding_scheme packet %d size %q must satisfy 1 <= min <= max <= %d", packetIndex, token, maxAnyTLSPaddingSize)
		}
		previousCheck = false
	}
	return nil
}

func parseAnyTLSPositiveDecimal(value string) (int, error) {
	if value == "" {
		return 0, errors.New("empty decimal")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid decimal")
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("decimal must be positive")
	}
	return parsed, nil
}
