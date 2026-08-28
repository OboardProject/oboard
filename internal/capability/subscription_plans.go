package capability

import (
	"context"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

func subscriptionPlanRefFromID(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "id", "subscription_plan")
}

func subscriptionPlanDeleteRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "plan_id", "subscription_plan")
}

func subscriptionPlanNodesUpdateRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	planID, ok := int64Value(object["plan_id"])
	if !ok || planID <= 0 {
		return nil, errors.New("plan_id must be a positive integer ID")
	}
	refs := []mcpauth.ResourceRef{{Type: "subscription_plan", ID: strconv.FormatInt(planID, 10)}}
	nodes, ok := object["nodes"].([]any)
	if !ok {
		return nil, errors.New("nodes must be an array")
	}
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("each node must be an object")
		}
		nodeType, _ := node["node_type"].(string)
		nodeID, valid := int64Value(node["node_id"])
		if !valid || nodeID <= 0 {
			return nil, errors.New("each node requires a positive node_id")
		}
		switch nodeType {
		case "inbound", "proxy_path", "external_outbound":
			refs = append(refs, mcpauth.ResourceRef{Type: nodeType, ID: strconv.FormatInt(nodeID, 10)})
		default:
			return nil, errors.New("invalid node_type")
		}
	}
	return refs, nil
}
