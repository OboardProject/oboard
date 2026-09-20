package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

func TestReplayDeterministic(t *testing.T) {
	input, e := json.Marshal(example())
	if e != nil {
		t.Fatal(e)
	}
	var first, second bytes.Buffer
	if e = replay(bytes.NewReader(input), &first); e != nil {
		t.Fatal(e)
	}
	if e = replay(bytes.NewReader(input), &second); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("non deterministic replay")
	}
	var result auditrisk.Snapshot
	if e = json.Unmarshal(first.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.Activity == nil || result.Activity.Lower != 40 || result.Activity.Upper != 40 || result.AutomaticActionEligible {
		t.Fatalf("unexpected result: %+v", result)
	}
}
func TestReplayRejectsOversizedAndTrailingInput(t *testing.T) {
	for _, in := range []string{strings.Repeat(" ", (1<<20)+1), `{} {}`, `{"unrecognized":1}`} {
		if e := replay(strings.NewReader(in), &bytes.Buffer{}); e == nil {
			t.Fatal("invalid fixture accepted")
		}
	}
}
