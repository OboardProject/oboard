package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// ConfigSemanticDigest separates exact JSON identity from data-plane topology
// and runtime traffic policy. Traffic accounting fields live in
// `_oboard.rate_limits` and must not decide whether a proxy configuration
// reload or full deployment is required.
type ConfigSemanticDigest struct {
	ExactDigest         string
	DataPlaneDigest     string
	TrafficPolicyDigest string
}

// ConfigComparison is the three-way result of comparing two Controller desired
// configs. Exact equality is a strict JSON identity. Data-plane equality means
// listeners, routes, trusted-forward topology, and other non-traffic state
// match. Traffic-policy equality covers only `_oboard.rate_limits`.
type ConfigComparison struct {
	ExactEqual         bool
	DataPlaneEqual     bool
	TrafficPolicyEqual bool
}

// SemanticConfigDigest canonicalizes cfg and returns the three digests.
func SemanticConfigDigest(cfg string) (ConfigSemanticDigest, error) {
	var root any
	if err := json.Unmarshal([]byte(cfg), &root); err != nil {
		return ConfigSemanticDigest{}, err
	}
	exact, err := canonicalJSONSHA256(root)
	if err != nil {
		return ConfigSemanticDigest{}, err
	}
	dataPlane, err := canonicalJSONSHA256(stripRateLimits(cloneJSON(root)))
	if err != nil {
		return ConfigSemanticDigest{}, err
	}
	traffic, err := canonicalJSONSHA256(extractRateLimits(root))
	if err != nil {
		return ConfigSemanticDigest{}, err
	}
	return ConfigSemanticDigest{ExactDigest: exact, DataPlaneDigest: dataPlane, TrafficPolicyDigest: traffic}, nil
}

// CompareConfigSemantics compares two Controller-generated configs.
func CompareConfigSemantics(previous, next string) (ConfigComparison, error) {
	left, err := SemanticConfigDigest(previous)
	if err != nil {
		return ConfigComparison{}, err
	}
	right, err := SemanticConfigDigest(next)
	if err != nil {
		return ConfigComparison{}, err
	}
	return ConfigComparison{
		ExactEqual:         left.ExactDigest == right.ExactDigest,
		DataPlaneEqual:     left.DataPlaneDigest == right.DataPlaneDigest,
		TrafficPolicyEqual: left.TrafficPolicyDigest == right.TrafficPolicyDigest,
	}, nil
}

func stripRateLimits(value any) any {
	root, ok := value.(map[string]any)
	if !ok {
		return value
	}
	raw, ok := root["_oboard"]
	if !ok {
		return root
	}
	metadata, ok := raw.(map[string]any)
	if !ok {
		return root
	}
	delete(metadata, "rate_limits")
	if len(metadata) == 0 {
		delete(root, "_oboard")
	} else {
		root["_oboard"] = metadata
	}
	return root
}

func extractRateLimits(value any) any {
	root, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	raw, ok := root["_oboard"]
	if !ok {
		return map[string]any{}
	}
	metadata, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	limits, ok := metadata["rate_limits"]
	if !ok || limits == nil {
		return map[string]any{}
	}
	return limits
}

func cloneJSON(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if json.Unmarshal(encoded, &out) != nil {
		return value
	}
	return out
}

func canonicalJSONSHA256(value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(canonical)), nil
}
