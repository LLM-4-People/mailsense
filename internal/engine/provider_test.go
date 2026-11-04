package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

// A reasoning model spends output tokens before it writes the answer. The
// Q&A cap must reserve that reasoning, within the record's declared output
// reserve; a model without reasoning keeps the answer budget exactly.
func TestAnswerOutputTokensCoversReasoningWithinTheReserve(t *testing.T) {
	cases := []struct {
		name   string
		p      Provider
		answer int
		want   int
	}{
		{"no reasoning", Provider{ReserveOutputTokens: 16384}, 350, 350},
		{"reasoning off", Provider{ReserveOutputTokens: 16384, ReasoningEffort: "none"}, 350, 350},
		{"low effort adds its allowance", Provider{ReserveOutputTokens: 16384, ReasoningEffort: "low"}, 350, 4446},
		{"minimal effort", Provider{ReserveOutputTokens: 16384, ReasoningEffort: "minimal"}, 350, 1374},
		{"the record reserve is the ceiling", Provider{ReserveOutputTokens: 1000, ReasoningEffort: "low"}, 350, 1000},
		{"no reserve means no ceiling", Provider{ReasoningEffort: "low"}, 200, 4296},
		{"zero answer stays zero", Provider{ReserveOutputTokens: 16384, ReasoningEffort: "low"}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.AnswerOutputTokens(tc.answer); got != tc.want {
				t.Fatalf("AnswerOutputTokens(%d) = %d, want %d", tc.answer, got, tc.want)
			}
		})
	}
}

// A provider record names a secret; its value lives in the store and never in
// the record. A diagnostic about that record still passes the one redaction
// projection, so a registered value cannot reappear even when an adapter or a
// validation problem quotes it.
func TestProviderDiagnosticNeverCarriesAKeyValue(t *testing.T) {
	const key = "Xa5Tu0Vr7Kp2Ny4Mc9Df6Gh1Js3Lw8Bo"
	logx.RegisterSecret(key)
	p := Provider{ID: "dec", Label: "Decider", Protocol: ProtocolSystemOne, APIKeySecret: "DECIDER_KEY"}
	for name, err := range map[string]error{
		"validation": &ValidationError{Problems: []string{
			fmt.Sprintf("providers/%s/api_key_secret: %v", p.ID, key)}},
		"call": fmt.Errorf("%w: provider %s answered: %s", ErrHTTP, p.ID, key),
	} {
		t.Run(name, func(t *testing.T) {
			got := logx.RedactedError(err)
			if strings.Contains(got, key) {
				t.Fatalf("provider diagnostic leaked the key: %q", got)
			}
			if !strings.Contains(got, p.ID) {
				t.Fatalf("provider diagnostic lost the record id: %q", got)
			}
		})
	}
}
