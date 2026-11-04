//go:build fixture

package threads

// Review 2b, median/percentile (scripts/fixture-import.sh): for every inbox
// message of the fixture with a sender standing, the history's bucket of
// the median must equal a brute-force median of the replies before it;
// and the test counts how many urgency priors and speed bands the 2a rule
// (median-low, exclusive bounds) gave differently from the restored legacy
// rule (average of the two middles; urgency inclusive at 0.1 minute).

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

func TestFixtureMedians(t *testing.T) {
	cfg, st := fixturetest.Import(t, fixturetest.Path(t), nil)
	set, err := SettingsFrom(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Load(t.Context(), st, set)
	if err != nil {
		t.Fatal(err)
	}
	speed := Floats(set.Options.BucketMinutes)
	urgency := urgencyBounds(t)
	inclusive := make([]float64, len(urgency))
	for i, b := range urgency {
		inclusive[i] = b + 0.05
	}
	fine := slices.Compact(slices.Sorted(slices.Values(append(append([]float64(nil), speed...), inclusive...))))
	h := a.KnownHistory(fine, 1, set.Options.Zone, set.Domains)
	n, wrong, urgencyChanged, speedChanged := 0, 0, 0, 0
	for i := range a.Messages {
		m := &a.Messages[i]
		tl := h.senders[mail.NormalizeAddress(m.Sender)]
		if m.Role != mail.RoleInbox || !m.Dated() || tl == nil {
			continue
		}
		pt, ok := h.Sender(m.Sender, m.At)
		k := sort.Search(len(tl.replies), func(j int) bool { return !tl.replies[j].at.Before(m.At) })
		if !ok || k == 0 {
			continue
		}
		lat := make([]float64, k)
		for j, r := range tl.replies[:k] {
			lat[j] = r.minutes
		}
		med, _ := mathx.Median(lat)
		low := slices.Sorted(slices.Values(lat))[(k-1)/2]
		n++
		if pt.Bucket != BucketIndex(med, fine) {
			wrong++
		}
		if BucketIndex(low, Floats(intsOf(urgency))) != BucketIndex(med, inclusive) {
			urgencyChanged++
		}
		if BucketIndex(low, speed) != BucketIndex(med, speed) {
			speedChanged++
		}
	}
	t.Logf("FIXTURE medians standings=%d history_vs_bruteforce_mismatch=%d urgency_prior_changed=%d speed_band_changed=%d", n, wrong, urgencyChanged, speedChanged)
	if n == 0 || wrong != 0 {
		t.Fatalf("FIXTURE the history's median bucket differs from the brute-force median for %d of %d standings", wrong, n)
	}
}

func urgencyBounds(t *testing.T) []float64 {
	raw, err := os.ReadFile("../triage/rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Urgency struct {
			Bounds []int `json:"bounds_minutes"`
		} `json:"urgency_prior"`
	}
	_ = json.Unmarshal(raw, &r)
	return Floats(r.Urgency.Bounds)
}

func intsOf(f []float64) []int {
	out := make([]int, len(f))
	for i, v := range f {
		out[i] = int(v)
	}
	return out
}
