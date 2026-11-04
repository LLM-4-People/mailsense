//go:build judgegold

package judge

// Aggregate, value-free diagnosis of gold-run contract failures: which
// question failed, the failed value's JSON type, and (for strings) whether
// it looks like a number, a criterion name, a 1-based position or something
// else. Values and gold labels are counted, never printed.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// captureGenerator records the raw answer of the last Generate call so the
// gold harness can classify contract failures without changing the judge.
// The content is never printed; only shapes derived from it are counted.
type captureGenerator struct {
	engine.Generator
	content string
}

func (c *captureGenerator) Generate(ctx context.Context, system, user string, opts ...engine.GenerateOptions) (engine.Generation, error) {
	g, err := c.Generator.Generate(ctx, system, user, opts...)
	if g.Content != "" {
		c.content = g.Content
	}
	return g, err
}

// failedShape is one failed answer's aggregate shape. The value itself is
// deliberately not kept.
type failedShape struct {
	Question  string
	JSONType  string
	Numeric   bool
	Criterion bool
	OneBased  bool
	Range     string // numeric: in_range, top_plus_one, out_of_range, unrepresentable
}

type diagnosisAnswer struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

type diagnosisDoc struct {
	Answers []diagnosisAnswer `json:"answers"`
}

// diagnoseFailures classifies every requested answer that ParseLabel
// rejects. It mirrors parseAnswer's per-question attribution: a document
// that does not decode, a missing answer and an invalid value each become
// one shape per affected question. A null value is a refusal, not a failure.
func diagnoseFailures(content string, qs []engine.Question) []failedShape {
	var doc diagnosisDoc
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		out := make([]failedShape, 0, len(qs))
		for _, q := range qs {
			out = append(out, failedShape{Question: q.Name, JSONType: "document"})
		}
		return out
	}
	byName := make(map[string]*engine.Question, len(qs))
	for i := range qs {
		byName[qs[i].Name] = &qs[i]
	}
	seen := make(map[string]bool, len(qs))
	var out []failedShape
	for _, a := range doc.Answers {
		q := byName[a.Name]
		if q == nil || seen[a.Name] {
			continue // structural failure, already counted by parseAnswer
		}
		seen[a.Name] = true
		if a.Value == nil {
			out = append(out, failedShape{Question: q.Name, JSONType: "missing"})
			continue
		}
		raw, err := jsonx.DecodeOne(a.Value)
		if err != nil {
			out = append(out, failedShape{Question: q.Name, JSONType: "invalid"})
			continue
		}
		if raw == nil {
			continue // refusal: no label, not a failure
		}
		if _, err := taxonomy.ParseLabel(q, raw); err == nil {
			continue // valid: not part of this diagnosis
		}
		out = append(out, classifyFailure(q, raw))
	}
	for i := range qs {
		if !seen[qs[i].Name] {
			out = append(out, failedShape{Question: qs[i].Name, JSONType: "missing"})
		}
	}
	return out
}

func classifyFailure(q *engine.Question, raw any) failedShape {
	f := failedShape{Question: q.Name, JSONType: jsonx.Kind(raw)}
	numericQuestion := q.Type == engine.Noul || q.Type == engine.Score
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if n, ok := parseDiagnosisNumber(s); ok {
			f.Numeric = true
			if numericQuestion {
				f.Range = diagnosisRange(q, n)
			}
		}
		f.Criterion = matchesCriterion(q, s)
		if i, err := strconv.Atoi(s); err == nil && i >= 1 && i <= q.Top()+1 {
			f.OneBased = true
		}
	case json.Number:
		if n, err := v.Float64(); err == nil && numericQuestion {
			f.Numeric = true
			f.Range = diagnosisRange(q, n)
		}
	case float64:
		if numericQuestion {
			f.Numeric = true
			if math.IsNaN(v) || math.IsInf(v, 0) {
				f.Range = "unrepresentable"
			} else {
				f.Range = diagnosisRange(q, v)
			}
		}
	}
	return f
}

func parseDiagnosisNumber(s string) (float64, bool) {
	if s == "" || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) || !json.Valid([]byte(s)) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func diagnosisRange(q *engine.Question, n float64) string {
	top := float64(q.Top())
	switch {
	case n == top+1:
		return "top_plus_one"
	case n >= 0 && n <= top:
		return "in_range" // only a non-canonical spelling can land here
	default:
		return "out_of_range"
	}
}

// matchesCriterion compares against a score level, a choice option, or the
// short name before " - " in a level ("Very negative" for the full
// criterion), case-insensitively; the gold values are never consulted.
func matchesCriterion(q *engine.Question, s string) bool {
	if s == "" {
		return false
	}
	names := append([]string{}, q.Levels...)
	for _, o := range q.Options {
		names = append(names, o.Name)
	}
	for _, name := range names {
		if strings.EqualFold(s, strings.TrimSpace(name)) {
			return true
		}
		if i := strings.Index(name, " - "); i > 0 && strings.EqualFold(s, strings.TrimSpace(name[:i])) {
			return true
		}
	}
	return false
}

// logFailureDiagnosis prints aggregate counts only: no value, criterion
// text, gold label or message identity is ever logged.
func logFailureDiagnosis(t *testing.T, shapes []failedShape) {
	t.Helper()
	if len(shapes) == 0 {
		t.Logf("diagnosis failed_values=0")
		return
	}
	byQuestion := map[string]int{}
	byType := map[string]int{}
	stringsNumeric, stringsCriterion, stringsOneBased, stringsOther := 0, 0, 0, 0
	oneBasedFlag := 0
	ranges := map[string]int{}
	for _, s := range shapes {
		byQuestion[s.Question]++
		byType[s.JSONType]++
		if s.JSONType == "string" {
			switch {
			case s.Numeric:
				stringsNumeric++
			case s.Criterion:
				stringsCriterion++
			case s.OneBased:
				stringsOneBased++
			default:
				stringsOther++
			}
		}
		if s.OneBased {
			oneBasedFlag++
		}
		if s.Numeric {
			ranges[s.Range]++
		}
	}
	t.Logf("diagnosis failed_values=%d by_question=%s types=%s", len(shapes),
		formatCounts(byQuestion), formatCounts(byType))
	t.Logf("diagnosis strings numeric=%d criterion=%d one_based=%d other=%d one_based_flag=%d",
		stringsNumeric, stringsCriterion, stringsOneBased, stringsOther, oneBasedFlag)
	t.Logf("diagnosis numeric_ranges top_plus_one=%d out_of_range=%d in_range=%d unrepresentable=%d",
		ranges["top_plus_one"], ranges["out_of_range"], ranges["in_range"], ranges["unrepresentable"])
}

// logScoreOneAboveGold counts, per score question, the valid answers that
// are exactly one level above the gold answer. A consistently 1-based model
// produces this shape; it is an aggregate signature, not a value listing.
// A 1-based top answer is rejected and therefore appears in the failure
// diagnosis instead, not here.
func logScoreOneAboveGold(t *testing.T, judged feedback.Judged, gold []feedback.Label, p *taxonomy.Profile) {
	t.Helper()
	type counts struct{ valid, oneAbove int }
	byQuestion := map[string]*counts{}
	for _, g := range gold {
		q, ok := p.Question(g.Question)
		if !ok || q.Type != engine.Score {
			continue
		}
		v, ok := judged[g.MessageID][g.Question]
		if !ok {
			continue
		}
		c := byQuestion[g.Question]
		if c == nil {
			c = &counts{}
			byQuestion[g.Question] = c
		}
		c.valid++
		if v.Model == g.Value.Number+1 {
			c.oneAbove++
		}
	}
	names := make([]string, 0, len(byQuestion))
	for name := range byQuestion {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := byQuestion[name]
		t.Logf("%-18s score_one_above_gold=%d/%d", name, c.oneAbove, c.valid)
	}
}

func TestDiagnoseFailuresClassifiesShapes(t *testing.T) {
	levels := []string{"Very negative - angry", "Negative", "Neutral", "Positive", "Very positive"}
	score := func(name string) engine.Question {
		return engine.Question{Name: name, Type: engine.Score, Levels: levels}
	}
	qs := []engine.Question{
		score("criterion"), score("top_string"), score("top_number"),
		score("range"), score("other"), score("valid"), score("refused"), score("missing"),
	}
	content := `{"answers":[
		{"name":"criterion","value":"neutral"},
		{"name":"top_string","value":"5"},
		{"name":"top_number","value":5},
		{"name":"range","value":4.5},
		{"name":"other","value":"lots"},
		{"name":"valid","value":"2"},
		{"name":"refused","value":null}
	]}`
	shapes := diagnoseFailures(content, qs)
	if len(shapes) != 6 {
		t.Fatalf("%d shapes: %+v", len(shapes), shapes)
	}
	byName := map[string]failedShape{}
	for _, s := range shapes {
		byName[s.Question] = s
	}
	if s := byName["criterion"]; !s.Criterion || s.Numeric {
		t.Errorf("criterion shape %+v", s)
	}
	if s := byName["top_string"]; !s.Numeric || !s.OneBased || s.Range != "top_plus_one" {
		t.Errorf("top string shape %+v", s)
	}
	if s := byName["top_number"]; !s.Numeric || s.Range != "top_plus_one" {
		t.Errorf("top number shape %+v", s)
	}
	if s := byName["range"]; !s.Numeric || s.Range != "out_of_range" {
		t.Errorf("range shape %+v", s)
	}
	if s := byName["other"]; s.Numeric || s.Criterion || s.OneBased || s.JSONType != "string" {
		t.Errorf("other shape %+v", s)
	}
	if s := byName["missing"]; s.JSONType != "missing" {
		t.Errorf("missing shape %+v", s)
	}
	if _, ok := byName["valid"]; ok {
		t.Error("a valid value was diagnosed as a failure")
	}
	if _, ok := byName["refused"]; ok {
		t.Error("a refusal was diagnosed as a failure")
	}
}

func formatCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s:%d", name, counts[name]))
	}
	return strings.Join(parts, ",")
}
