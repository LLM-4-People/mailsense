package mathx

import (
	"math"
	"testing"
)

func TestNormalQuantile(t *testing.T) {
	for _, tc := range []struct{ p, want float64 }{
		{0.5, 0},
		{0.975, 1.959964},
		{0.9985294117647059, 2.9738},
	} {
		got, ok := NormalQuantile(tc.p)
		if !ok || math.Abs(got-tc.want) > 1e-4 {
			t.Errorf("NormalQuantile(%v) = %v, %v; want %v", tc.p, got, ok, tc.want)
		}
	}
	for _, p := range []float64{0, 1, -0.1, 1.1, math.NaN()} {
		if _, ok := NormalQuantile(p); ok {
			t.Errorf("NormalQuantile(%v) accepted a probability outside (0, 1)", p)
		}
	}
	if got := NormalCDF(0); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("NormalCDF(0) = %v, want 0.5", got)
	}
}
