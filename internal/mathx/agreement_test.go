package mathx

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAUC(t *testing.T) {
	if v, ok := AUC([]float64{0.9, 0.8, 0.3, 0.1}, []bool{true, true, false, false}); !ok || v != 1 {
		t.Fatalf("perfect ranking: %v %v", v, ok)
	}
	if v, _ := AUC([]float64{0.1, 0.9}, []bool{true, false}); v != 0 {
		t.Fatalf("inverted ranking: %v", v)
	}
	// ties count one half: 0.5 ties one negative (½), 0.7 beats one (1) → 1.5 / 4 pairs
	if v, _ := AUC([]float64{0.5, 0.7, 0.5, 0.9}, []bool{true, true, false, false}); !near(v, 0.375) {
		t.Fatalf("ties: %v", v)
	}
	for _, c := range []struct {
		s []float64
		p []bool
	}{{nil, nil}, {[]float64{1}, []bool{true}}, {[]float64{1, 2}, []bool{true}}} {
		if _, ok := AUC(c.s, c.p); ok {
			t.Fatalf("undefined AUC accepted: %v %v", c.s, c.p)
		}
	}
}

func TestBrier(t *testing.T) {
	if v, ok := Brier([]float64{1, 0, 0.5}, []bool{true, false, true}); !ok || !near(v, 0.25/3) {
		t.Fatalf("brier %v %v", v, ok)
	}
	if _, ok := Brier(nil, nil); ok {
		t.Fatal("no pairs")
	}
	if _, ok := Brier([]float64{1}, []bool{true, false}); ok {
		t.Fatal("length mismatch")
	}
}

func TestKappa(t *testing.T) {
	if v, ok := Kappa([]string{"a", "b", "a", "b"}, []string{"a", "b", "a", "b"}); !ok || v != 1 {
		t.Fatalf("perfect agreement: %v %v", v, ok)
	}
	// po = 0.5, pe = 0.5 → 0: agreement no better than chance
	if v, _ := Kappa([]string{"a", "a", "b", "b"}, []string{"a", "b", "a", "b"}); !near(v, 0) {
		t.Fatalf("chance agreement: %v", v)
	}
	// textbook: po = 0.7, pe = 0.5·0.6 + 0.5·0.4 = 0.5 → 0.4
	a := []string{"y", "y", "y", "y", "y", "n", "n", "n", "n", "n"}
	b := []string{"y", "y", "y", "y", "n", "n", "n", "n", "y", "y"}
	if v, _ := Kappa(a, b); !near(v, 0.4) {
		t.Fatalf("kappa %v", v)
	}
	if _, ok := Kappa([]string{"a", "a"}, []string{"a", "a"}); ok {
		t.Fatal("one label everywhere: kappa is undefined")
	}
	if _, ok := Kappa([]string{"a"}, nil); ok {
		t.Fatal("length mismatch")
	}
}

func TestWilson(t *testing.T) {
	// Reference values: Wilson (1927) / Newcombe (1998) table, 95 %.
	for _, c := range []struct {
		k, n   int
		lo, hi float64
	}{{81, 263, 0.2553, 0.3662}, {0, 10, 0, 0.2775}, {10, 10, 0.7225, 1}, {15, 148, 0.0624, 0.1605}} {
		lo, hi, ok := Wilson(c.k, c.n, 1.959964)
		if !ok || math.Abs(lo-c.lo) > 5e-4 || math.Abs(hi-c.hi) > 5e-4 {
			t.Errorf("%d/%d: [%.4f, %.4f] %v, want [%.4f, %.4f]", c.k, c.n, lo, hi, ok, c.lo, c.hi)
		}
	}
	for _, bad := range [][3]float64{{1, 0, 1.96}, {-1, 5, 1.96}, {6, 5, 1.96}, {1, 5, 0}, {1, 5, math.NaN()}} {
		if _, _, ok := Wilson(int(bad[0]), int(bad[1]), bad[2]); ok {
			t.Errorf("%v must be refused", bad)
		}
	}
}
