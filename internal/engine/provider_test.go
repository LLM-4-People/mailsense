package engine

import "testing"

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
