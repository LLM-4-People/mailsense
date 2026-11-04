package mathx

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsFinite: the one predicate accepts every finite value and refuses NaN
// and both infinities.
func TestIsFinite(t *testing.T) {
	for _, v := range []float64{0, -0, 1, -1, math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64} {
		if !IsFinite(v) {
			t.Errorf("IsFinite(%v) = false", v)
		}
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if IsFinite(v) {
			t.Errorf("IsFinite(%v) = true", v)
		}
	}
}

// TestFinitenessIsSpelledOnce: the consumers read IsFinite instead of keeping
// their own `IsNaN` + `IsInf` pair.
func TestFinitenessIsSpelledOnce(t *testing.T) {
	for _, dir := range []string{"../jsonx", "../optim", "../vector"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			if strings.Contains(source, "IsNaN") && strings.Contains(source, "IsInf") {
				t.Errorf("%s keeps its own finiteness pair; use mathx.IsFinite", file)
			}
		}
	}
}
