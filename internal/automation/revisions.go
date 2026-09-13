package automation

import (
	"context"
	"encoding/json"
)

type approvedRevisionsContextKey struct{}

// ApprovedResourceRevision carries the validated plan version into the domain
// transaction. A second preflight read alone cannot protect the commit boundary.
func ApprovedResourceRevision(ctx context.Context, resource string) (string, bool) {
	raw, ok := ctx.Value(approvedRevisionsContextKey{}).(json.RawMessage)
	if !ok {
		return "", false
	}
	var revisions map[string]string
	if json.Unmarshal(raw, &revisions) != nil {
		return "", false
	}
	value, ok := revisions[resource]
	return value, ok
}
