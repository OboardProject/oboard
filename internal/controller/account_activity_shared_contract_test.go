package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestAccountActivitySharedContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/account_activity_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	// Pinned in both repositories so each suite runs independently.
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "410ff3c513ff2d04149da9ad6ce29355abbd3c343bd2ba7c562dc1826225f543" {
		t.Fatalf("shared fixture changed: %s", got)
	}
	var fixture struct {
		Report  json.RawMessage `json:"report"`
		Invalid []struct {
			Name, Field string
			Value       json.RawMessage
		} `json:"invalid"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var report accountActivityWireReport
	if err := json.Unmarshal(fixture.Report, &report); err != nil {
		t.Fatal(err)
	}
	if err := validateAccountActivityWire(report); err != nil {
		t.Fatal(err)
	}
	if report.CollectorStartedAt != 1790000000123456789 || report.Sequence != 1 || report.Items[0].UploadBytes != 9007199254740993 || report.ClockState != "unknown" || report.Complete {
		t.Fatalf("wire precision/coverage lost: %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]json.RawMessage
	if err := json.Unmarshal(fixture.Report, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("wire fields changed: %s", encoded)
	}
	for _, vector := range fixture.Invalid {
		t.Run(vector.Name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(fixture.Report, &fields); err != nil {
				t.Fatal(err)
			}
			if string(vector.Value) == "null" {
				delete(fields, vector.Field)
			} else {
				fields[vector.Field] = vector.Value
			}
			body, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var invalid accountActivityWireReport
			if err := json.Unmarshal(body, &invalid); err == nil && validateAccountActivityWire(invalid) == nil {
				t.Fatalf("invalid contract accepted: %s", body)
			}
		})
	}
}
