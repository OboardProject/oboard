package core

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// configJSONPathPrefix is the prefix ConfigFieldError uses for a location
// inside the stored protocol document. Repair works on the document itself, so
// the prefix is stripped before a path is resolved.
const configJSONPathPrefix = "config_json."

// repairIterationLimit bounds the normalization fixpoint. Every accepted
// iteration removes one existing key, so the loop already terminates; the limit
// only keeps a pathological document from producing a long result list.
const repairIterationLimit = 32

// InboundRepairResult is the outcome of normalizing one stored inbound
// document. It is the same value for a preview and for an applied repair: the
// caller decides whether to persist ConfigJSON.
type InboundRepairResult struct {
	// ConfigJSON is the normalized document. It equals the input when nothing
	// was removable.
	ConfigJSON string
	// RemovedPaths are the document paths that were dropped, without the
	// config_json prefix, in the order they were removed.
	RemovedPaths []string
	// Resolved reports whether the normalized document passes the same
	// validation the store applies on write.
	Resolved bool
	// Remaining is the validation error left after normalization. It is empty
	// when Resolved is true.
	Remaining string
}

// ValidateStoredInbound runs the authoritative write-time validation against an
// already-stored inbound. It is the single oracle both the health evaluator and
// the repair loop consult, so a finding can never disagree with what the store
// would accept.
func ValidateStoredInbound(v model.Inbound) error {
	if v.Protocol == model.ProtocolSSH {
		return nil
	}
	if strings.TrimSpace(v.ConfigJSON) != "" {
		if err := ValidatePersistedInboundConfigJSON(v.Protocol, v.ConfigJSON); err != nil {
			return err
		}
	}
	a, err := AdapterFor(v.Protocol)
	if err != nil {
		return err
	}
	return a.ValidateInbound(v)
}

// NormalizeInboundConfig removes the stored protocol fields that the current
// model cannot accept and reports what it took out.
//
// It deliberately never invents an allowlist of its own. A field is removed
// only when the capability table says the protocol has no such option, or when
// the authoritative validator names that exact path and the path really exists
// in the document. Anything else (a missing required field, an invalid port, a
// value that is wrong rather than unsupported) is left untouched and reported
// through Remaining, because removing it would silently change behaviour
// instead of cleaning up debt.
func NormalizeInboundConfig(v model.Inbound) (InboundRepairResult, error) {
	result := InboundRepairResult{ConfigJSON: v.ConfigJSON}
	if v.Protocol == model.ProtocolSSH {
		result.Resolved = true
		return result, nil
	}
	raw := strings.TrimSpace(v.ConfigJSON)
	if raw == "" {
		if err := ValidateStoredInbound(v); err != nil {
			result.Remaining = err.Error()
			return result, nil
		}
		result.Resolved = true
		return result, nil
	}
	document, err := decodeConfigDocument(raw)
	if err != nil {
		// A document that is not a single JSON object has no addressable
		// fields, so there is nothing to normalize. The finding stays and the
		// operator can only disable or delete the inbound.
		result.Remaining = err.Error()
		return result, nil
	}

	candidate := v
	removed := []string{}
	for i := 0; i < repairIterationLimit; i++ {
		encoded, err := encodeConfigDocument(document)
		if err != nil {
			return InboundRepairResult{}, err
		}
		candidate.ConfigJSON = encoded
		validationErr := ValidateStoredInbound(candidate)
		if validationErr == nil {
			result.ConfigJSON = encoded
			result.RemovedPaths = removed
			result.Resolved = true
			return result, nil
		}
		path, ok := removablePath(candidate, document, validationErr)
		if !ok {
			result.ConfigJSON = encoded
			result.RemovedPaths = removed
			result.Remaining = validationErr.Error()
			return result, nil
		}
		if !deleteDocumentPath(document, path) {
			result.ConfigJSON = encoded
			result.RemovedPaths = removed
			result.Remaining = validationErr.Error()
			return result, nil
		}
		removed = append(removed, path)
	}
	encoded, err := encodeConfigDocument(document)
	if err != nil {
		return InboundRepairResult{}, err
	}
	result.ConfigJSON = encoded
	result.RemovedPaths = removed
	if validationErr := ValidateStoredInbound(candidate); validationErr != nil {
		result.Remaining = validationErr.Error()
	} else {
		result.Resolved = true
	}
	return result, nil
}

// removablePath decides which single document path the current validation error
// justifies removing.
func removablePath(v model.Inbound, document map[string]any, validationErr error) (string, bool) {
	var fieldErr *ConfigFieldError
	if errors.As(validationErr, &fieldErr) {
		path := strings.TrimPrefix(fieldErr.Path, configJSONPathPrefix)
		if path == "" || path == "config_json" {
			return "", false
		}
		// A named path that is not present is a missing-required error. Removal
		// cannot fix it and would not change the document.
		if _, exists := lookupDocumentPath(document, path); !exists {
			return "", false
		}
		return path, true
	}
	// Transport capability violations are reported as plain errors, so the
	// removable field is derived from the capability table rather than parsed
	// out of the message.
	for _, path := range removableTransportPaths(v.Protocol, document) {
		if _, exists := lookupDocumentPath(document, path); exists {
			return path, true
		}
	}
	return "", false
}

// removableTransportPaths lists the connection-reuse and TCP Fast Open fields
// this protocol can never honour, in removal order. The kernel silently ignores
// every one of them, so dropping them changes no runtime behaviour.
func removableTransportPaths(protocol model.Protocol, document map[string]any) []string {
	capability := ProtocolTransportCapability(protocol)
	paths := []string{}
	if raw, exists := document["multiplex"]; exists && raw != nil {
		if !capability.SupportsGenericMux() {
			paths = append(paths, "multiplex")
		} else if options, ok := raw.(map[string]any); ok {
			keys := make([]string, 0, len(options))
			for key := range options {
				switch key {
				case "enabled", "padding":
				default:
					// protocol, stream limits and brutal are client-side or
					// unexposed; none of them apply to a listener.
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				paths = append(paths, "multiplex."+key)
			}
		}
	}
	if raw, exists := document["tcp_fast_open"]; exists && raw != nil {
		enabled, ok := raw.(bool)
		if !ok || (enabled && !protocolTCPDataPath(protocol, document)) {
			paths = append(paths, "tcp_fast_open")
		}
	}
	return paths
}

// decodeConfigDocument parses a stored document the same way the validator
// does, so a repair preview and the validation it is checked against see
// identical numbers.
func decodeConfigDocument(raw string) (map[string]any, error) {
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("config_json must be a JSON object")
	}
	return document, nil
}

func encodeConfigDocument(document map[string]any) (string, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// lookupDocumentPath resolves a dotted path against the decoded document.
func lookupDocumentPath(document map[string]any, path string) (any, bool) {
	segments := strings.Split(path, ".")
	current := document
	for i, segment := range segments {
		value, exists := current[segment]
		if !exists {
			return nil, false
		}
		if i == len(segments)-1 {
			return value, true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return nil, false
}

// deleteDocumentPath removes a dotted path and reports whether anything was
// removed. A parent object left empty by the removal is dropped as well so the
// normalized document does not keep a meaningless shell.
func deleteDocumentPath(document map[string]any, path string) bool {
	segments := strings.Split(path, ".")
	parents := make([]map[string]any, 0, len(segments))
	current := document
	for i, segment := range segments {
		if i == len(segments)-1 {
			if _, exists := current[segment]; !exists {
				return false
			}
			delete(current, segment)
			break
		}
		next, ok := current[segment].(map[string]any)
		if !ok {
			return false
		}
		parents = append(parents, current)
		current = next
	}
	for i := len(parents) - 1; i >= 0; i-- {
		child, ok := parents[i][segments[i]].(map[string]any)
		if !ok || len(child) > 0 {
			break
		}
		delete(parents[i], segments[i])
	}
	return true
}
