package ipdata

import (
	"errors"
	"reflect"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestASNPathConservativeComparison(t *testing.T) {
	trace := model.NetworkTraceResult{Reached: true, Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"10.0.0.1"}}, {Hop: 2, Addresses: []string{"1.1.1.1"}}, {Hop: 3, Addresses: []string{"1.0.0.1"}}, {Hop: 4, Addresses: []string{"8.8.8.8"}}}}
	lookup := func(ip string) (string, error) {
		if ip == "8.8.8.8" {
			return "AS15169", nil
		}
		return "AS13335", nil
	}
	got, err := ASNPath(trace, lookup)
	if err != nil || !reflect.DeepEqual(got, []string{"AS13335", "AS15169"}) {
		t.Fatalf("path=%v err=%v", got, err)
	}
	for _, change := range []func(*model.NetworkTraceResult){func(v *model.NetworkTraceResult) { v.Reached = false }, func(v *model.NetworkTraceResult) { v.Truncated = true }, func(v *model.NetworkTraceResult) { v.Hops[1].Addresses = nil }, func(v *model.NetworkTraceResult) { v.Hops[1].Addresses = []string{"1.1.1.1", "8.8.8.8"} }, func(v *model.NetworkTraceResult) { v.Hops[1].Hop = 3 }} {
		copy := trace
		copy.Hops = append([]model.NetworkTraceHop{}, trace.Hops...)
		change(&copy)
		if _, err := ASNPath(copy, lookup); err == nil {
			t.Fatal("incomplete or ambiguous path accepted")
		}
	}
	if _, err := ASNPath(trace, func(string) (string, error) { return "", errors.New("quota") }); err == nil {
		t.Fatal("failed ASN lookup accepted")
	}
}
