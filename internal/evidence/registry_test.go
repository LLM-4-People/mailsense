package evidence

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestTheRegistryIsValid(t *testing.T) {
	signals := Signals()
	if err := Validate(signals); err != nil {
		t.Fatal(err)
	}
	if len(signals) < 70 {
		t.Errorf("%d signals", len(signals))
	}
	groups := map[string]int{}
	for _, s := range signals {
		groups[s.Group]++
	}
	for _, g := range []string{GroupHeaders, GroupRecipients, GroupThread, GroupHistory, GroupContent, GroupOwner} {
		if groups[g] == 0 {
			t.Errorf("group %s is empty", g)
		}
	}
	if s, ok := Lookup("list_id"); !ok || s.Type != TypeBool {
		t.Error("lookup")
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("an unknown name must not resolve")
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	ok := Signal{Name: "a", Version: 1, Type: TypeBool, Group: GroupContent, Doc: "d", Extract: func(*Input) Value { return Bool(true) }}
	bad := []Signal{ok, ok, // duplicate
		{Name: "b", Version: 0, Type: TypeBool, Group: GroupContent, Doc: "d", Extract: ok.Extract},
		{Name: "c", Version: 1, Type: TypeEnum, Group: GroupContent, Doc: "d", Extract: ok.Extract},
		{Name: "d", Version: 1, Type: TypeBool, Values: []string{"x"}, Group: GroupContent, Doc: "d", Extract: ok.Extract},
		{Name: "e", Version: 1, Type: TypeBool, Needs: []string{"telepathy"}, Group: GroupContent, Doc: "d", Extract: ok.Extract},
		{Name: "f", Version: 1, Type: "complex", Group: GroupContent, Doc: "d", Extract: ok.Extract},
		{Name: "g", Version: 1, Type: TypeBool, Group: GroupContent, Doc: "d"},
	}
	err := Validate(bad)
	for _, want := range []string{`"a": empty or duplicate`, `"b": needs a version`, `"c": an enum`, `"d": an enum`, `"e": unknown capability`,
		`"f": needs`, `"g": needs`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestValuesRoundTrip(t *testing.T) {
	cases := []struct {
		t Type
		v Value
	}{
		{TypeBool, Bool(true)}, {TypeBool, Bool(false).Cut(true)}, {TypeInt, Int(-3)}, {TypeFloat, Num(0.25).Cut(true)},
		{TypeEnum, Str("pass")}, {TypeCategory, Str("ab12cd34")}, {TypeSet, List([]string{"a", "b"})}, {TypeSet, List(nil)},
		{TypeInts, Ints([]int{-2, 0, 5})}, {TypeInt, NotApplicable(ReasonFirst)}, {TypeEnum, Missing(ReasonCapability + "headers")},
		// Regression (review L0, rules #16): a not-applicable value read on a cut input stays partial.
		{TypeInt, NotApplicable(ReasonNone).Cut(true)}, {TypeEnum, Missing(ReasonPending).Cut(true)},
	}
	// An in-memory reason book (the store's interning is tested with the runs).
	ids, texts := map[string]int64{}, map[int64]string{}
	intern := func(r string) (int64, error) {
		if _, ok := ids[r]; !ok {
			ids[r] = int64(len(ids) + 1)
			texts[ids[r]] = r
		}
		return ids[r], nil
	}
	lookup := func(id int64) (string, error) { return texts[id], nil }
	for _, c := range cases {
		raw, err := c.v.encode(c.t, intern)
		if err != nil {
			t.Fatal(err)
		}
		back, err := decode(raw, c.t, lookup)
		if err != nil || !back.Equal(c.v) {
			t.Errorf("%s %+v -> %s -> %+v %v", c.t, c.v, raw, back, err)
		}
	}
	if raw, _ := Missing(ReasonNoHeaders).encode(TypeBool, intern); string(raw) != `{"u":`+strconv.FormatInt(ids[ReasonNoHeaders], 10)+`}` {
		t.Errorf("a reason is stored by id: %s", raw)
	}
	if v, err := decode(json.RawMessage(`{"n":"first"}`), TypeInt, lookup); err != nil || !v.Equal(NotApplicable(ReasonFirst)) {
		t.Errorf("a reason stored as text still reads: %+v %v", v, err)
	}
	if _, err := (Value{State: "maybe"}).encode(TypeBool, intern); err == nil {
		t.Error("an unknown state must not be stored")
	}
	for _, junk := range []string{`{"x":1}`, `"text"`, `{"p":"text"}`, `[1,`} {
		if _, err := decode(json.RawMessage(junk), TypeBool, lookup); err == nil {
			t.Errorf("%s must not decode as a bool", junk)
		}
	}
}

// Regression (review L0, bugs plausible: locale data updates): the pinned
// data a signal's views read is declared (Signal.Data) — checked by
// running every extractor over a sample of every shape — and its digest
// is part of the effective version.
func TestSignalsDeclareThePinnedDataTheyRead(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	m := msg("x", "ann@example.org", 1, "Le devis du 03/11/2026 s'élève à 1 250,00 € ? https://example.org\n-- \nAnn")
	w.write(m)
	env, err := buildEnv(t.Context(), w.st, mustSettings(t, w), "")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.st.EachMessage(t.Context(), storeAll(), func(r *store.MessageRow) error {
		for _, s := range Signals() {
			in := newInput(r, env)
			s.Extract(in)
			for _, d := range in.used {
				if !slices.Contains(s.Data, d) {
					t.Errorf("%s reads %s data without declaring it", s.Name, d)
				}
			}
		}
		return nil
	})
	with := Signal{Name: "a", Version: 1, Type: TypeBool, Data: []string{DataLocale}}
	without := with
	without.Data = nil
	a, err := newSchema([]Signal{with}, w.cfg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newSchema([]Signal{without}, w.cfg.Snapshot())
	if a.Keys[0] == b.Keys[0] {
		t.Error("the data digest is part of the effective version")
	}
	if _, err := newSchema([]Signal{{Name: "a", Version: 1, Data: []string{"nope"}}}, w.cfg.Snapshot()); err == nil {
		t.Error("unknown data is refused")
	}
}

func mustSettings(t *testing.T, w *world) Settings {
	t.Helper()
	s, err := settingsFrom(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
