package auditrisk

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestExposureBoundsTightenAndAttentionMax(t *testing.T) {
	rng := rand.New(rand.NewSource(3042))
	for trial := 0; trial < 300; trial++ {
		f, q, p, v, now := fixture()
		lo := rng.Intn(9)
		hi := lo + rng.Intn(9-lo)
		f.NovelRepeatedSources = CountRange{lo, hi}
		for i := range f.ExposureActivity {
			n := rng.Intn(9)
			f.ExposureActivity[i].Sources = CountRange{n, n + rng.Intn(9-n)}
		}
		bounds, err := Evaluate(f, q, p, v, now)
		if err != nil {
			t.Fatal(err)
		}
		f.NovelRepeatedSources = CountRange{hi, hi}
		for i := range f.ExposureActivity {
			n := f.ExposureActivity[i].Sources.Upper
			f.ExposureActivity[i].Sources = CountRange{n, n}
		}
		complete, err := Evaluate(f, q, p, v, now)
		if err != nil {
			t.Fatal(err)
		}
		if complete.Exposure.Lower < bounds.Exposure.Lower || complete.Exposure.Upper > bounds.Exposure.Upper {
			t.Fatal("exposure bounds did not contain completion")
		}
		if complete.Attention.Lower != max(complete.Activity.Lower, complete.Exposure.Lower) {
			t.Fatal("attention was not max")
		}
	}
}
func TestLowDurationAndAbsentBusiness(t *testing.T) {
	f, q, p, v, now := fixture()
	for size := 4; size <= p.SourceCapacity; size++ {
		for i := range f.Activity {
			f.Activity[i].Sources = CountRange{1, 1}
		}
		for i := 0; i < 5; i++ {
			f.Activity[i].Sources = CountRange{size, size}
		}
		s, err := Evaluate(f, q, p, v, now)
		if err != nil || s.Activity.Lower != 0 {
			t.Fatal(s.Activity, err)
		}
	}
	for n := 0; n <= p.SourceCapacity; n++ {
		f.NovelRepeatedSources = CountRange{n, n}
		s, err := Evaluate(f, q, p, v, now)
		if err != nil || s.Exposure.Upper > 50 {
			t.Fatal(s.Exposure, err)
		}
	}
}
func TestNormalizationAndFinalRoundingOnly(t *testing.T) {
	for _, tc := range []struct {
		x    int
		want *big.Rat
	}{{-1, big.NewRat(0, 1)}, {3, big.NewRat(0, 1)}, {4, big.NewRat(1, 5)}, {8, big.NewRat(1, 1)}, {100, big.NewRat(1, 1)}} {
		if normalized(tc.x, Scale{3, 8}).Cmp(tc.want) != 0 {
			t.Fatal(tc)
		}
	}
	// 1/5 * 1/15 is rounded only after multiplication by 100.
	r := new(big.Rat).Mul(normalized(4, Scale{3, 8}), normalized(6, Scale{5, 20}))
	if halfUp(r) != 1 {
		t.Fatal(r)
	}
	p := DefaultPolicy()
	p.SourceCapacity = 7
	if p.Validate() == nil {
		t.Fatal("unrepresentable saturation accepted")
	}
	p = DefaultPolicy()
	p.MinimumSlices = 13
	if p.Validate() == nil {
		t.Fatal("invalid slice threshold accepted")
	}
}
func TestResourceUnavailableAndPartial(t *testing.T) {
	s, err := resource(nil)
	if err != nil || s != nil {
		t.Fatal("absence represented as zero")
	}
	lo, hi, value := uint64(100), uint64(200), uint64(150)
	s, err = resource([]ResourceDimension{{Name: "request_rate", Unit: "requests/s", Value: &value, Start: &lo, Full: &hi}})
	if err != nil || s.Lower != 50 || s.Upper != 100 {
		t.Fatal(s, err)
	}
	_, err = resource([]ResourceDimension{{Name: "request_rate", Unit: "requests/s", Value: &value, Start: &hi, Full: &lo}})
	if err == nil {
		t.Fatal("invalid resource thresholds accepted")
	}
}
