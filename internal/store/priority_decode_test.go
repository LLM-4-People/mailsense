package store

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
)

// priorityBenchAnswers is the shape one routed message stores from a
// 34-question priority profile: seventeen answers present, with the
// ranking's distribution mix (five choice answers with a confidence and an
// option distribution, four score answers with a confidence and a full
// level distribution, eight noul answers). The ranking selects fifteen of
// the seventeen answers; the two signature answers stay unselected, exactly
// as the priority read leaves them.
const priorityBenchAnswers = `{"category":{"type":"choice","choice":"work","probabilities":{"work":0.62,"personal":0.05,"notifications":0.03,"marketing":0.04,"finance":0.06,"security":0.08,"support_issue":0.12},"confidence":0.62,"layer":"layer1","rule":"key"}` +
	`,"topic":{"type":"choice","choice":"project_work","probabilities":{"project_work":0.41,"support_issue":0.14,"billing":0.09,"meeting":0.08,"personal":0.05,"newsletter":0.04,"security_alert":0.03,"travel":0.06,"hiring":0.04,"legal":0.03,"other":0.03},"confidence":0.41,"layer":"ondelle","rule":"ondelle"}` +
	`,"asks_for":{"type":"choice","choice":"reply","probabilities":{"nothing_fyi":0.08,"reply":0.52,"decision":0.14,"action":0.09,"payment":0.04,"meeting":0.05,"information":0.05,"approval":0.03},"confidence":0.52,"layer":"layer1","rule":"key"}` +
	`,"relationship":{"type":"choice","choice":"colleague","probabilities":{"colleague":0.47,"customer":0.14,"vendor":0.11,"manager":0.09,"friend":0.05,"unknown":0.06,"family":0.04,"partner":0.04},"confidence":0.47,"layer":"layer1","rule":"key"}` +
	`,"stance":{"type":"choice","choice":"neutral","probabilities":{"neutral":0.66,"positive":0.17,"negative":0.12,"mixed":0.05},"confidence":0.66,"layer":"ondelle","rule":"ondelle"}` +
	`,"urgency":{"type":"score","value":2.4,"probabilities":{"0":0.08,"1":0.19,"2":0.37,"3":0.26,"4":0.10},"confidence":0.37,"layer":"layer1","rule":"key"}` +
	`,"deadline":{"type":"score","value":3.1,"probabilities":{"0":0.05,"1":0.09,"2":0.18,"3":0.34,"4":0.22,"5":0.12},"confidence":0.34,"layer":"layer1","rule":"key"}` +
	`,"effort":{"type":"score","value":1.3,"probabilities":{"0":0.21,"1":0.43,"2":0.27,"3":0.09},"confidence":0.43,"layer":"ondelle","rule":"ondelle"}` +
	`,"sentiment":{"type":"score","value":2.8,"probabilities":{"0":0.07,"1":0.11,"2":0.23,"3":0.38,"4":0.21},"confidence":0.38,"layer":"layer1","rule":"key"}` +
	`,"reply_needed":{"type":"noul","value":0.82}` +
	`,"suspicious":{"type":"noul","value":0.04}` +
	`,"is_question":{"type":"noul","value":0.71}` +
	`,"call_to_action":{"type":"noul","value":0.33}` +
	`,"decision_needed":{"type":"noul","value":0.58}` +
	`,"info_sufficiency":{"type":"noul","value":0.64}` +
	`,"has_signature":{"type":"noul","value":0.12}` +
	`,"is_bulk":{"type":"noul","value":0.03}}`

// priorityBenchSelection is the ranking's fifteen-question selection: the
// five choice and four score answers plus six of the eight noul answers.
func priorityBenchSelection() map[string]QuestionKey {
	return map[string]QuestionKey{
		"category": {Name: "category", Type: "choice"}, "topic": {Name: "topic", Type: "choice"},
		"asks_for": {Name: "asks_for", Type: "choice"}, "relationship": {Name: "relationship", Type: "choice"},
		"stance":  {Name: "stance", Type: "choice"},
		"urgency": {Name: "urgency", Type: "score"}, "deadline": {Name: "deadline", Type: "score"},
		"effort": {Name: "effort", Type: "score"}, "sentiment": {Name: "sentiment", Type: "score"},
		"reply_needed": {Name: "reply_needed", Type: "noul"}, "suspicious": {Name: "suspicious", Type: "noul"},
		"is_question": {Name: "is_question", Type: "noul"}, "call_to_action": {Name: "call_to_action", Type: "noul"},
		"decision_needed": {Name: "decision_needed", Type: "noul"}, "info_sufficiency": {Name: "info_sufficiency", Type: "noul"},
	}
}

// TestLeanRouteDecodeMatchesTheStoredShape pins the lean route decode: it
// materializes exactly the fields the ranking reads (type, refused, value,
// choice, abstain, and a score's distribution) and drops confidence, layer,
// rule and every non-score distribution. The decoded map must equal a
// literal route.Answer map field for field, so a field that starts or stops
// being retained fails here.
func TestLeanRouteDecodeMatchesTheStoredShape(t *testing.T) {
	selected := priorityBenchSelection()
	got, err := decodePriorityAnswers(priorityBenchAnswers, selected, decodePriorityRouteAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(selected) {
		t.Fatalf("decoded %d answers, want the %d selected", len(got), len(selected))
	}
	want := map[string]route.Answer{
		"category":     {Answer: engine.Answer{Type: engine.Choice, Choice: "work"}},
		"topic":        {Answer: engine.Answer{Type: engine.Choice, Choice: "project_work"}},
		"asks_for":     {Answer: engine.Answer{Type: engine.Choice, Choice: "reply"}},
		"relationship": {Answer: engine.Answer{Type: engine.Choice, Choice: "colleague"}},
		"stance":       {Answer: engine.Answer{Type: engine.Choice, Choice: "neutral"}},
		"urgency": {Answer: engine.Answer{Type: engine.Score, Value: 2.4,
			Probabilities: map[string]float64{"0": 0.08, "1": 0.19, "2": 0.37, "3": 0.26, "4": 0.10}}},
		"deadline": {Answer: engine.Answer{Type: engine.Score, Value: 3.1,
			Probabilities: map[string]float64{"0": 0.05, "1": 0.09, "2": 0.18, "3": 0.34, "4": 0.22, "5": 0.12}}},
		"effort": {Answer: engine.Answer{Type: engine.Score, Value: 1.3,
			Probabilities: map[string]float64{"0": 0.21, "1": 0.43, "2": 0.27, "3": 0.09}}},
		"sentiment": {Answer: engine.Answer{Type: engine.Score, Value: 2.8,
			Probabilities: map[string]float64{"0": 0.07, "1": 0.11, "2": 0.23, "3": 0.38, "4": 0.21}}},
		"reply_needed":     {Answer: engine.Answer{Type: engine.Noul, Value: 0.82}},
		"suspicious":       {Answer: engine.Answer{Type: engine.Noul, Value: 0.04}},
		"is_question":      {Answer: engine.Answer{Type: engine.Noul, Value: 0.71}},
		"call_to_action":   {Answer: engine.Answer{Type: engine.Noul, Value: 0.33}},
		"decision_needed":  {Answer: engine.Answer{Type: engine.Noul, Value: 0.58}},
		"info_sufficiency": {Answer: engine.Answer{Type: engine.Noul, Value: 0.64}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lean decode\n got %+v\nwant %+v", got, want)
	}
	if _, ok := got["has_signature"]; ok {
		t.Fatal("an unselected route answer was decoded")
	}
	if _, ok := got["is_bulk"]; ok {
		t.Fatal("an unselected route answer was decoded")
	}

	// The dropped fields are explicit, not merely absent because the
	// literal was written without them.
	for _, name := range []string{"category", "topic", "asks_for", "relationship", "stance",
		"urgency", "deadline", "effort", "sentiment",
		"reply_needed", "suspicious", "is_question", "call_to_action", "decision_needed", "info_sufficiency"} {
		a := got[name]
		if a.Confidence != nil || a.Layer != "" || a.Rule != "" {
			t.Fatalf("answer %q retained a field the ranking never reads: %+v", name, a)
		}
	}
	for _, name := range []string{"category", "reply_needed"} {
		if got[name].Probabilities != nil {
			t.Fatalf("non-score answer %q retained its distribution: %v", name, got[name].Probabilities)
		}
	}

	// A nil selection is the full-read fallback and keeps the full route
	// shape: the lean decode applies only to a caller's selection.
	full, err := decodePriorityAnswers(priorityBenchAnswers, nil, decodePriorityRouteAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 17 {
		t.Fatalf("the full-read fallback decoded %d answers, want 17", len(full))
	}
	if a := full["category"]; a.Confidence == nil || a.Layer == "" || a.Rule == "" || a.Probabilities["work"] != 0.62 {
		t.Fatalf("the full-read fallback dropped a route field: %+v", a)
	}
	if a := full["urgency"]; a.Confidence == nil || a.Probabilities["4"] != 0.10 {
		t.Fatalf("the full-read fallback dropped a score field: %+v", a)
	}
	if _, ok := full["has_signature"]; !ok {
		t.Fatal("the full-read fallback skipped an answer")
	}

	// Refused and abstained answers keep the two fields that mark them
	// unusable; the distribution of an abstained score stays whole.
	states := `{"refused":{"type":"noul","refused":true,"value":0.9,"layer":"layer1","rule":"key"},
		"abstained":{"type":"score","abstain":"below_target","probabilities":{"0":0.5,"1":0.5}},
		"skipped":{"type":"noul","bogus":true}}`
	got, err = decodePriorityAnswers(states, map[string]QuestionKey{
		"refused": {Name: "refused", Type: "noul"}, "abstained": {Name: "abstained", Type: "score"},
	}, decodePriorityRouteAnswer)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := map[string]route.Answer{
		"refused":   {Answer: engine.Answer{Type: engine.Noul, Refused: true, Value: 0.9}},
		"abstained": {Answer: engine.Answer{Type: engine.Score, Abstain: "below_target", Probabilities: map[string]float64{"0": 0.5, "1": 0.5}}},
	}
	if !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("refused/abstained decode\n got %+v\nwant %+v", got, wantStates)
	}
}

// TestLeanRouteShapesDeclareEveryRouteAnswerField is the drift guard for
// the mirrored field set: a field added to route.Answer or engine.Answer
// must be declared by both lean shapes, or DisallowUnknownFields would start
// refusing stored payloads the full decode accepts.
func TestLeanRouteShapesDeclareEveryRouteAnswerField(t *testing.T) {
	want := jsonFieldNames(reflect.TypeOf(route.Answer{}))
	for _, shape := range []struct {
		name string
		typ  reflect.Type
	}{
		{"priorityScoreAnswer", reflect.TypeOf(priorityScoreAnswer{})},
		{"priorityFlatAnswer", reflect.TypeOf(priorityFlatAnswer{})},
	} {
		if got := jsonFieldNames(shape.typ); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s declares %v, want route.Answer's %v", shape.name, got, want)
		}
	}
}

// jsonFieldNames returns the sorted JSON names a struct declares, including
// an embedded struct's fields (the shape route.Answer uses).
func jsonFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && f.Tag.Get("json") == "" {
			names = append(names, jsonFieldNames(f.Type)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestLeanRouteDecodeKeepsTheStrictBoundary pins the trust boundary: a
// selected answer still refuses an unknown top-level field and every
// malformed payload fails, while an unselected answer is consumed without
// shape validation (its syntax is still checked). The error strings stay
// the decoder's own.
func TestLeanRouteDecodeKeepsTheStrictBoundary(t *testing.T) {
	selected := map[string]QuestionKey{"category": {Name: "category", Type: "choice"}}
	decode := func(raw string) (map[string]route.Answer, error) {
		return decodePriorityAnswers(raw, selected, decodePriorityRouteAnswer)
	}
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		want    string // error substring when wantErr is set
	}{
		{"unknown field in a selected answer", `{"category":{"type":"choice","choice":"work","bogus":1}}`, true, `unknown field "bogus"`},
		{"confidence with a wrong kind", `{"category":{"type":"choice","choice":"work","confidence":"high"}}`, true, "JSON number"},
		{"layer with a wrong kind", `{"category":{"type":"choice","choice":"work","layer":3}}`, true, "JSON string"},
		{"rule with a wrong kind", `{"category":{"type":"choice","choice":"work","rule":{}}}`, true, "JSON string"},
		{"a flat distribution with a wrong kind", `{"category":{"type":"choice","choice":"work","probabilities":[]}}`, true, "JSON object"},
		{"skipped fields accept null", `{"category":{"type":"choice","choice":"work","confidence":null,"layer":null,"rule":null,"probabilities":null}}`, false, ""},
		{"malformed selected answer", `{"category":{"type":"choice",}}`, true, "invalid character"},
		{"malformed unselected answer", `{"category":{"type":"choice","choice":"work"},"has_signature":}`, true, "invalid character"},
		{"unterminated object", `{"category":{"type":"choice","choice":"work"}`, true, ""},
		{"top-level array", `[]`, true, "expected an answers object"},
		{"trailing object", `{"category":{"type":"choice","choice":"work"}}{}`, true, "trailing data after the answers object"},
		{"trailing text", `{"category":{"type":"choice","choice":"work"}}x`, true, "invalid character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decode(tc.raw)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("decoded %+v, want an error", got)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want one containing %q", err, tc.want)
			}
		})
	}

	// Both lean shapes share the field set, so the score shape must refuse
	// an unknown field exactly like the flat one.
	if _, err := decodePriorityAnswers(`{"urgency":{"type":"score","value":2,"bogus":1}}`,
		map[string]QuestionKey{"urgency": {Name: "urgency", Type: "score"}}, decodePriorityRouteAnswer); err == nil ||
		!strings.Contains(err.Error(), `unknown field "bogus"`) {
		t.Fatalf("the score shape must keep the strict boundary: %v", err)
	}

	// A score's distribution is read, so it keeps the map's own shape
	// validation rather than a discarded kind.
	if _, err := decodePriorityAnswers(`{"urgency":{"type":"score","value":2,"probabilities":[]}}`,
		map[string]QuestionKey{"urgency": {Name: "urgency", Type: "score"}}, decodePriorityRouteAnswer); err == nil {
		t.Fatal("a score distribution of the wrong kind must fail")
	}

	// A skipped answer's shape is not validated: only its JSON syntax is.
	got, err := decode(`{"category":{"type":"choice","choice":"work"},"has_signature":{"type":"noul","bogus":true}}`)
	if err != nil {
		t.Fatalf("an unselected answer's shape must be skipped: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("decoded %d answers, want only the selected one", len(got))
	}

	// JSON null is the absent answers object the caller reports.
	got, err = decode(`null`)
	if err != nil {
		t.Fatalf("null must decode as the absent object: %v", err)
	}
	if got != nil {
		t.Fatalf("null decoded as %+v, want nil", got)
	}
}

// TestLeanRouteDecodeKeepsEveryScoreDistribution pins the one distribution
// the ranking reads: a score answer keeps its stored level map verbatim,
// even when it is incomplete or the wrong length. The decoder never
// synthesizes a class from Value; the class belongs to the consumer
// (selective.Decide) in internal/priority.
func TestLeanRouteDecodeKeepsEveryScoreDistribution(t *testing.T) {
	selected := map[string]QuestionKey{"urgency": {Name: "urgency", Type: "score"}}
	cases := []struct {
		name string
		raw  string
		want map[string]float64
	}{
		{
			name: "distribution chooses another class than round(value)",
			raw:  `{"urgency":{"type":"score","value":1.6,"probabilities":{"0":0.02,"1":0.08,"2":0.10,"3":0.20,"4":0.60}}}`,
			want: map[string]float64{"0": 0.02, "1": 0.08, "2": 0.10, "3": 0.20, "4": 0.60},
		},
		{
			name: "missing level key",
			raw:  `{"urgency":{"type":"score","value":2,"probabilities":{"0":0.9,"1":0.1}}}`,
			want: map[string]float64{"0": 0.9, "1": 0.1},
		},
		{
			name: "wrong length",
			raw:  `{"urgency":{"type":"score","value":2,"probabilities":{"0":0.1,"1":0.1,"2":0.1,"3":0.1,"4":0.1,"5":0.5}}}`,
			want: map[string]float64{"0": 0.1, "1": 0.1, "2": 0.1, "3": 0.1, "4": 0.1, "5": 0.5},
		},
		{
			name: "empty distribution",
			raw:  `{"urgency":{"type":"score","value":2,"probabilities":{}}}`,
			want: map[string]float64{},
		},
		{
			name: "null distribution",
			raw:  `{"urgency":{"type":"score","value":2,"probabilities":null}}`,
			want: nil,
		},
		{
			name: "absent distribution",
			raw:  `{"urgency":{"type":"score","value":2}}`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodePriorityAnswers(tc.raw, selected, decodePriorityRouteAnswer)
			if err != nil {
				t.Fatal(err)
			}
			a := got["urgency"]
			if !reflect.DeepEqual(a.Probabilities, tc.want) {
				t.Fatalf("probabilities %v, want %v", a.Probabilities, tc.want)
			}
			if a.Confidence != nil {
				t.Fatalf("a score's confidence was retained: %v", *a.Confidence)
			}
			if tc.name == "distribution chooses another class than round(value)" {
				class, best := 0, math.Inf(-1)
				for i := 0; i <= 4; i++ {
					if p := a.Probabilities[engine.LevelKey(i)]; p > best {
						class, best = i, p
					}
				}
				if class != 4 || int(math.Round(a.Value)) != 2 {
					t.Fatalf("the decoded distribution must stay the class owner: class %d, round(value) %d", class, int(math.Round(a.Value)))
				}
			}
		})
	}
}

// TestPreviewDecodeIsUnchanged pins the other half of the boundary: preview
// answers keep the full engine.Answer decode because checkPreviewAnswer
// validates the confidence and the complete distribution.
func TestPreviewDecodeIsUnchanged(t *testing.T) {
	raw := `{"category":{"type":"choice","choice":"work","probabilities":{"work":0.62,"security":0.38},"confidence":0.62},
		"urgency":{"type":"score","value":2.4,"probabilities":{"0":0.08,"1":0.19,"2":0.37,"3":0.26,"4":0.10},"confidence":0.37},
		"reply_needed":{"type":"noul","value":0.82},
		"has_signature":{"type":"noul","value":0.12}}`
	selected := map[string]QuestionKey{
		"category": {Name: "category", Type: "choice"}, "urgency": {Name: "urgency", Type: "score"},
		"reply_needed": {Name: "reply_needed", Type: "noul"},
	}
	got, err := decodePriorityAnswers(raw, selected, decodePriorityPreviewAnswer)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]engine.Answer{
		"category": {Type: engine.Choice, Choice: "work",
			Probabilities: map[string]float64{"work": 0.62, "security": 0.38}, Confidence: floatPtr(0.62)},
		"urgency": {Type: engine.Score, Value: 2.4,
			Probabilities: map[string]float64{"0": 0.08, "1": 0.19, "2": 0.37, "3": 0.26, "4": 0.10}, Confidence: floatPtr(0.37)},
		"reply_needed": {Type: engine.Noul, Value: 0.82},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preview decode\n got %+v\nwant %+v", got, want)
	}
	if _, ok := got["has_signature"]; ok {
		t.Fatal("an unselected preview answer was decoded")
	}

	// The preview path keeps the engine shape: route-only fields are still
	// unknown here, as they were before the lean route decode.
	_, err = decodePriorityAnswers(`{"category":{"type":"choice","choice":"work","layer":"key"}}`,
		map[string]QuestionKey{"category": {Name: "category", Type: "choice"}}, decodePriorityPreviewAnswer)
	if err == nil || !strings.Contains(err.Error(), `unknown field "layer"`) {
		t.Fatalf("the preview decode must keep the engine shape: %v", err)
	}
}

func floatPtr(v float64) *float64 { return &v }

// BenchmarkDecodePriorityRoute measures one routed message's route-answer
// decode with the ranking's selection: the decode the priority build does
// once per message before scoring.
func BenchmarkDecodePriorityRoute(b *testing.B) {
	selected := priorityBenchSelection()
	b.ReportAllocs()
	for range b.N {
		answers, err := decodePriorityAnswers(priorityBenchAnswers, selected, decodePriorityRouteAnswer)
		if err != nil {
			b.Fatal(err)
		}
		if len(answers) != len(selected) {
			b.Fatalf("decoded %d answers, want %d", len(answers), len(selected))
		}
	}
}
