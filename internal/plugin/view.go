package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	maxViewBytes      = 64 << 10
	maxViewNodes      = 128
	maxViewDepth      = 6
	maxTableColumns   = 8
	maxTableRows      = 40
	maxViewTextBytes  = 2000
	maxViewLabelBytes = 80
	maxViewCellBytes  = 120
)

// View bindings name an SDK read the panel resolves. They are not arbitrary queries.
var viewBindingCapability = map[string]string{
	"servers.get":     CapServersRead,
	"servers.health":  CapServersHealthRead,
	"servers.metrics": CapServersMetricsRead,
}

var viewTones = map[string]bool{"neutral": true, "success": true, "warning": true, "danger": true}

// ViewBindingError is attached to one binding node when the panel cannot resolve it.
// The rest of the document still renders.
type ViewBindingError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BindingResolver fills one binding. A non-nil error is stored on that node.
type BindingResolver func(source, envName string) (any, *ViewBindingError)

// ValidateViewDocument checks a published document against the closed component
// vocabulary and the page's declared actions. The returned bytes are canonical.
// An invalid document must not replace a previously stored snapshot.
func ValidateViewDocument(raw []byte, page PageDeclaration) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, Fail(CodeInvalidArgument, "document is required")
	}
	if len(raw) > maxViewBytes {
		return nil, Fail(CodeInvalidArgument, "document exceeds 64 KiB")
	}
	var doc struct {
		Title string            `json:"title,omitempty"`
		Body  []json.RawMessage `json:"body"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return nil, Fail(CodeInvalidArgument, "document: "+sanitizeDecodeError(err))
	}
	title, err := optionalText(doc.Title, maxViewLabelBytes)
	if err != nil {
		return nil, Fail(CodeInvalidArgument, "document title: "+err.Error())
	}
	if len(doc.Body) == 0 {
		return nil, Fail(CodeInvalidArgument, "document body must contain at least one block")
	}
	nodes := 0
	body := make([]any, 0, len(doc.Body))
	for _, rawNode := range doc.Body {
		node, err := validateViewNode(rawNode, page, 1, &nodes)
		if err != nil {
			return nil, err
		}
		body = append(body, node)
	}
	out := map[string]any{"body": body}
	if title != "" {
		out["title"] = title
	}
	canonical, err := json.Marshal(out)
	if err != nil {
		return nil, Fail(CodeInvalidArgument, "document is not valid JSON")
	}
	if len(canonical) > maxViewBytes {
		return nil, Fail(CodeInvalidArgument, "document exceeds 64 KiB")
	}
	return canonical, nil
}

// ResolveViewDocument copies a canonical document and fills binding nodes.
// Stored snapshots stay unresolved; resolution happens when the page is read.
func ResolveViewDocument(canonical []byte, resolve BindingResolver) ([]byte, error) {
	var doc struct {
		Title string            `json:"title,omitempty"`
		Body  []json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(canonical, &doc); err != nil {
		return nil, Fail(CodeInvalidArgument, "stored document is invalid")
	}
	body := make([]any, 0, len(doc.Body))
	for _, rawNode := range doc.Body {
		node, err := resolveViewNode(rawNode, resolve)
		if err != nil {
			return nil, err
		}
		body = append(body, node)
	}
	out := map[string]any{"body": body}
	if doc.Title != "" {
		out["title"] = doc.Title
	}
	return json.Marshal(out)
}

func validateViewNode(raw json.RawMessage, page PageDeclaration, depth int, nodes *int) (any, error) {
	if depth > maxViewDepth {
		return nil, Fail(CodeInvalidArgument, "document is nested too deeply")
	}
	*nodes++
	if *nodes > maxViewNodes {
		return nil, Fail(CodeInvalidArgument, "document has too many blocks")
	}
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &kind); err != nil || kind.Type == "" {
		return nil, Fail(CodeInvalidArgument, "each block needs a type")
	}
	switch kind.Type {
	case "stack":
		var node struct {
			Type     string            `json:"type"`
			Children []json.RawMessage `json:"children"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "stack: "+sanitizeDecodeError(err))
		}
		if len(node.Children) == 0 {
			return nil, Fail(CodeInvalidArgument, "stack needs at least one child")
		}
		children := make([]any, 0, len(node.Children))
		for _, child := range node.Children {
			decoded, err := validateViewNode(child, page, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			children = append(children, decoded)
		}
		return map[string]any{"type": "stack", "children": children}, nil
	case "heading", "text", "empty":
		var node struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, kind.Type+": "+sanitizeDecodeError(err))
		}
		limit := maxViewLabelBytes
		newlines := false
		if kind.Type != "heading" {
			limit = maxViewTextBytes
			newlines = true
		}
		text, err := requiredText(node.Text, limit, newlines)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, kind.Type+": "+err.Error())
		}
		return map[string]any{"type": kind.Type, "text": text}, nil
	case "metric":
		var node struct {
			Type  string `json:"type"`
			Label string `json:"label"`
			Value string `json:"value"`
			Tone  string `json:"tone,omitempty"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "metric: "+sanitizeDecodeError(err))
		}
		label, err := requiredText(node.Label, maxViewLabelBytes, false)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, "metric label: "+err.Error())
		}
		value, err := requiredText(node.Value, maxViewLabelBytes, false)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, "metric value: "+err.Error())
		}
		out := map[string]any{"type": "metric", "label": label, "value": value}
		if err := putTone(out, node.Tone); err != nil {
			return nil, err
		}
		return out, nil
	case "badge":
		var node struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Tone string `json:"tone,omitempty"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "badge: "+sanitizeDecodeError(err))
		}
		text, err := requiredText(node.Text, maxViewLabelBytes, false)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, "badge: "+err.Error())
		}
		out := map[string]any{"type": "badge", "text": text}
		if err := putTone(out, node.Tone); err != nil {
			return nil, err
		}
		return out, nil
	case "table":
		var node struct {
			Type    string `json:"type"`
			Columns []struct {
				Label string `json:"label"`
			} `json:"columns"`
			Rows [][]string `json:"rows"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "table: "+sanitizeDecodeError(err))
		}
		if len(node.Columns) == 0 || len(node.Columns) > maxTableColumns {
			return nil, Fail(CodeInvalidArgument, "table declares between 1 and 8 columns")
		}
		if len(node.Rows) > maxTableRows {
			return nil, Fail(CodeInvalidArgument, "table has too many rows")
		}
		columns := make([]any, 0, len(node.Columns))
		for _, column := range node.Columns {
			label, err := requiredText(column.Label, maxViewLabelBytes, false)
			if err != nil {
				return nil, Fail(CodeInvalidArgument, "table column: "+err.Error())
			}
			columns = append(columns, map[string]any{"label": label})
		}
		rows := make([]any, 0, len(node.Rows))
		for _, row := range node.Rows {
			if len(row) != len(node.Columns) {
				return nil, Fail(CodeInvalidArgument, "each table row must have one cell per column")
			}
			cells := make([]string, len(row))
			for i, cell := range row {
				text, err := optionalText(cell, maxViewCellBytes)
				if err != nil {
					return nil, Fail(CodeInvalidArgument, "table cell: "+err.Error())
				}
				cells[i] = text
			}
			rows = append(rows, cells)
		}
		return map[string]any{"type": "table", "columns": columns, "rows": rows}, nil
	case "binding":
		var node struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Server struct {
				Env string `json:"$env"`
			} `json:"server"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "binding: "+sanitizeDecodeError(err))
		}
		if _, ok := viewBindingCapability[node.Source]; !ok {
			return nil, Fail(CodeInvalidArgument, "binding source must be servers.get, servers.health or servers.metrics")
		}
		if !envNamePattern.MatchString(node.Server.Env) {
			return nil, Fail(CodeInvalidArgument, "binding server must name one environment variable")
		}
		return map[string]any{"type": "binding", "source": node.Source, "server": map[string]any{"$env": node.Server.Env}}, nil
	case "button":
		var node struct {
			Type   string `json:"type"`
			Action string `json:"action"`
			Label  string `json:"label"`
		}
		if err := decodeStrict(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "button: "+sanitizeDecodeError(err))
		}
		if !page.HasAction(node.Action) {
			return nil, Fail(CodeInvalidArgument, "button action "+node.Action+" is not declared on this page")
		}
		label, err := requiredText(node.Label, maxViewLabelBytes, false)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, "button: "+err.Error())
		}
		return map[string]any{"type": "button", "action": node.Action, "label": label}, nil
	default:
		return nil, Fail(CodeInvalidArgument, "unknown block type "+kind.Type)
	}
}

func resolveViewNode(raw json.RawMessage, resolve BindingResolver) (any, error) {
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &kind); err != nil {
		return nil, Fail(CodeInvalidArgument, "stored document is invalid")
	}
	switch kind.Type {
	case "stack":
		var node struct {
			Children []json.RawMessage `json:"children"`
		}
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "stored document is invalid")
		}
		children := make([]any, 0, len(node.Children))
		for _, child := range node.Children {
			decoded, err := resolveViewNode(child, resolve)
			if err != nil {
				return nil, err
			}
			children = append(children, decoded)
		}
		return map[string]any{"type": "stack", "children": children}, nil
	case "binding":
		var node struct {
			Source string `json:"source"`
			Server struct {
				Env string `json:"$env"`
			} `json:"server"`
		}
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "stored document is invalid")
		}
		out := map[string]any{"type": "binding", "source": node.Source, "server": map[string]any{"$env": node.Server.Env}}
		if resolve != nil {
			data, failure := resolve(node.Source, node.Server.Env)
			if failure != nil {
				out["error"] = failure
			} else {
				out["data"] = data
			}
		}
		return out, nil
	default:
		var node map[string]any
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, Fail(CodeInvalidArgument, "stored document is invalid")
		}
		return node, nil
	}
}

func decodeStrict(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func putTone(out map[string]any, tone string) error {
	tone = strings.TrimSpace(tone)
	if tone == "" {
		return nil
	}
	if !viewTones[tone] {
		return Fail(CodeInvalidArgument, "tone must be neutral, success, warning or danger")
	}
	out["tone"] = tone
	return nil
}

func optionalText(value string, max int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > max || hasControl(value, false) {
		return "", fmt.Errorf("must be plain text of at most %d bytes", max)
	}
	return value, nil
}

func requiredText(value string, max int, newlines bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max || hasControl(value, newlines) {
		return "", fmt.Errorf("must be plain text of 1-%d bytes", max)
	}
	return value, nil
}
