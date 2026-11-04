package mathx

import (
	"math"
	"testing"
	"time"
)

func TestRoundIsHalfUp(t *testing.T) {
	for _, c := range []struct {
		v      float64
		places int
		want   float64
	}{{0.5, 0, 1}, {1.5, 0, 2}, {2.5, 0, 3}, {-2.5, 0, -3}, {0.285, 2, 0.29}, {1.25, 1, 1.3}, {12.345, 1, 12.3}, {0, 1, 0}} {
		if got := Round(c.v, c.places); got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v (banker's rounding or truncation)", c.v, c.places, got, c.want)
		}
	}
	if RoundInt(2.5) != 3 || RoundInt(2.4999) != 2 {
		t.Fatal("RoundInt")
	}
}

func TestStatistics(t *testing.T) {
	if _, ok := Median(nil); ok {
		t.Fatal("empty median")
	}
	if m, _ := Median([]float64{3, 1, 2}); m != 2 {
		t.Fatal(m)
	}
	if m, _ := Median([]float64{4, 1, 3, 2}); m != 2.5 {
		t.Fatal(m)
	}
	if m, _ := Mean([]float64{1, 2, 6}); m != 3 {
		t.Fatal(m)
	}
	// rank: index round-half-even(0.9*(n-1)); n=10 → index 8 (8.1 → 8)
	vals := []float64{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	if p, _ := Percentile(vals, 0.9); p != 9 {
		t.Fatalf("p90 = %v", p)
	}
	// Review 2b: n=6 → 0.9*5 = 4.5 → index 4, legacy Python round() (half-up
	// gave 5 in 2a: failed first).
	if p, _ := Percentile([]float64{1, 2, 3, 4, 5, 6}, 0.9); p != 5 {
		t.Fatalf("p90 = %v, legacy 5", p)
	}
	if Rank(1, 0.9) != 0 || Rank(16, 0.9) != 14 || Rank(4, 0.5) != 2 {
		t.Fatal("rank")
	}
	for attempt, want := range map[int]time.Duration{0: time.Second, 3: 8 * time.Second, 10: time.Minute, 100: time.Minute} {
		if got := Backoff(time.Second, time.Minute, attempt); got != want {
			t.Fatalf("Backoff(%d) = %v", attempt, got)
		}
	}
	if s := PStdev([]float64{2, 4, 4, 4, 5, 5, 7, 9}); s != 2 {
		t.Fatal(s)
	}
	if r, ok := Pearson([]float64{1, 2, 3}, []float64{2, 4, 6}); !ok || math.Abs(r-1) > 1e-12 {
		t.Fatal(r)
	}
	for _, bad := range [][2][]float64{{{1, 1}, {1, 2}}, {{1}, {1}}, {{1, 2}, {1}}} {
		if _, ok := Pearson(bad[0], bad[1]); ok {
			t.Fatalf("Pearson(%v) must be undefined", bad)
		}
	}
	if Ratio(1, 0) != 0 || Ratio(1, 4) != 0.25 {
		t.Fatal("ratio")
	}
}

// TestClampIsTheOneLimitRule pins the request-surface bounds: a value below 1
// is unset or invalid and falls back to the caller's default, a value above
// the maximum is capped, and a valid value passes through untouched.
func TestClampIsTheOneLimitRule(t *testing.T) {
	cases := []struct{ v, fallback, max, want int }{
		{v: -3, fallback: 25, max: 200, want: 25},
		{v: 0, fallback: 25, max: 200, want: 25},
		{v: 1, fallback: 25, max: 200, want: 1},
		{v: 7, fallback: 25, max: 200, want: 7},
		{v: 200, fallback: 25, max: 200, want: 200},
		{v: 201, fallback: 25, max: 200, want: 200},
		{v: 1 << 20, fallback: 25, max: 12, want: 12},
	}
	for _, tc := range cases {
		if got := Clamp(tc.v, tc.fallback, tc.max); got != tc.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", tc.v, tc.fallback, tc.max, got, tc.want)
		}
	}
}
