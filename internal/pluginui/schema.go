package pluginui

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

const MaxBytes = 256 << 10

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Page is rendered by the host; it never contains executable frontend code.
type Page struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Components  []Component `json:"components"`
}
type Component struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Title   string   `json:"title,omitempty"`
	Text    string   `json:"text,omitempty"`
	Columns []Column `json:"columns,omitempty"`
	Fields  []Field  `json:"fields,omitempty"`
	Action  *Action  `json:"action,omitempty"`
}
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
}
type Action struct {
	Label  string         `json:"label"`
	Params map[string]any `json:"params,omitempty"`
}
type Document struct {
	Pages []Page `json:"pages"`
}

func Parse(raw []byte) (Document, error) {
	var doc Document
	if len(raw) == 0 {
		return doc, nil
	}
	if len(raw) > MaxBytes {
		return doc, errors.New("UI document exceeds size limit")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return doc, errors.New("invalid UI document")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return doc, errors.New("UI document must contain one JSON object")
	}
	if len(doc.Pages) == 0 || len(doc.Pages) > 8 {
		return doc, errors.New("UI requires between one and eight pages")
	}
	pages := map[string]bool{}
	for _, page := range doc.Pages {
		if !identifier.MatchString(page.ID) || pages[page.ID] {
			return doc, errors.New("invalid or duplicate page ID")
		}
		pages[page.ID] = true
		if !label(page.Title) || len(page.Description) > 2048 || page.Components == nil || len(page.Components) > 32 {
			return doc, errors.New("invalid page limits")
		}
		ids := map[string]bool{}
		for _, c := range page.Components {
			if !identifier.MatchString(c.ID) || ids[c.ID] {
				return doc, errors.New("invalid or duplicate component ID")
			}
			ids[c.ID] = true
			if len(c.Title) > 128 || len(c.Text) > 8192 {
				return doc, errors.New("component text exceeds limit")
			}
			switch c.Type {
			case "text", "stat":
				if c.Action != nil || len(c.Fields) > 0 || len(c.Columns) > 0 {
					return doc, errors.New("display component cannot declare actions or fields")
				}
			case "table":
				if len(c.Columns) == 0 || len(c.Columns) > 16 || len(c.Fields) > 0 {
					return doc, errors.New("invalid table columns")
				}
				keys := map[string]bool{}
				for _, col := range c.Columns {
					if !identifier.MatchString(col.Key) || keys[col.Key] || !label(col.Label) {
						return doc, errors.New("invalid table column")
					}
					keys[col.Key] = true
				}
			case "form":
				if c.Action == nil || len(c.Fields) > 32 || len(c.Columns) > 0 {
					return doc, errors.New("invalid form")
				}
				fields := map[string]bool{}
				for _, field := range c.Fields {
					if !identifier.MatchString(field.Name) || fields[field.Name] || !label(field.Label) {
						return doc, errors.New("invalid form field")
					}
					fields[field.Name] = true
					switch field.Type {
					case "text", "number", "boolean":
						if len(field.Options) > 0 {
							return doc, errors.New("options require select field")
						}
					case "select":
						if len(field.Options) == 0 || len(field.Options) > 64 {
							return doc, errors.New("invalid select options")
						}
						seen := map[string]bool{}
						for _, value := range field.Options {
							if !label(value) || seen[value] {
								return doc, errors.New("invalid select option")
							}
							seen[value] = true
						}
					default:
						return doc, errors.New("unsupported field type")
					}
					if _, exists := c.Action.Params[field.Name]; exists {
						return doc, errors.New("form field conflicts with fixed action parameter")
					}
				}
			case "button":
				if c.Action == nil || len(c.Fields) > 0 || len(c.Columns) > 0 {
					return doc, errors.New("invalid button")
				}
			default:
				return doc, errors.New("unsupported component type")
			}
			if c.Action != nil {
				if !label(c.Action.Label) || len(c.Action.Params) > 32 {
					return doc, errors.New("invalid action")
				}
				for key := range c.Action.Params {
					if !identifier.MatchString(key) {
						return doc, errors.New("invalid action parameter")
					}
				}
			}
		}
	}
	return doc, nil
}
func label(value string) bool { return strings.TrimSpace(value) != "" && len(value) <= 128 }
