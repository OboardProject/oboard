package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// Resource resolvers extract mcpauth.ResourceRef from operation input so the
// unified MCP evaluator can enforce the grant's resource boundary. They never
// look up state; they only map input fields to resource references. Unknown
// input is rejected so a denied boundary can never be bypassed with a crafted
// object.

func serverRefsFromIDs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromInt64Slice(ctx, input, "server_ids", "server")
}

func serverRefFromID(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "id", "server")
}

func userRefsFromIDs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromInt64Slice(ctx, input, "user_ids", "user")
}

func userRefFromID(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "user_id", "user")
}

func auditIncidentRefFromID(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, ok := input.(map[string]any)
	if !ok {
		decoded, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		object = decoded
	}
	id, ok := object["id"].(string)
	if !ok || id == "" {
		return nil, errors.New("audit incident input requires a non-empty id")
	}
	return []mcpauth.ResourceRef{{Type: "audit_incident", ID: id}}, nil
}

func serverOnboardRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	// Server creation has no existing resource ref; the boundary's
	// allow_create flag is checked by the evaluator through a dedicated
	// creation ref.
	return nil, nil
}

func serverUpdateRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "server_id", "server")
}

func inboundCreateRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	inbound, ok := object["inbound"].(map[string]any)
	if !ok {
		return nil, errors.New("inbound must be an object")
	}
	serverID, ok := int64Value(inbound["server_id"])
	if !ok || serverID <= 0 {
		return nil, errors.New("inbound.server_id must be a positive integer ID")
	}
	return []mcpauth.ResourceRef{{Type: "server", ID: strconv.FormatInt(serverID, 10)}}, nil
}

func inboundUpdateRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	inboundID, ok := int64Value(object["inbound_id"])
	if !ok || inboundID <= 0 {
		return nil, errors.New("inbound_id must be a positive integer ID")
	}
	refs := []mcpauth.ResourceRef{{Type: "inbound", ID: strconv.FormatInt(inboundID, 10)}}
	if changes, ok := object["changes"].(map[string]any); ok {
		if serverID, present := int64Value(changes["server_id"]); present && serverID > 0 {
			refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(serverID, 10)})
		}
	}
	return refs, nil
}

func proxyPathUpdateRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	pathID, ok := int64Value(object["path_id"])
	if !ok || pathID <= 0 {
		return nil, errors.New("path_id must be a positive integer ID")
	}
	refs := []mcpauth.ResourceRef{{Type: "proxy_path", ID: strconv.FormatInt(pathID, 10)}}
	if changes, ok := object["changes"].(map[string]any); ok {
		if inboundID, present := int64Value(changes["inbound_id"]); present && inboundID > 0 {
			refs = append(refs, mcpauth.ResourceRef{Type: "inbound", ID: strconv.FormatInt(inboundID, 10)})
		}
	}
	return refs, nil
}

func proxyPathDirectRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	if inboundID, ok := int64Value(object["inbound_id"]); ok && inboundID > 0 {
		refs = append(refs, mcpauth.ResourceRef{Type: "inbound", ID: strconv.FormatInt(inboundID, 10)})
	}
	if pathID, ok := int64Value(object["source_path_id"]); ok && pathID > 0 {
		refs = append(refs, mcpauth.ResourceRef{Type: "proxy_path", ID: strconv.FormatInt(pathID, 10)})
	}
	if len(refs) == 0 {
		return nil, errors.New("inbound_id or source_path_id is required")
	}
	return refs, nil
}

func proxyPathStepWriteRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	step, _ := object["step"].(map[string]any)
	if step == nil {
		step, _ = object["changes"].(map[string]any)
	}
	for field, resourceType := range map[string]string{"path_id": "proxy_path", "server_id": "server", "inbound_id": "inbound", "external_outbound_id": "external_outbound"} {
		if id, ok := int64Value(step[field]); ok && id > 0 {
			refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
		}
	}
	return refs, nil
}

func inboundDeleteRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "inbound_id", "inbound")
}

func proxyPathDeleteRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "path_id", "proxy_path")
}

func proxyPathStepTruncateRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return refsFromSingleID(ctx, input, "path_id", "proxy_path")
}

func topologyWriteRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	if steps, ok := object["steps"].([]any); ok {
		for _, raw := range steps {
			step, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if serverID, ok := int64Value(step["server_id"]); ok {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(serverID, 10)})
			}
			if inboundID, ok := int64Value(step["inbound_id"]); ok {
				refs = append(refs, mcpauth.ResourceRef{Type: "inbound", ID: strconv.FormatInt(inboundID, 10)})
			}
			if outboundID, ok := int64Value(step["external_outbound_id"]); ok {
				refs = append(refs, mcpauth.ResourceRef{Type: "external_outbound", ID: strconv.FormatInt(outboundID, 10)})
			}
		}
	}
	return refs, nil
}

func topologyReuseInboundRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	if target, ok := int64Value(object["target_server_id"]); ok {
		refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(target, 10)})
	}
	if targetInbound, ok := int64Value(object["target_inbound_id"]); ok {
		refs = append(refs, mcpauth.ResourceRef{Type: "inbound", ID: strconv.FormatInt(targetInbound, 10)})
	}
	if sources, ok := object["sources"].([]any); ok {
		for _, raw := range sources {
			source, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if inboundID, ok := int64Value(source["inbound_id"]); ok {
				refs = append(refs, mcpauth.ResourceRef{Type: "inbound", ID: strconv.FormatInt(inboundID, 10)})
			}
		}
	}
	return refs, nil
}

func proxyPathPlanRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	if entry, ok := int64Value(object["entry_server_id"]); ok {
		refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(entry, 10)})
	}
	if avoid, ok := object["avoid_server_ids"].([]any); ok {
		for _, raw := range avoid {
			if id, ok := int64Value(raw); ok {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		}
	}
	return refs, nil
}

func incidentResponseRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	if id, ok := object["incident_id"].(string); ok && id != "" {
		refs = append(refs, mcpauth.ResourceRef{Type: "audit_incident", ID: id})
	}
	if userID, ok := int64Value(object["user_id"]); ok {
		refs = append(refs, mcpauth.ResourceRef{Type: "user", ID: strconv.FormatInt(userID, 10)})
	}
	return refs, nil
}

func noRefs(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	return nil, nil
}

func refsFromInt64Slice(_ context.Context, input any, field, resourceType string) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	raw, ok := object[field]
	if !ok {
		return []mcpauth.ResourceRef{}, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New(field + " must be an array of integer IDs")
	}
	refs := make([]mcpauth.ResourceRef, 0, len(list))
	for _, item := range list {
		id, ok := int64Value(item)
		if !ok || id <= 0 {
			return nil, errors.New(field + " contains an invalid ID")
		}
		refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
	}
	return refs, nil
}

func refsFromSingleID(_ context.Context, input any, field, resourceType string) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	id, ok := int64Value(object[field])
	if !ok || id <= 0 {
		return nil, errors.New(field + " must be a positive integer ID")
	}
	return []mcpauth.ResourceRef{{Type: resourceType, ID: strconv.FormatInt(id, 10)}}, nil
}

func canonicalMap(input any) (map[string]any, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, errors.New("input must be a JSON object")
	}
	return object, nil
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		value, err := typed.Int64()
		return value, err == nil
	case string:
		id, err := strconv.ParseInt(typed, 10, 64)
		return id, err == nil
	default:
		return 0, false
	}
}
