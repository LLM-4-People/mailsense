package evidence

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// docs/EVIDENCE.md is the reference of the signals and rules: it lists
// every registered signal (with its version) and every shipped rule, and
// nothing else.
func TestTheDocListsEverySignalAndRule(t *testing.T) {
	raw, err := os.ReadFile("../../docs/EVIDENCE.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)`( v[0-9]+)? \\|").FindAllStringSubmatch(doc, -1) {
		listed[m[1]+m[2]] = true
	}
	want := map[string]bool{}
	for _, s := range Signals() {
		want[s.Name+" v"+strconv.Itoa(s.Version)] = true
	}
	set, err := parseRules(rulesJSON, Signals())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.Rules {
		want[r.ID] = true
	}
	for k := range want {
		if !listed[k] {
			t.Errorf("docs/EVIDENCE.md does not list %s", k)
		}
	}
	for k := range listed {
		if !want[k] {
			t.Errorf("docs/EVIDENCE.md lists %s, which is no signal or rule", k)
		}
	}
}
