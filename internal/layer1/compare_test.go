package layer1

// scripts/layer1-fixture.sh compare <a> <b>: the paired bootstrap of two
// stored harness runs (their private per-item results), offline.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

func TestLayer1Compare(t *testing.T) {
	t.Parallel()
	a, b := os.Getenv("LAYER1_COMPARE_A"), os.Getenv("LAYER1_COMPARE_B")
	dir := os.Getenv("LAYER1_CACHE_DIR")
	if a == "" || b == "" || dir == "" {
		t.Skip("LAYER1_COMPARE_A, LAYER1_COMPARE_B and LAYER1_CACHE_DIR name two runs (scripts/layer1-fixture.sh compare)")
	}
	load := func(name string) itemResults {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "runs", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var r itemResults
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("%s: %v (runs before v4 hold correctness only)", name, err)
		}
		return r
	}
	ra, rb := load(a), load(b)
	d, lo, hi := pairedBootstrap(ra, rb, 2000, accuracyOf)
	c, clo, chi := pairedBootstrap(ra, rb, 2000, coverageAt(0.99))
	t.Logf("LAYER1 %s vs %s: mean dev accuracy %+.4f [%+.4f, %+.4f]; coverage at 0.99 %+.4f [%+.4f, %+.4f]", b, a, d, lo, hi, c, clo, chi)
	// recorded in the dev ledger (aggregates): a comparison quoted in the
	// docs is reproducible from out/ (rules review #12)
	if out := os.Getenv("LAYER1_REPORT_DIR"); out != "" {
		line, _ := json.Marshal(map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "kind": "compare", "baseline": a, "run": b,
			"mean_diff": d, "ci95": []float64{lo, hi}, "coverage_0_99_diff": c, "coverage_0_99_ci95": []float64{clo, chi}})
		f, err := fsx.OpenAppend(filepath.Join(out, "layer1-dev-ledger.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}
