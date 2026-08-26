package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	maxAnyTLSPaddingRules = 64
	maxAnyTLSPaddingSize  = 65535
	maxTunedPaddingSize   = 1320
	minTunedRangeWidth    = 32
	anyTLSTuneAttempts    = 20
)

const (
	AnyTLSPaddingModePreset = "preset"
	AnyTLSPaddingModeTuned  = "tuned"
	AnyTLSPaddingModeCustom = "custom"

	AnyTLSPaddingBalancedV1 = "balanced_v1"
	AnyTLSPaddingLightV1    = "light_v1"
)

// AnyTLSPaddingPreset is an immutable, versioned Controller-owned profile.
type AnyTLSPaddingPreset struct {
	ID          string   `json:"id"`
	Version     int      `json:"version"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Recommended bool     `json:"recommended"`
	Scheme      []string `json:"padding_scheme"`
}

// AnyTLSPaddingMetadata is persisted inside an inbound's ConfigJSON. It is
// Controller-only and is intentionally absent from all generated data-plane
// and subscription projections.
type AnyTLSPaddingMetadata struct {
	Mode          string `json:"mode"`
	PresetID      string `json:"preset_id,omitempty"`
	PresetVersion int    `json:"preset_version,omitempty"`
	Generation    int    `json:"generation"`
	GeneratedAt   string `json:"generated_at"`
	Fingerprint   string `json:"fingerprint"`
}

type AnyTLSPaddingOperation struct {
	Operation string   `json:"operation"`
	PresetID  string   `json:"preset_id,omitempty"`
	AutoTune  *bool    `json:"auto_tune,omitempty"`
	Scheme    []string `json:"padding_scheme,omitempty"`
}

var errAnyTLSPaddingTuneRetry = errors.New("retry AnyTLS padding tuning")

// AnyTLSBalancedPaddingScheme is OBoard's default AnyTLS padding profile.
func AnyTLSBalancedPaddingScheme() []string {
	return []string{
		"stop=8",
		"0=32-160",
		"1=180-480",
		"2=260-520,c,560-960,c,760-1280",
		"3=24-96,c,420-900",
		"4=360-980",
		"5=280-860",
		"6=420-1120",
		"7=300-940",
	}
}

// AnyTLSLightPaddingScheme is OBoard's low-overhead AnyTLS profile.
func AnyTLSLightPaddingScheme() []string {
	return []string{
		"stop=5",
		"0=16-80",
		"1=120-320",
		"2=320-620,c,580-980",
		"3=180-520",
		"4=220-620",
	}
}

// AnyTLSLargePaddingScheme remains as an internal compatibility name for the
// former node-preset kind. The current immutable profile is light_v1.
func AnyTLSLargePaddingScheme() []string {
	return AnyTLSLightPaddingScheme()
}

func AnyTLSPaddingPresets() []AnyTLSPaddingPreset {
	return []AnyTLSPaddingPreset{
		{ID: AnyTLSPaddingBalancedV1, Version: 1, Name: "均衡型", Description: "推荐；兼顾包长变化与额外开销", Recommended: true, Scheme: AnyTLSBalancedPaddingScheme()},
		{ID: AnyTLSPaddingLightV1, Version: 1, Name: "轻量型", Description: "适合高延迟、移动网络和短连接", Scheme: AnyTLSLightPaddingScheme()},
	}
}

func AnyTLSPaddingPresetByID(id string) (AnyTLSPaddingPreset, bool) {
	id = strings.TrimSpace(id)
	for _, preset := range AnyTLSPaddingPresets() {
		if preset.ID == id {
			preset.Scheme = append([]string(nil), preset.Scheme...)
			return preset, true
		}
	}
	return AnyTLSPaddingPreset{}, false
}

// PrepareAnyTLSPaddingForCreate resolves a create-time selection into one
// immutable ConfigJSON snapshot. Existing Controller metadata is treated as a
// copy source and never reused verbatim: tuned copies receive a fresh variant.
func PrepareAnyTLSPaddingForCreate(raw string, selection *model.AnyTLSPaddingInput, existingFingerprints map[string]struct{}, generatedAt time.Time) (string, error) {
	config, err := decodeAnyTLSPaddingConfig(raw)
	if err != nil {
		return "", err
	}
	previous, _ := anyTLSPaddingMetadataFromConfig(config)
	delete(config, "_oboard_padding")

	if selection == nil && previous != nil {
		switch previous.Mode {
		case AnyTLSPaddingModePreset, AnyTLSPaddingModeTuned:
			tune := previous.Mode == AnyTLSPaddingModeTuned
			selection = &model.AnyTLSPaddingInput{PresetID: previous.PresetID, AutoTune: &tune}
		case AnyTLSPaddingModeCustom:
			return storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: AnyTLSPaddingModeCustom, Generation: 1}, generatedAt)
		}
	}
	if selection == nil {
		if _, exists := config["padding_scheme"]; exists {
			return storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: AnyTLSPaddingModeCustom, Generation: 1}, generatedAt)
		}
		tune := true
		selection = &model.AnyTLSPaddingInput{PresetID: AnyTLSPaddingBalancedV1, AutoTune: &tune}
	}
	preset, ok := AnyTLSPaddingPresetByID(selection.PresetID)
	if !ok {
		return "", fmt.Errorf("unknown AnyTLS padding preset %q", selection.PresetID)
	}
	autoTune := true
	if selection.AutoTune != nil {
		autoTune = *selection.AutoTune
	}
	scheme := append([]string(nil), preset.Scheme...)
	mode := AnyTLSPaddingModePreset
	if autoTune {
		scheme, err = TuneAnyTLSPaddingScheme(preset.Scheme, existingFingerprints)
		if err != nil {
			return "", err
		}
		mode = AnyTLSPaddingModeTuned
	}
	config["padding_scheme"] = scheme
	return storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: mode, PresetID: preset.ID, PresetVersion: preset.Version, Generation: 1}, generatedAt)
}

// ApplyAnyTLSPaddingOperation performs the only supported post-create changes.
func ApplyAnyTLSPaddingOperation(raw string, operation AnyTLSPaddingOperation, existingFingerprints map[string]struct{}, generatedAt time.Time) (string, error) {
	config, err := decodeAnyTLSPaddingConfig(raw)
	if err != nil {
		return "", err
	}
	current, _ := anyTLSPaddingMetadataFromConfig(config)
	generation := 1
	if current != nil && current.Generation > 0 {
		generation = current.Generation + 1
	}
	switch strings.TrimSpace(operation.Operation) {
	case "replace_preset":
		preset, ok := AnyTLSPaddingPresetByID(operation.PresetID)
		if !ok {
			return "", fmt.Errorf("unknown AnyTLS padding preset %q", operation.PresetID)
		}
		autoTune := true
		if operation.AutoTune != nil {
			autoTune = *operation.AutoTune
		}
		scheme := append([]string(nil), preset.Scheme...)
		mode := AnyTLSPaddingModePreset
		if autoTune {
			scheme, err = TuneAnyTLSPaddingScheme(preset.Scheme, existingFingerprints)
			if err != nil {
				return "", err
			}
			mode = AnyTLSPaddingModeTuned
		}
		config["padding_scheme"] = scheme
		return storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: mode, PresetID: preset.ID, PresetVersion: preset.Version, Generation: generation}, generatedAt)
	case "regenerate":
		if current == nil || current.Mode == AnyTLSPaddingModeCustom || current.PresetID == "" {
			return "", errors.New("custom AnyTLS padding cannot be regenerated; choose a preset first")
		}
		preset, ok := AnyTLSPaddingPresetByID(current.PresetID)
		if !ok {
			return "", fmt.Errorf("unknown AnyTLS padding preset %q", current.PresetID)
		}
		scheme, tuneErr := TuneAnyTLSPaddingScheme(preset.Scheme, existingFingerprints)
		if tuneErr != nil {
			return "", tuneErr
		}
		config["padding_scheme"] = scheme
		return storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: AnyTLSPaddingModeTuned, PresetID: preset.ID, PresetVersion: preset.Version, Generation: generation}, generatedAt)
	case "set_custom":
		if err := ValidateAnyTLSPaddingScheme(operation.Scheme); err != nil {
			return "", err
		}
		if len(operation.Scheme) == 0 {
			return "", errors.New("custom AnyTLS padding_scheme is required")
		}
		config["padding_scheme"] = append([]string(nil), operation.Scheme...)
		return storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: AnyTLSPaddingModeCustom, Generation: generation}, generatedAt)
	default:
		return "", errors.New("operation must be replace_preset, regenerate, or set_custom")
	}
}

// MarkExistingAnyTLSPaddingCustom annotates a legacy snapshot without changing
// the scheme itself. Missing schemes remain untouched by the migration.
func MarkExistingAnyTLSPaddingCustom(raw string, generatedAt time.Time) (string, bool, error) {
	config, err := decodeAnyTLSPaddingConfig(raw)
	if err != nil {
		return "", false, err
	}
	if metadata, _ := anyTLSPaddingMetadataFromConfig(config); metadata != nil {
		return raw, false, nil
	}
	value, exists := config["padding_scheme"]
	if !exists {
		return raw, false, nil
	}
	if err := ValidateAnyTLSPaddingScheme(value); err != nil {
		return raw, false, nil
	}
	lines, _ := anyTLSPaddingLines(value)
	if len(lines) == 0 {
		return raw, false, nil
	}
	encoded, err := storeAnyTLSPaddingSnapshot(config, AnyTLSPaddingMetadata{Mode: AnyTLSPaddingModeCustom, Generation: 1}, generatedAt)
	return encoded, err == nil && encoded != raw, err
}

// PreserveAnyTLSPaddingSnapshot prevents an ordinary inbound PATCH from
// changing either the effective scheme or its Controller metadata.
func PreserveAnyTLSPaddingSnapshot(currentRaw, proposedRaw string) (string, error) {
	current, err := decodeAnyTLSPaddingConfig(currentRaw)
	if err != nil {
		return "", err
	}
	proposed, err := decodeAnyTLSPaddingConfig(proposedRaw)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"padding_scheme", "_oboard_padding"} {
		if value, exists := current[key]; exists {
			proposed[key] = value
		} else {
			delete(proposed, key)
		}
	}
	encoded, err := json.Marshal(proposed)
	return string(encoded), err
}

func AnyTLSPaddingMetadataFromJSON(raw string) (*AnyTLSPaddingMetadata, []string, error) {
	config, err := decodeAnyTLSPaddingConfig(raw)
	if err != nil {
		return nil, nil, err
	}
	metadata, err := anyTLSPaddingMetadataFromConfig(config)
	if err != nil {
		return nil, nil, err
	}
	lines, err := anyTLSPaddingLines(config["padding_scheme"])
	return metadata, append([]string(nil), lines...), err
}

func AnyTLSPaddingFingerprint(scheme any) (string, error) {
	canonical, err := canonicalAnyTLSPaddingScheme(scheme)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\n")))
	return hex.EncodeToString(digest[:]), nil
}

func TuneAnyTLSPaddingScheme(base []string, avoid map[string]struct{}) ([]string, error) {
	if err := ValidateAnyTLSPaddingScheme(base); err != nil {
		return nil, err
	}
	baseCost, err := anyTLSPaddingCost(base)
	if err != nil {
		return nil, err
	}
	var duplicate []string
	for attempt := 0; attempt < anyTLSTuneAttempts; attempt++ {
		candidate, tuneErr := tuneAnyTLSPaddingAttempt(base)
		if tuneErr != nil {
			if errors.Is(tuneErr, errAnyTLSPaddingTuneRetry) {
				continue
			}
			return nil, tuneErr
		}
		if err := ValidateAnyTLSPaddingScheme(candidate); err != nil {
			continue
		}
		cost, costErr := anyTLSPaddingCost(candidate)
		if costErr != nil || cost < baseCost*0.90 || cost > baseCost*1.10 || countAnyTLSPaddingEndpointChanges(base, candidate) < 3 {
			continue
		}
		fingerprint, fingerprintErr := AnyTLSPaddingFingerprint(candidate)
		if fingerprintErr != nil {
			return nil, fingerprintErr
		}
		if _, exists := avoid[fingerprint]; exists {
			duplicate = candidate
			continue
		}
		return candidate, nil
	}
	if duplicate != nil {
		return duplicate, nil
	}
	return nil, errors.New("unable to generate a valid AnyTLS padding variant after 20 attempts")
}

func tuneAnyTLSPaddingAttempt(base []string) ([]string, error) {
	out := make([]string, len(base))
	for lineIndex, line := range base {
		key, rule, _ := strings.Cut(line, "=")
		if key == "stop" {
			out[lineIndex] = line
			continue
		}
		tokens := strings.Split(rule, ",")
		previousCenter := -1.0
		for tokenIndex, token := range tokens {
			if token == "c" {
				continue
			}
			minimumText, maximumText, _ := strings.Cut(token, "-")
			minimum, _ := strconv.Atoi(minimumText)
			maximum, _ := strconv.Atoi(maximumText)
			center := float64(minimum+maximum) / 2
			width := float64(maximum - minimum)
			shiftBP, err := secureRandomInt(-800, 800)
			if err != nil {
				return nil, err
			}
			scaleBP, err := secureRandomInt(9200, 10800)
			if err != nil {
				return nil, err
			}
			newCenter := center * (1 + float64(shiftBP)/10000)
			newWidth := math.Max(minTunedRangeWidth, width*float64(scaleBP)/10000)
			newMinimum := int(math.Round(newCenter - newWidth/2))
			newMaximum := int(math.Round(newCenter + newWidth/2))
			if newMinimum < 1 {
				newMaximum += 1 - newMinimum
				newMinimum = 1
			}
			if newMaximum > maxTunedPaddingSize {
				newMinimum -= newMaximum - maxTunedPaddingSize
				newMaximum = maxTunedPaddingSize
			}
			if newMinimum < 1 {
				newMinimum = 1
			}
			if newMaximum-newMinimum < minTunedRangeWidth {
				newMaximum = min(maxTunedPaddingSize, newMinimum+minTunedRangeWidth)
				newMinimum = max(1, newMaximum-minTunedRangeWidth)
			}
			actualCenter := float64(newMinimum+newMaximum) / 2
			if previousCenter >= 0 && actualCenter <= previousCenter {
				return nil, errAnyTLSPaddingTuneRetry
			}
			previousCenter = actualCenter
			tokens[tokenIndex] = fmt.Sprintf("%d-%d", newMinimum, newMaximum)
		}
		out[lineIndex] = key + "=" + strings.Join(tokens, ",")
	}
	return out, nil
}

func secureRandomInt(minimum, maximum int64) (int64, error) {
	span := maximum - minimum + 1
	value, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, fmt.Errorf("read crypto random source: %w", err)
	}
	return minimum + value.Int64(), nil
}

func storeAnyTLSPaddingSnapshot(config map[string]any, metadata AnyTLSPaddingMetadata, generatedAt time.Time) (string, error) {
	if err := ValidateAnyTLSPaddingScheme(config["padding_scheme"]); err != nil {
		return "", err
	}
	lines, err := anyTLSPaddingLines(config["padding_scheme"])
	if err != nil || len(lines) == 0 {
		if err == nil {
			err = errors.New("AnyTLS padding_scheme is required")
		}
		return "", err
	}
	fingerprint, err := AnyTLSPaddingFingerprint(lines)
	if err != nil {
		return "", err
	}
	metadata.GeneratedAt = generatedAt.UTC().Format(time.RFC3339Nano)
	metadata.Fingerprint = fingerprint
	config["padding_scheme"] = append([]string(nil), lines...)
	config["_oboard_padding"] = metadata
	encoded, err := json.Marshal(config)
	return string(encoded), err
}

func decodeAnyTLSPaddingConfig(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil || config == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return nil, &ConfigFieldError{Path: "config_json", Problem: err.Error()}
	}
	return config, nil
}

func anyTLSPaddingMetadataFromConfig(config map[string]any) (*AnyTLSPaddingMetadata, error) {
	value, exists := config["_oboard_padding"]
	if !exists || value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var metadata AnyTLSPaddingMetadata
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

func canonicalAnyTLSPaddingScheme(value any) ([]string, error) {
	lines, err := anyTLSPaddingLines(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateAnyTLSPaddingScheme(lines); err != nil {
		return nil, err
	}
	stop := ""
	numbered := map[int]string{}
	keys := make([]int, 0, len(lines))
	for _, line := range lines {
		key, _, _ := strings.Cut(line, "=")
		if key == "stop" {
			stop = line
			continue
		}
		index, _ := strconv.Atoi(key)
		numbered[index] = line
		keys = append(keys, index)
	}
	sort.Ints(keys)
	out := []string{stop}
	for _, key := range keys {
		out = append(out, numbered[key])
	}
	return out, nil
}

func anyTLSPaddingCost(value any) (float64, error) {
	lines, err := anyTLSPaddingLines(value)
	if err != nil {
		return 0, err
	}
	cost := 0.0
	for _, line := range lines {
		key, rule, _ := strings.Cut(line, "=")
		if key == "stop" {
			continue
		}
		for _, token := range strings.Split(rule, ",") {
			if token == "c" {
				continue
			}
			minimumText, maximumText, _ := strings.Cut(token, "-")
			minimum, _ := strconv.Atoi(minimumText)
			maximum, _ := strconv.Atoi(maximumText)
			cost += float64(minimum+maximum) / 2
		}
	}
	return cost, nil
}

func countAnyTLSPaddingEndpointChanges(base, candidate []string) int {
	changes := 0
	for lineIndex := range base {
		if lineIndex >= len(candidate) {
			break
		}
		_, baseRule, _ := strings.Cut(base[lineIndex], "=")
		_, candidateRule, _ := strings.Cut(candidate[lineIndex], "=")
		baseTokens := strings.Split(baseRule, ",")
		candidateTokens := strings.Split(candidateRule, ",")
		for tokenIndex, baseToken := range baseTokens {
			if baseToken == "c" || tokenIndex >= len(candidateTokens) {
				continue
			}
			baseMinimum, baseMaximum, _ := strings.Cut(baseToken, "-")
			candidateMinimum, candidateMaximum, _ := strings.Cut(candidateTokens[tokenIndex], "-")
			if baseMinimum != candidateMinimum {
				changes++
			}
			if baseMaximum != candidateMaximum {
				changes++
			}
		}
	}
	return changes
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
