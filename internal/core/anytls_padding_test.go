package core

import (
	cryptorand "crypto/rand"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAnyTLSPaddingPresetRegistryAndUntunedSnapshot(t *testing.T) {
	presets := AnyTLSPaddingPresets()
	if len(presets) != 2 || presets[0].ID != AnyTLSPaddingBalancedV1 || presets[1].ID != AnyTLSPaddingLightV1 {
		t.Fatalf("presets = %#v", presets)
	}
	for _, preset := range presets {
		if preset.Version != 1 {
			t.Fatalf("preset %s version = %d", preset.ID, preset.Version)
		}
		if err := ValidateAnyTLSPaddingScheme(preset.Scheme); err != nil {
			t.Fatalf("preset %s invalid: %v", preset.ID, err)
		}
	}
	autoTune := false
	raw, err := PrepareAnyTLSPaddingForCreate(`{"tls":{"enabled":true}}`, &model.AnyTLSPaddingInput{PresetID: AnyTLSPaddingLightV1, AutoTune: &autoTune}, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	metadata, scheme, err := AnyTLSPaddingMetadataFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Mode != AnyTLSPaddingModePreset || metadata.PresetID != AnyTLSPaddingLightV1 || metadata.Generation != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if strings.Join(scheme, "\n") != strings.Join(AnyTLSLightPaddingScheme(), "\n") {
		t.Fatalf("untuned scheme changed:\n%s", strings.Join(scheme, "\n"))
	}
}

func TestTuneAnyTLSPaddingSchemePreservesStructureAndBounds(t *testing.T) {
	for _, base := range [][]string{AnyTLSBalancedPaddingScheme(), AnyTLSLightPaddingScheme()} {
		baseCost, _ := anyTLSPaddingCost(base)
		seen := map[string]bool{}
		for iteration := 0; iteration < 8; iteration++ {
			candidate, err := TuneAnyTLSPaddingScheme(base, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertAnyTLSPaddingStructure(t, base, candidate)
			cost, _ := anyTLSPaddingCost(candidate)
			if cost < baseCost*0.90 || cost > baseCost*1.10 {
				t.Fatalf("cost %.2f outside range for %.2f", cost, baseCost)
			}
			fingerprint, _ := AnyTLSPaddingFingerprint(candidate)
			seen[fingerprint] = true
		}
		if len(seen) < 2 {
			t.Fatalf("generated variants did not produce different fingerprints: %#v", seen)
		}
	}
}

func TestAnyTLSPaddingCreateCopyAndExplicitOperations(t *testing.T) {
	now := time.Unix(200, 0)
	first, err := PrepareAnyTLSPaddingForCreate(`{"tls":{"enabled":true}}`, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	firstMeta, firstScheme, _ := AnyTLSPaddingMetadataFromJSON(first)
	copy, err := PrepareAnyTLSPaddingForCreate(first, nil, map[string]struct{}{firstMeta.Fingerprint: {}}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	copyMeta, _, _ := AnyTLSPaddingMetadataFromJSON(copy)
	if copyMeta.Generation != 1 || copyMeta.Fingerprint == firstMeta.Fingerprint {
		t.Fatalf("copy metadata = %#v, source = %#v", copyMeta, firstMeta)
	}
	regenerated, err := ApplyAnyTLSPaddingOperation(first, AnyTLSPaddingOperation{Operation: "regenerate"}, map[string]struct{}{firstMeta.Fingerprint: {}}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	regeneratedMeta, _, _ := AnyTLSPaddingMetadataFromJSON(regenerated)
	if regeneratedMeta.Generation != 2 || regeneratedMeta.Fingerprint == firstMeta.Fingerprint {
		t.Fatalf("regenerated metadata = %#v", regeneratedMeta)
	}
	autoTune := false
	replaced, err := ApplyAnyTLSPaddingOperation(regenerated, AnyTLSPaddingOperation{Operation: "replace_preset", PresetID: AnyTLSPaddingLightV1, AutoTune: &autoTune}, nil, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replacedMeta, replacedScheme, _ := AnyTLSPaddingMetadataFromJSON(replaced)
	if replacedMeta.Generation != 3 || replacedMeta.Mode != AnyTLSPaddingModePreset || strings.Join(replacedScheme, "\n") != strings.Join(AnyTLSLightPaddingScheme(), "\n") {
		t.Fatalf("replaced metadata/scheme = %#v %#v", replacedMeta, replacedScheme)
	}
	custom, err := ApplyAnyTLSPaddingOperation(replaced, AnyTLSPaddingOperation{Operation: "set_custom", Scheme: firstScheme}, nil, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	customMeta, _, _ := AnyTLSPaddingMetadataFromJSON(custom)
	if customMeta.Mode != AnyTLSPaddingModeCustom || customMeta.Generation != 4 {
		t.Fatalf("custom metadata = %#v", customMeta)
	}
	if _, err := ApplyAnyTLSPaddingOperation(custom, AnyTLSPaddingOperation{Operation: "regenerate"}, nil, now); err == nil {
		t.Fatal("custom regenerate must fail")
	}
}

func TestAnyTLSPaddingOrdinaryUpdateAndLegacyAnnotation(t *testing.T) {
	raw, err := PrepareAnyTLSPaddingForCreate(`{"tls":{"enabled":true},"password":"secret"}`, nil, nil, time.Unix(300, 0))
	if err != nil {
		t.Fatal(err)
	}
	metadata, scheme, _ := AnyTLSPaddingMetadataFromJSON(raw)
	preserved, err := PreserveAnyTLSPaddingSnapshot(raw, `{"tls":{"enabled":true,"server_name":"new.example"},"padding_scheme":["stop=1","0=1-2"],"_oboard_padding":{"mode":"custom"}}`)
	if err != nil {
		t.Fatal(err)
	}
	gotMeta, gotScheme, _ := AnyTLSPaddingMetadataFromJSON(preserved)
	if gotMeta.Fingerprint != metadata.Fingerprint || strings.Join(gotScheme, "\n") != strings.Join(scheme, "\n") || !strings.Contains(preserved, "new.example") {
		t.Fatalf("ordinary update did not preserve snapshot: %s", preserved)
	}
	legacy := `{"tls":{"enabled":true},"padding_scheme":["stop=2","0=16-80","1=120-320"]}`
	annotated, changed, err := MarkExistingAnyTLSPaddingCustom(legacy, time.Unix(400, 0))
	if err != nil || !changed {
		t.Fatalf("legacy annotation changed=%v err=%v", changed, err)
	}
	legacyMeta, legacyScheme, _ := AnyTLSPaddingMetadataFromJSON(annotated)
	if legacyMeta.Mode != AnyTLSPaddingModeCustom || strings.Join(legacyScheme, "\n") != "stop=2\n0=16-80\n1=120-320" {
		t.Fatalf("legacy annotation = %#v %#v", legacyMeta, legacyScheme)
	}
	missing := `{"tls":{"enabled":true}}`
	if got, changed, err := MarkExistingAnyTLSPaddingCustom(missing, time.Now()); err != nil || changed || got != missing {
		t.Fatalf("missing scheme changed: %q %v %v", got, changed, err)
	}
}

func TestAnyTLSPaddingCryptoFailureAborts(t *testing.T) {
	original := cryptorand.Reader
	cryptorand.Reader = failingRandomReader{}
	defer func() { cryptorand.Reader = original }()
	if _, err := TuneAnyTLSPaddingScheme(AnyTLSBalancedPaddingScheme(), nil); err == nil || !strings.Contains(err.Error(), "crypto random") {
		t.Fatalf("crypto failure = %v", err)
	}
}

type failingRandomReader struct{}

func (failingRandomReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func assertAnyTLSPaddingStructure(t *testing.T, base, candidate []string) {
	t.Helper()
	if len(base) != len(candidate) {
		t.Fatalf("line count changed: %d != %d", len(base), len(candidate))
	}
	for lineIndex := range base {
		baseKey, baseRule, _ := strings.Cut(base[lineIndex], "=")
		candidateKey, candidateRule, _ := strings.Cut(candidate[lineIndex], "=")
		if baseKey != candidateKey {
			t.Fatalf("line %d key changed: %q != %q", lineIndex, baseKey, candidateKey)
		}
		if baseKey == "stop" {
			if baseRule != candidateRule {
				t.Fatalf("stop changed: %q != %q", baseRule, candidateRule)
			}
			continue
		}
		baseTokens := strings.Split(baseRule, ",")
		candidateTokens := strings.Split(candidateRule, ",")
		if len(baseTokens) != len(candidateTokens) {
			t.Fatalf("line %d token count changed", lineIndex)
		}
		previousCenter := -1
		for tokenIndex := range baseTokens {
			if baseTokens[tokenIndex] == "c" {
				if candidateTokens[tokenIndex] != "c" {
					t.Fatalf("line %d c marker moved", lineIndex)
				}
				continue
			}
			minimumText, maximumText, ok := strings.Cut(candidateTokens[tokenIndex], "-")
			minimum, minErr := strconv.Atoi(minimumText)
			maximum, maxErr := strconv.Atoi(maximumText)
			if !ok || minErr != nil || maxErr != nil || minimum <= 0 || maximum <= minimum || maximum > maxTunedPaddingSize || maximum-minimum < minTunedRangeWidth {
				t.Fatalf("invalid tuned range %q", candidateTokens[tokenIndex])
			}
			center := minimum + maximum
			if center <= previousCenter {
				t.Fatalf("line %d center order changed", lineIndex)
			}
			previousCenter = center
		}
	}
}
