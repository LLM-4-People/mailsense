package feedback

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// An abstained relationship, asks_for or deadline value is a non-answer and
// must not become a finding input. Before the fix, !v.Refused admitted it and
// its empty choice/zero number formed groups the owner never labelled.
func TestCorrelationsIgnoreAbstainedValues(t *testing.T) {
	p := profile(t)
	a := analysis(t)
	abstained := Judged{}
	for i := range a.Messages {
		m := &a.Messages[i]
		abstained[m.RowID] = map[string]taxonomy.Value{
			"relationship": {Type: engine.Choice, Choice: "existing_client", Abstain: engine.AbstainBelowTarget},
			"asks_for":     {Type: engine.Choice, Choice: "answer_question", Abstain: "not_certified"},
			"deadline":     {Type: engine.Score, Number: 2.5, Abstain: "below_coverage"},
		}
	}
	set := SettingsFrom(configtest.New(t, map[string]any{}))
	if fs := Correlations(a, abstained, p, set.Correlations); len(fs) != 0 {
		t.Fatalf("an abstention produced findings: %+v", fs)
	}
	answered := Judged{}
	for i := range a.Messages {
		m := &a.Messages[i]
		answered[m.RowID] = map[string]taxonomy.Value{
			"relationship": {Type: engine.Choice, Choice: "existing_client"},
			"asks_for":     {Type: engine.Choice, Choice: "answer_question"},
			"deadline":     {Type: engine.Score, Number: 2.5},
		}
	}
	byID := map[string]Finding{}
	for _, f := range Correlations(a, answered, p, set.Correlations) {
		byID[f.ID] = f
	}
	if len(byID) != 3 || byID["asks_for_vs_response"].N != 4 || byID["deadline_vs_response"].N != 4 || byID["reply_rate_by_relationship"].N != 6 {
		t.Fatalf("answered values are correlation inputs: %+v", byID)
	}
}
