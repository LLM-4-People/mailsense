package evidence

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFeatureViewIsStable(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.run()
	layout := w.ev.FeatureLayout()
	names := layout.Names
	if len(names) < 300 || !slices.Contains(names, "list_id") || !slices.Contains(names, "auth_dmarc=fail") ||
		!slices.Contains(names, "recipient_count:1-2") || !slices.Contains(names, "mailer#0") || !slices.Contains(names, "date_next_days?missing") ||
		!slices.Contains(names, "date_past_days:1-2") || !slices.Contains(names, "date_past_days?missing") {
		t.Fatalf("schema: %d %v", len(names), names[:20])
	}
	for _, n := range names {
		if strings.HasPrefix(n, "owner_replied_after") || strings.HasPrefix(n, "read_state") || strings.HasPrefix(n, "folder_") {
			t.Errorf("%s: a non-causal signal in the features", n)
		}
	}
	r := w.record("q2")
	v := layout.Vector(r)
	if len(v) != len(names) {
		t.Fatalf("vector %d, schema %d", len(v), len(names))
	}
	cell := func(name string) float32 { return v[slices.Index(names, name)] }
	if cell("question_marks:<1") != 1 || cell("thread_last_sender=owner") != 1 || cell("thread_gap_minutes:1440-4320") != 1 ||
		cell("auth_dmarc?missing") != 0 || cell("template_matches?missing") != 1 || cell("list_id") != 0 {
		t.Errorf("cells: %v", v)
	}
	// The overdue signal's own cell: a past date sets its distance bin, and
	// an absent value sets only the missing flag (never the "0" bin).
	overdue := layout.Vector(Record{"date_past_days": Int(1)})
	if overdue[slices.Index(names, "date_past_days:1-2")] != 1 || overdue[slices.Index(names, "date_past_days?missing")] != 0 {
		t.Errorf("date_past_days: %v", names)
	}
	// A record without values (never computed) is all missing.
	empty := layout.Vector(Record{})
	missing := 0
	for i, x := range empty {
		if x != 0 && !strings.HasSuffix(names[i], "?missing") {
			t.Errorf("%s set without a value", names[i])
		}
		if x == 1 {
			missing++
		}
	}
	if missing == 0 {
		t.Error("missing flags")
	}
}

func TestFactsAreCausalBucketedAndOffByDefault(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("x", "ann@example.org", 0, "Can we meet on 2026-10-14 at 10:00?")
	m.Headers = "Received: a\nX-Mailer: Secret Mailer 1.0"
	m.IsRead = true
	w.write(m)
	w.run()
	text := w.ev.FactsText(w.record("x"))
	for _, want := range []string{"question_marks: 1-2", "date_next_days: 2-8", "mailer: none", "language: en"} {
		if want == "mailer: none" {
			if strings.Contains(text, "mailer:") {
				t.Errorf("an opaque value is never a fact: %s", text)
			}
			continue
		}
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	for _, never := range []string{"read_state", "owner_replied_after", "folder_path"} {
		if strings.Contains(text, never) {
			t.Errorf("%s is not causal: never a fact", never)
		}
	}
	facts, err := w.ev.Facts(t.Context())
	if err != nil || len(facts) != 1 || w.cfg.Bool("triage.facts.evidence") {
		t.Fatalf("facts %d %v; the triage setting is off by default", len(facts), err)
	}
	if w.ev.Version() == "0" {
		t.Error("a run that wrote moves the version")
	}
}

// The coverage view counts values, reasons and buckets, and never lists
// an opaque value.
func TestCoverage(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.run()
	cov, err := w.ev.Coverage(t.Context())
	if err != nil || cov.Messages != 5 {
		t.Fatalf("%v %+v", err, cov.Messages)
	}
	by := map[string]SignalCoverage{}
	for _, c := range cov.Signals {
		by[c.Name] = c
	}
	if c := by["direction"]; c.OK != 5 || c.Values["received"] != 4 || c.Values["sent_by_owner"] != 1 {
		t.Errorf("direction %+v", c)
	}
	if c := by["list_id"]; c.Unavailable != 1 || c.Reasons["unavailable:"+ReasonNoHeaders] != 1 || c.Values["false"] != 4 {
		t.Errorf("list_id %+v", c)
	}
	if c := by["thread_position"]; c.Values["<1"] != 1 || c.Max != 4 || c.Mean != 2 {
		t.Errorf("thread_position %+v", c)
	}
	if c := by["mailer"]; c.Values != nil || c.Distinct != 1 {
		t.Errorf("an opaque value is counted, never listed: %+v", c)
	}
}

// Regression (review L0, bugs plausible: feature drift): a layout reads
// evidence.features.hash_buckets once, so its vectors keep its shape after
// the setting changes; a new layout has a new id; an enum value outside
// the vocabulary (stored before a vocabulary change) is missing, not an
// all-zero row.
func TestFeatureLayoutIsOneReading(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.run()
	l := w.ev.FeatureLayout()
	if err := w.cfg.Set("evidence.features.hash_buckets", 4); err != nil {
		t.Fatal(err)
	}
	if v := l.Vector(w.record("q2")); len(v) != len(l.Names) {
		t.Fatalf("the layout's vectors keep its shape: %d vs %d", len(v), len(l.Names))
	}
	if n := w.ev.FeatureLayout(); n.ID == l.ID || len(n.Names) == len(l.Names) {
		t.Fatal("a new reading is a new layout")
	}
	v := l.Vector(Record{"language": Str("no-such-language")})
	if v[slices.Index(l.Names, "language?missing")] != 1 {
		t.Fatal("an unknown enum value is missing")
	}
}

// Review L0 (rules plausible: bins are thresholds): features.json has an
// override file like the rules (evidence.features.file), checked the same way.
func TestFeatureBinsHaveAnOverrideFile(t *testing.T) {
	w := newWorld(t, everything, nil)
	path := filepath.Join(w.cfg.Paths().DataDir, "evidence-features.json")
	if err := os.WriteFile(path, []byte(`{"bins": {"hop_count": [1, 10]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bins, err := LoadBins(w.cfg, Signals())
	if err != nil {
		t.Fatal(err)
	}
	if got := bins.bounds["hop_count"]; !slices.Equal(got, []float64{1, 10}) || len(bins.bounds["own_chars"]) == 0 {
		t.Fatalf("merged per signal: %v", bins.bounds)
	}
	for body, want := range map[string]string{`{"bins": {"hop_count": [10, 1]}}`: "ascending", `{"bins": {"nope": [1]}}`: "unknown signal",
		`{"bins": {"hop_count": null}}`: "ascending", `[1]`: "JSON object"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBins(w.cfg, Signals()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}

// Every numeric signal (an ints list: its length) has ascending structural
// bounds in features.json; New and LoadBins refuse a registry that misses
// one, and this pins the overdue signal's own scale.
func TestEveryNumericSignalHasBins(t *testing.T) {
	w := newWorld(t, everything, nil)
	bins, err := LoadBins(w.cfg, Signals())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Signals() {
		if !numeric(s.Type) {
			continue
		}
		b := bins.bounds[s.Name]
		if len(b) == 0 || !slices.IsSorted(b) {
			t.Errorf("%s: bounds %v are not ascending", s.Name, b)
		}
	}
	if got := bins.bounds["date_past_days"]; !slices.Equal(got, []float64{1, 2, 7, 30, 90}) {
		t.Fatalf("date_past_days bins %v", got)
	}
	s, _ := Lookup("date_past_days")
	for value, want := range map[int]int{0: 0, 1: 1, 2: 2, 7: 3, 30: 4, 90: 5, 365: 5} {
		if got := bins.bucket(s, Int(value)); got != want {
			t.Errorf("date_past_days %d: bucket %d, want %d", value, got, want)
		}
	}
}

// Regression (review L0, rules plausible: message_class names): a message
// class the protocol does not document (an organisation's own form) is
// counted as "custom" in the aggregates: its name never reaches a report.
func TestCustomMessageClassesAreBucketed(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("form", "ann@example.org", 1, "Hi")
	m.MessageClass = "IPM.Note.ExampleCorp.ExpenseForm"
	w.write(m, msg("note", "bob@example.org", 2, "Hi"))
	w.run()
	cov, err := w.ev.Coverage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cov.Signals {
		if c.Name != "message_class" {
			continue
		}
		if c.Values[CustomClass] != 1 || c.Values["ipm.note"] != 1 || len(c.Values) != 2 {
			t.Fatalf("values %v", c.Values)
		}
	}
	want(t, w.record("form"), "message_class", Str("ipm.note.examplecorp.expenseform")) // the stored value is the protocol value (hashed in features)
}

// The route key reads the feature layout: the merged bins document and the
// hash-bucket count both move the digest, and two reads agree.
func TestFeaturesDigestFollowsTheLayout(t *testing.T) {
	w := newWorld(t, everything, nil)
	first, err := FeaturesDigest(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := FeaturesDigest(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("two reads of the same layout disagree: %s != %s", first, again)
	}
	if err := w.cfg.Set(SettingFeaturesHashBuckets, 8); err != nil {
		t.Fatal(err)
	}
	changed, err := FeaturesDigest(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("the hash-bucket count is not in the feature digest")
	}
	path := filepath.Join(t.TempDir(), "features.json")
	if err := os.WriteFile(path, []byte(`{"bins":{"question_marks":[2,5]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingFeaturesFile, path); err != nil {
		t.Fatal(err)
	}
	bins, err := FeaturesDigest(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bins == changed {
		t.Fatal("a bins override is not in the feature digest")
	}
}
