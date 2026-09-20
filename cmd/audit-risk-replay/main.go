// audit-risk-replay evaluates a bounded feature fixture without reading a database.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

type fixture struct {
	Features auditrisk.Features `json:"features"`
	Quality  auditrisk.Quality  `json:"quality"`
	Policy   auditrisk.Policy   `json:"policy"`
	Versions auditrisk.Versions `json:"versions"`
	AsOf     time.Time          `json:"as_of"`
}

func example() fixture {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f := fixture{Policy: auditrisk.DefaultPolicy(), Versions: auditrisk.Versions{Model: "account-risk-v1", Baseline: "static-v1", Source: "prefix-v1/epoch-example"}, AsOf: now}
	good := auditrisk.Dimension{State: auditrisk.Satisfied, ReasonCode: "synthetic_complete"}
	f.Quality = auditrisk.Quality{IdentityTrusted: good, SourceUsable: good, Deduplicated: good, MeasurementValid: good, CoverageComplete: good, TimeAligned: good, SourceSetComplete: good, BaselineReady: auditrisk.Dimension{State: auditrisk.Unsatisfied, ReasonCode: "baseline_not_established"}, HistoryComplete: good, Freshness: good, CapabilitySupported: good}
	f.Features.AccountID = 1
	f.Features.DataRevision = 1
	f.Features.EvidenceCutoff = now
	for i := 0; i < auditrisk.WindowMinutes; i++ {
		minute := now.Add(time.Duration(i-auditrisk.WindowMinutes) * time.Minute)
		count := 1
		if i < 15 {
			count = 6
		}
		f.Features.Activity[i] = auditrisk.Minute{Start: minute, Sources: auditrisk.CountRange{Lower: count, Upper: count}}
		f.Features.ExposureActivity[i] = auditrisk.Minute{Start: minute, Sources: auditrisk.CountRange{}}
	}
	return f
}
func replay(in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("fixture exceeds 1 MiB")
	}
	var f fixture
	// Unknown fields are rejected so misspelled quality or version fields do not
	// silently change replay semantics.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&f); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected one fixture")
	}
	snapshot, err := auditrisk.Evaluate(f.Features, f.Quality, f.Policy, f.Versions, f.AsOf)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(snapshot)
}
func main() {
	sample := flag.Bool("example", false, "write a synthetic six-source / fifteen-minute input fixture")
	flag.Parse()
	var err error
	if *sample {
		err = json.NewEncoder(os.Stdout).Encode(example())
	} else {
		err = replay(os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit replay:", err)
		os.Exit(1)
	}
}
