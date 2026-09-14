package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// LatencyProbeAppliedSnapshot is the probe plan a node reports it currently
// runs. It carries no targets: the Controller rendered the plan and only needs
// to know which one landed.
//
// Without it a rejected plan is invisible. The Agent refuses a plan whose
// version it already holds with different content, and it can only log that
// locally; the Controller keeps rendering the same version for the same content
// and keeps being refused, with no evidence on either side that the two
// disagree. Reporting the held identity is what makes that state detectable and
// therefore repairable.
type LatencyProbeAppliedSnapshot struct {
	PlanVersion int64  `json:"plan_version"`
	PlanDigest  string `json:"plan_digest,omitempty"`
}

// LatencyProbePlanContentDigest is the content identity of a rendered probe
// plan: everything an Agent compares except the version itself, so one version
// can never describe two different plans.
//
// Controller and Agent must produce the same value from the same plan. Both
// call this one function over the shared wire struct; a field added to the plan
// on one side only would change the digest there and nowhere else, which is the
// same synchronization requirement the operational core-config digest carries.
func LatencyProbePlanContentDigest(plan LatencyProbeTargetsPlan) string {
	plan.Version = 0
	raw, err := json.Marshal(plan)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
