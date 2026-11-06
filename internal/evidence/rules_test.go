package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
)

// The one owner of the classes a rule's answer names (layer 1's targets
// and the route's key offer read it): an option, a truth value or a level
// range, with an option that is not the question's reported as -1.
func TestRuleAnswerClasses(t *testing.T) {
	yes, no := true, false
	choice := &engine.Question{Name: "c", Type: engine.Choice, Instructions: "x",
		Options: []engine.Option{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	noul := &engine.Question{Name: "n", Type: engine.Noul, Instructions: "x"}
	score := &engine.Question{Name: "s", Type: engine.Score, Instructions: "x", Levels: []string{"0", "1", "2", "3"}}
	for _, c := range []struct {
		name string
		q    *engine.Question
		a    RuleAnswer
		lo   int
		hi   int
	}{
		{"choice", choice, RuleAnswer{Option: "c"}, 2, 2},
		{"unknown option", choice, RuleAnswer{Option: "z"}, -1, -1},
		{"true", noul, RuleAnswer{Truth: &yes}, 1, 1},
		{"false", noul, RuleAnswer{Truth: &no}, 0, 0},
		{"level range", score, RuleAnswer{Min: 1, Max: 2, Range: true}, 1, 2},
	} {
		if lo, hi := c.a.Classes(c.q); lo != c.lo || hi != c.hi {
			t.Errorf("%s: Classes = %d..%d, want %d..%d", c.name, lo, hi, c.lo, c.hi)
		}
	}
}

func TestShippedRulesLoad(t *testing.T) {
	w := newWorld(t, everything, nil)
	if n := len(w.ev.Rules().Rules); n < 40 {
		t.Fatalf("%d rules", n)
	}
}

// deadline_overdue answers level 5 on a full date strictly before the
// message's day only: a same-day or future date, no date, an unavailable
// value and a past date read on a cut body (at_most settles maxima only)
// never make it fire.
func TestDeadlineOverdueRule(t *testing.T) {
	set, err := parseRules(rulesJSON, Signals())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*Rule{}
	for i := range set.Rules {
		byID[set.Rules[i].ID] = &set.Rules[i]
	}
	rule := byID["deadline_overdue"]
	if rule == nil {
		t.Fatal("no deadline_overdue rule")
	}
	if a := rule.Parsed(); a.Min != 5 || a.Max != 5 {
		t.Fatalf("answer %+v", a)
	}
	for _, c := range []struct {
		name string
		v    Value
		want bool
	}{
		{"the day before", Int(1), true},
		{"long overdue", Int(400), true},
		{"same day or no past date", Int(0), false},
		{"no full date", NotApplicable(ReasonNone), false},
		{"unresolved date", NotApplicable("ambiguous_order"), false},
		{"pending details", Missing(ReasonPending), false},
		{"cut body", Int(10).Cut(true), false},
	} {
		if got := rule.Fires(Record{"date_past_days": c.v}); got != c.want {
			t.Errorf("%s: fires = %v, want %v", c.name, got, c.want)
		}
	}
	// The general "some date" rule fires on a past date too, with a wider
	// answer: the pooled rules target averages the two (measured, never
	// special-cased).
	any := byID["deadline_any"]
	if any == nil || !any.Fires(Record{"date_count": Int(1)}) {
		t.Fatal("deadline_any does not fire on a date")
	}
	if a := any.Parsed(); a.Min != 1 || a.Max != 5 {
		t.Fatalf("deadline_any answer %+v", a)
	}
}

// No rule may read a lexical signal. Automatic answers must not depend on
// one language's wordlist: the evidence layer may still use such a signal
// as a feature (is_reply feeds threading), but the rule table may not.
func TestNoRuleUsesALexicalSignal(t *testing.T) {
	set, err := parseRules(rulesJSON, Signals())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.Rules {
		for _, c := range r.When {
			if c.sig.Lexical {
				t.Errorf("rule %s reads the lexical signal %s", r.ID, c.Signal)
			}
		}
	}
}

func TestRulesMisuse(t *testing.T) {
	doc := func(rules string) []byte { return []byte(`{"_doc": "x", "rules": {` + rules + `}}`) }
	cases := map[string]string{
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "nope", "is": true}], "doc": "d"}`:                     "unknown signal",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "read_state", "is": true}], "doc": "d"}`:               "not causal",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "is_reply", "is": true}], "doc": "d"}`:                 "lexical",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "auth_dmarc", "in": ["maybe"]}], "doc": "d"}`:          "not one of its values",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "list_id", "is": true, "min": 1}], "doc": "d"}`:        "exactly one",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "list_id", "min": 1}], "doc": "d"}`:                    "need a number",
		`"a": {"question": "q", "answer": {"min": 3, "max": 1}, "when": [{"signal": "list_id", "is": true}], "doc": "d"}`: "a ≤ b",
		`"a": {"question": "q", "answer": [1], "when": [{"signal": "list_id", "is": true}], "doc": "d"}`:                  "an answer is",
		`"a": {"question": "", "answer": "x", "when": [], "doc": ""}`:                                                     "needs a question",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "attachment_types", "is": "x"}], "doc": "d"}`:          "does not fit its type",
		// Regression (review L0, bugs #10a): a value is checked against the signal's type and vocabulary.
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "message_kind", "is": "no_such_kind"}], "doc": "d"}`: "not one of its values",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "list_id", "is": "true"}], "doc": "d"}`:              "does not fit its type",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "hop_count", "is": true}], "doc": "d"}`:              "does not fit its type",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "hop_count", "min": 3, "max": 1}], "doc": "d"}`:      "above max",
		`"a": {"question": "q", "answer": "x", "when": [{"signal": "list_id", "is": true}], "doc": "d", "extra": 1}`:    "unknown field",
	}
	for rule, want := range cases {
		if _, err := parseRules(doc(rule), Signals()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", rule, err, want)
		}
	}
}

// Regression (review L0, bugs #10b/c, rules #17): a number on an ints
// list is its length (one accessor), and each signal's declared unsure
// values and bound decide what a cut input settles: a partial "absent"
// or "present" is never taken as read, a partial nearest distance settles
// only a maximum.
func TestCutValuesSettleOnlyWhatTheyCannotOverstate(t *testing.T) {
	set, err := parseRules([]byte(`{"rules": {
		"zero": {"question": "q", "answer": "x", "when": [{"signal": "date_days", "is": 0}], "doc": "d"},
		"two": {"question": "q", "answer": "x", "when": [{"signal": "date_days", "is": 2}], "doc": "d"},
		"absent": {"question": "q", "answer": "x", "when": [{"signal": "auth_dmarc", "in": ["absent"]}], "doc": "d"},
		"present": {"question": "q", "answer": "x", "when": [{"signal": "list_unsubscribe", "in": ["present"]}], "doc": "d"},
		"bulk": {"question": "q", "answer": "x", "when": [{"signal": "list_unsubscribe", "in": ["present", "one_click"]}], "doc": "d"},
		"soon": {"question": "q", "answer": "x", "when": [{"signal": "date_next_days", "max": 3}], "doc": "d"},
		"late": {"question": "q", "answer": "x", "when": [{"signal": "date_next_days", "min": 1}], "doc": "d"}}}`), Signals())
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{"date_days": Ints([]int{3, 5}), "auth_dmarc": Str("absent").Cut(true), "list_unsubscribe": Str("present").Cut(true),
		"date_next_days": Int(2).Cut(true)}
	got := map[string]bool{}
	for _, r := range set.Rules {
		got[r.ID] = r.Fires(rec)
	}
	// A partial "present" may hide one_click only: a condition accepting both holds.
	if want := map[string]bool{"zero": false, "two": true, "absent": false, "present": false, "bulk": true, "soon": true, "late": false}; !maps.Equal(got, want) {
		t.Fatalf("fired %v, want %v", got, want)
	}
}

func TestRuleConditionsNeverOverstate(t *testing.T) {
	set, err := parseRules([]byte(`{"rules": {
		"t": {"question": "q", "answer": true, "when": [{"signal": "list_id", "is": true}], "doc": "d"},
		"f": {"question": "q", "answer": false, "when": [{"signal": "list_id", "is": false}], "doc": "d"},
		"n": {"question": "q", "answer": "x", "when": [{"signal": "list_unsubscribe", "in": ["none"]}], "doc": "d"},
		"min": {"question": "q", "answer": "x", "when": [{"signal": "hop_count", "min": 2}], "doc": "d"},
		"max": {"question": "q", "answer": "x", "when": [{"signal": "hop_count", "max": 2}], "doc": "d"}}}`), Signals())
	if err != nil {
		t.Fatal(err)
	}
	fire := map[string]bool{}
	cut := Record{"list_id": Bool(false).Cut(true), "list_unsubscribe": Str("none").Cut(true), "hop_count": Int(2).Cut(true)}
	for _, r := range set.Rules {
		fire[r.ID] = r.Fires(cut)
	}
	if fire["t"] || fire["f"] || fire["n"] || !fire["min"] || fire["max"] {
		t.Errorf("on a cut input only a minimum holds: %v", fire)
	}
	full := Record{"list_id": Bool(false), "list_unsubscribe": Str("none"), "hop_count": Int(2)}
	for _, r := range set.Rules {
		if got := r.Fires(full); got == (r.ID == "t") {
			t.Errorf("%s on complete values: %v", r.ID, got)
		}
	}
	for _, r := range set.Rules {
		if r.Fires(Record{"list_id": Missing(ReasonNoHeaders), "hop_count": NotApplicable(ReasonNone)}) {
			t.Errorf("%s fired on missing values", r.ID)
		}
	}
}

func TestMeasureWithWilson(t *testing.T) {
	set, err := parseRules([]byte(`{"rules": {
		"auto": {"question": "relationship", "answer": "automated_service", "when": [{"signal": "list_id", "is": true}], "doc": "d"},
		"q": {"question": "is_question", "answer": true, "when": [{"signal": "question_marks", "min": 1}], "doc": "d"},
		"d": {"question": "deadline", "answer": {"min": 3, "max": 4}, "when": [{"signal": "date_next_days", "max": 7}], "doc": "d"}}}`), Signals())
	if err != nil {
		t.Fatal(err)
	}
	labels := map[int64]map[string]json.RawMessage{}
	recs := map[int64]Record{}
	for i := int64(0); i < 100; i++ {
		rel, q, dl := `"automated_service"`, `1`, `3`
		if i%10 == 0 {
			rel, q, dl = `"vendor_partner"`, `0`, `1`
		}
		labels[i] = map[string]json.RawMessage{"relationship": json.RawMessage(rel), "is_question": json.RawMessage(q),
			"deadline": json.RawMessage(dl)}
		recs[i] = Record{"list_id": Bool(i < 50), "question_marks": Int(1), "date_next_days": Int(2)}
	}
	labels[999] = map[string]json.RawMessage{"is_question": json.RawMessage(`1`)} // no record: not counted
	stats := map[string]RuleStat{}
	for _, s := range set.Measure(labels, recs, 1.96, 0.75) {
		stats[s.ID] = s
	}
	a := stats["auto"]
	if a.Labelled != 100 || a.Fired != 50 || a.Correct != 45 || a.Coverage != 0.5 || a.Precision != 0.9 || !a.Key || a.WilsonLow < 0.78 || a.WilsonLow > 0.79 {
		t.Errorf("auto: %+v", a)
	}
	if q := stats["q"]; q.Fired != 100 || q.Correct != 90 || !q.Key {
		t.Errorf("noul labels 0/1: %+v", q)
	}
	if d := stats["d"]; d.Correct != 90 {
		t.Errorf("a level range: %+v", d)
	}
	if none := set.Measure(nil, recs, 1.96, 0.8); none[0].Fired != 0 || none[0].Key {
		t.Errorf("no labels, no key: %+v", none[0])
	}
}

func TestRulesPerInstallAndOverrides(t *testing.T) {
	w := newWorld(t, everything, nil)
	if _, err := w.ev.RuleTable(t.Context()); !errors.Is(err, ErrNoJudge) {
		t.Fatalf("without a judge no rule is measured: %v", err)
	}
	w.write(conversation()...)
	w.run()
	w.ev.UseJudge(fakeJudge{w: w})
	table, err := w.ev.RuleTable(t.Context())
	if err != nil || len(table) == 0 {
		t.Fatal(err)
	}
	for _, s := range table {
		if s.ID == "question_mark" && (s.Labelled != 5 || s.Fired != 3 || s.Correct != 3 || s.Key) {
			t.Errorf("measured on the judge's labels: %+v", s)
		}
	}
	// A user file adds, replaces and removes rules by id.
	path := filepath.Join(w.cfg.Paths().DataDir, "evidence-rules.json")
	over := `{"rules": {"question_mark": null, "mine": {"question": "reply_needed", "answer": false, "when": [{"signal": "list_id", "is": true}], "doc": "d"}}}`
	if err := os.WriteFile(path, []byte(over), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadRules(w.cfg, Signals())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range set.Rules {
		ids[r.ID] = true
	}
	if ids["question_mark"] || !ids["mine"] || !ids["susp_lookalike"] {
		t.Errorf("merged ids %v", ids)
	}
	if err := os.WriteFile(path, []byte(`[1]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRules(w.cfg, Signals()); err == nil {
		t.Error("a file that is no object must be refused")
	}
	// Regression (review L0, rules #12): a rule is checked against the
	// triage taxonomy: its question, its option, its answer kind, its levels.
	for rule, want := range map[string]string{
		`"question": "no_such_question", "answer": true`:             "no question",
		`"question": "category", "answer": "no_such_option"`:         "not an option",
		`"question": "category", "answer": true`:                     "needs a yes/no question",
		`"question": "reply_needed", "answer": "work"`:               "not an option",
		`"question": "deadline", "answer": {"min": 0, "max": 9}`:     "not whole levels",
		`"question": "deadline", "answer": {"min": 0.5, "max": 1}`:   "not whole levels",
		`"question": "reply_needed", "answer": {"min": 0, "max": 1}`: "not whole levels",
	} {
		body := `{"rules": {"bad": {` + rule + `, "when": [{"signal": "list_id", "is": true}], "doc": "d"}}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRules(w.cfg, Signals()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", rule, err, want)
		}
	}
	// Answers are kept in one compact form (reports, tables).
	if err := os.WriteFile(path, []byte(`{"rules": {"spaced": {"question": "deadline", "answer": {
		"min": 1,   "max": 2}, "when": [{"signal": "list_id", "is": true}], "doc": "d"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err = LoadRules(w.cfg, Signals())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.Rules {
		if r.ID == "spaced" && string(r.Answer) != `{"min":1,"max":2}` {
			t.Errorf("answer %s", r.Answer)
		}
	}
}

// fakeJudge labels every message a question.
type fakeJudge struct{ w *world }

func (j fakeJudge) Labels(ctx context.Context) (map[int64]map[string]json.RawMessage, error) {
	ids, err := j.w.st.MessageIDs(ctx, storeAll())
	out := map[int64]map[string]json.RawMessage{}
	for _, id := range ids {
		out[id] = map[string]json.RawMessage{"is_question": json.RawMessage(`1`)}
	}
	return out, err
}

// Regression (review L0, bugs plausible: contradictory rules): no two
// shipped rules give different answers to one question on the same
// conditions (cat_unsubscribe_notifications was dropped: on the gold
// train and dev labels it was right 0 of 3 times, its twin 3 of 3).
func TestNoTwoRulesContradict(t *testing.T) {
	set, err := parseRules(rulesJSON, Signals())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]Rule{}
	for _, r := range set.Rules {
		var conds []string
		for _, c := range r.When {
			conds = append(conds, condText(c))
		}
		slices.Sort(conds)
		key := r.Question + "|" + strings.Join(conds, "&")
		if o, ok := seen[key]; ok && string(o.Answer) != string(r.Answer) {
			t.Errorf("%s and %s answer %s differently on the same conditions", o.ID, r.ID, r.Question)
		}
		seen[key] = r
	}
}

// The route key follows the merged rules document: two loads of one
// document agree, and removing a rule moves the digest.
func TestRulesDigestFollowsTheMergedDocument(t *testing.T) {
	cfg := configtest.New(t, nil)
	first, err := RulesDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := RulesDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("two loads of the same rules disagree: %s != %s", first, again)
	}
	set, err := LoadRules(cfg, Signals())
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rules) == 0 {
		t.Fatal("no shipped rules")
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	body := []byte(`{"rules":{"` + set.Rules[0].ID + `":null}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(SettingRulesFile, path); err != nil {
		t.Fatal(err)
	}
	changed, err := RulesDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("removing a rule did not move the rules digest")
	}
}
