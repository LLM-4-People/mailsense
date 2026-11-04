package mailquery

import (
	"fmt"
	"strings"
	"testing"
)

// The prompt and the validator are one contract: the default Normalize picks
// is the default the prompt states, the bound Validate accepts is the bound
// the prompt states, and one past it is refused.
func TestPromptSchemaMatchesNormalizeAndValidate(t *testing.T) {
	schema := PromptSchema()
	cases := []struct {
		kind Kind
		def  int
		max  int
	}{
		{KindContacts, DefaultRankLimit, MaxContactLimit},
		{KindCompanies, DefaultRankLimit, MaxCompanyLimit},
		{KindMessages, DefaultMessageLimit, MaxMessageListLimit},
		{KindAgenda, DefaultAgendaLimit, MaxAgendaLimit},
		{KindTime, DefaultTimeLimit, MaxTimeLimit},
	}
	for _, c := range cases {
		plan := Plan{Kind: c.kind}.Normalize()
		if plan.Limit != c.def {
			t.Errorf("%s default limit = %d, want %d", c.kind, plan.Limit, c.def)
		}
		if err := plan.Validate(); err != nil {
			t.Errorf("%s default plan is invalid: %v", c.kind, err)
		}
		plan.Limit = c.max
		if err := plan.Validate(); err != nil {
			t.Errorf("%s rejects its declared maximum %d: %v", c.kind, c.max, err)
		}
		plan.Limit = c.max + 1
		if err := plan.Validate(); err == nil {
			t.Errorf("%s accepts %d, one past the declared maximum", c.kind, c.max+1)
		}
		if !strings.Contains(schema, fmt.Sprintf("1..%d", c.max)) {
			t.Errorf("the prompt does not state the %s bound 1..%d", c.kind, c.max)
		}
		if !strings.Contains(schema, fmt.Sprint(c.def)) {
			t.Errorf("the prompt does not state the %s default %d", c.kind, c.def)
		}
	}
	agenda := Plan{Kind: KindAgenda, HorizonHours: float64(AgendaHorizonMax)}.Normalize()
	if err := agenda.Validate(); err != nil {
		t.Errorf("agenda rejects its declared horizon %d: %v", AgendaHorizonMax, err)
	}
	agenda.HorizonHours = float64(AgendaHorizonMax) + 1
	if err := agenda.Validate(); err == nil {
		t.Errorf("agenda accepts %d hours, one past the declared maximum", AgendaHorizonMax+1)
	}
	if !strings.Contains(schema, fmt.Sprintf("0..%d", AgendaHorizonMax)) {
		t.Errorf("the prompt does not state the agenda horizon 0..%d", AgendaHorizonMax)
	}
}
