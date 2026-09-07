package scripting

import (
	"bytes"
	"encoding/json"
)

func strictJSON(raw []byte, dest any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Coded(codeInvalidInput, "JSON object is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return Coded(codeInvalidInput, err.Error())
	}
	return nil
}

func MustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}
