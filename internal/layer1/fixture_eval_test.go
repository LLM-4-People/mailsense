//go:build fixture

package layer1

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// devItem is one gold dev message.
type devItem struct {
	id     int64
	gold   string // the gold id (opaque digest)
	group  string // its conversation (the dev certification split)
	lang   string
	labels goldset.Answers
}

// DevQuestion is one question's dev measurement (aggregates only).
type DevQuestion struct {
	Question         string                 `json:"question"`
	Type             engine.QuestionType    `json:"type"`
	N                int                    `json:"n"`
	Pending          int                    `json:"pending"` // dev items layer 1 could not answer (no vector)
	Layer1           Metrics                `json:"layer1"`
	Ondelle          Metrics                `json:"ondelle"` // the legacy Ondelle answers of the same items
	Majority         float64                `json:"majority"`
	Ceiling          float64                `json:"ceiling"`
	OfCeiling        float64                `json:"of_ceiling"`
	Meets            bool                   `json:"meets_requirement"` // on dev, by the honest dev certification at the requirement's target
	Requirement      Requirement            `json:"requirement"`
	ByLanguage       map[string]LanguageAcc `json:"by_language"`
	Confusion        map[string]int         `json:"confusion"` // "label→predicted" class names: counts (error analysis)
	OndelleConfusion map[string]int         `json:"ondelle_confusion"`
	// E0 "rules alone": dev items on which a teaching rule fires, and how
	// many of them its answer gets right
	RulesFired     int            `json:"rules_fired"`
	RulesCorrect   int            `json:"rules_correct"`
	Rules          []RuleEval     `json:"rules,omitempty"` // each rule's dev measurement (aggregates only)
	Train          QuestionReport `json:"train"`
	Select         *Selective     `json:"selective"` // coverage at the precision targets (fixture_select_test.go)
	answers        []devAnswer
	correctL1      map[int64]bool
	correctOndelle map[int64]bool
}

// RuleEval is one rule's dev measurement: how often it fires, how often
// its answer agrees with the dev label, how many dev labels its answer
// names (recall's denominator), whether the selection-part measurement
// makes it an answer key, and how often keyAnswer chose it.
type RuleEval struct {
	ID          string  `json:"id"`
	Question    string  `json:"question"`
	Key         bool    `json:"key"`
	Fired       int     `json:"fired"`
	Correct     int     `json:"correct"`
	Positives   int     `json:"positives"`
	KeyAnswered int     `json:"key_answered"`
	Precision   float64 `json:"precision"`
	Recall      float64 `json:"recall"`
}

// LanguageAcc is one language's dev accuracy.
type LanguageAcc struct {
	N       int     `json:"n"`
	Layer1  float64 `json:"layer1"`
	Ondelle float64 `json:"ondelle"`
}

// evaluateDev measures every question on the dev split: layer 1, the
// legacy Ondelle answers of the same messages, the majority class of the
// training targets, the agreement ceiling.
func evaluateDev(t *testing.T, plan *Plan, tr *Trained, dev []devItem, ondelle map[int64]map[string]engine.Answer, ceilings map[string]float64, rules *gathered) []DevQuestion {
	t.Helper()
	var out []DevQuestion
	reports := map[string]QuestionReport{}
	for _, r := range tr.Reports {
		reports[r.Question] = r
	}
	for i := range plan.Profile.Questions {
		q := &plan.Profile.Questions[i]
		req := plan.Requirements.For(q.Name)
		cutoff := plan.Profile.Cutoff(q.Name)
		dq := DevQuestion{Question: q.Name, Type: q.Type, Ceiling: ceilings[q.Name], Requirement: req, ByLanguage: map[string]LanguageAcc{},
			Confusion: map[string]int{}, OndelleConfusion: map[string]int{},
			Train: reports[q.Name], correctL1: map[int64]bool{}, correctOndelle: map[int64]bool{}}
		type ruleDev struct {
			eval   *RuleEval
			answer evidence.RuleAnswer
		}
		byRule := map[string]*ruleDev{}
		var ruleEvals []*RuleEval
		if rules != nil {
			for k := range rules.rules {
				r := &rules.rules[k]
				if r.Question != q.Name {
					continue
				}
				ev := &RuleEval{ID: r.ID, Question: r.Question, Key: r.Stat.Key}
				byRule[r.ID] = &ruleDev{eval: ev, answer: r.Answer}
				ruleEvals = append(ruleEvals, ev)
			}
		}
		// answerNames reports whether a rule's answer names a class: the
		// dev label it would be right about and recall's denominator.
		answerNames := func(a evidence.RuleAnswer, class int) bool {
			lo, hi := a.Classes(q)
			return lo >= 0 && class >= lo && class <= hi
		}
		var l1, sq []Prediction
		majority := 0
		if qs := tr.sets[q.Name]; qs != nil && len(qs.train) > 0 {
			majority = argmax(Prior(qs.train, qs.classes))
		}
		hits := 0
		langN, langL1, langSQ := map[string]int{}, map[string]int{}, map[string]int{}
		for _, it := range dev {
			raw, ok := it.labels[q.Name]
			if !ok {
				continue
			}
			_, class, err := labelTarget(q, string(raw), cutoff)
			if err != nil {
				t.Fatalf("dev label %s: %v", q.Name, err)
			}
			dq.N++
			if class == majority {
				hits++
			}
			lang := it.lang
			langN[lang]++
			da := devAnswer{item: it, class: class, l1: -1, key: -1, ondelle: -1}
			if p, ok := tr.Predict(q.Name, it.id); ok {
				l1 = append(l1, Prediction{ID: it.id, Label: class, Probs: p})
				c, conf := selective.Decide(q.Type, p, cutoff)
				da.l1, da.l1Conf, da.l1Probs = c, conf, p
				dq.correctL1[it.id] = c == class
				dq.Confusion[className(q, class)+"→"+className(q, c)]++
				if c == class {
					langL1[lang]++
				}
			} else {
				dq.Pending++
				dq.correctL1[it.id] = false
			}
			if rules != nil {
				if r, p, ok := keyAnswer(q, rules.rules, rules.fires, it.id); ok {
					da.key, _ = selective.Decide(q.Type, p, cutoff)
					da.keyLB, da.keyProbs, da.keyRule = r.Stat.WilsonLow, p, r.ID
					if rd := byRule[r.ID]; rd != nil {
						rd.eval.KeyAnswered++
					}
				}
				for _, ri := range rules.fires[it.id] {
					if ri < 0 || ri >= len(rules.rules) {
						continue
					}
					rd := byRule[rules.rules[ri].ID]
					if rd == nil {
						continue
					}
					rd.eval.Fired++
					if answerNames(rd.answer, class) {
						rd.eval.Correct++
					}
				}
				for _, rd := range byRule {
					if answerNames(rd.answer, class) {
						rd.eval.Positives++
					}
				}
				rq := &questionSet{q: q, classes: classesOf(q), counts: map[string]*SourceCount{}}
				for _, src := range plan.Teachers.For(q.Name) {
					if src.From != FromRules {
						continue
					}
					if tg, ok := rq.ruleTarget(src, rules, it.id); ok {
						dq.RulesFired++
						if tg.Class(rq.classes) == class {
							dq.RulesCorrect++
						}
					}
				}
			}
			if a, ok := ondelle[it.id][q.Name]; ok {
				if tg, err := answerTarget(q, a, plan.Tolerance["legacy"]); err == nil {
					p := tg.Mass(classesOf(q))
					sq = append(sq, Prediction{ID: it.id, Label: class, Probs: p})
					c, conf := selective.Decide(q.Type, p, cutoff)
					da.ondelle, da.ondelleConf, da.ondelleProbs = c, conf, p
					dq.correctOndelle[it.id] = c == class
					dq.OndelleConfusion[className(q, class)+"→"+className(q, c)]++
					if c == class {
						langSQ[lang]++
					}
				}
			}
			dq.answers = append(dq.answers, da)
		}
		for _, ev := range ruleEvals {
			if ev.Fired > 0 {
				ev.Precision = float64(ev.Correct) / float64(ev.Fired)
			}
			if ev.Positives > 0 {
				ev.Recall = float64(ev.Correct) / float64(ev.Positives)
			}
			dq.Rules = append(dq.Rules, *ev)
		}
		dq.Layer1 = Measure(q.Type, l1, cutoff, req.Z, plan.Requirements.Report)
		dq.Ondelle = Measure(q.Type, sq, cutoff, req.Z, plan.Requirements.Report)
		// pending items count as wrong: accuracy over every dev label
		if dq.N > 0 {
			dq.Layer1.Accuracy = dq.Layer1.Accuracy * float64(dq.Layer1.N) / float64(dq.N)
			dq.Majority = float64(hits) / float64(dq.N)
		}
		if dq.Ceiling > 0 {
			dq.OfCeiling = dq.Layer1.Accuracy / dq.Ceiling
		}
		for lang, n := range langN {
			dq.ByLanguage[lang] = LanguageAcc{N: n, Layer1: float64(langL1[lang]) / float64(n), Ondelle: float64(langSQ[lang]) / float64(n)}
		}
		out = append(out, dq)
	}
	return out
}

// className is a class's name (an option, no/yes, a level).
func className(q *engine.Question, c int) string {
	switch q.Type {
	case engine.Choice:
		return q.Options[c].Name
	case engine.Noul:
		return []string{"no", "yes"}[c]
	}
	return strconv.Itoa(c)
}

// devItems are the dev split's messages with their labels and the
// language of their own text (evidence), grouped into the languages
// with at least minLanguage items (others: "other").
func devItems(t *testing.T, gold *goldset.Set, rows map[string]int64, recs map[int64]evidence.Record, members map[int64]Member) []devItem {
	t.Helper()
	const minLanguage = 10
	var out []devItem
	count := map[string]int{}
	for id, labels := range gold.Of(goldset.Dev) {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("dev id %s names no owner message", id)
		}
		lang := "und"
		if v, ok := recs[row]["language"]; ok && v.State == evidence.OK {
			lang = v.Str
		}
		count[lang]++
		out = append(out, devItem{id: row, gold: id, group: members[row].Group, lang: lang, labels: labels})
	}
	for i := range out {
		if count[out[i].lang] < minLanguage {
			out[i].lang = "other"
		}
	}
	slices.SortFunc(out, func(a, b devItem) int { return int(a.id - b.id) })
	return out
}

// ceilingAliases are the short names WORDING-ISSUES.md uses.
var ceilingAliases = map[string]string{"cta": "call_to_action", "info": "info_sufficiency", "decision": "decision_needed"}

// readCeilings parses the agreement ceilings line of the gold sample's
// WORDING-ISSUES.md ("name .83, …"); an absent file gives none.
func readCeilings(t *testing.T, dir string, p *taxonomy.Profile) map[string]float64 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "WORDING-ISSUES.md"))
	out := map[string]float64{}
	if err != nil {
		return out
	}
	var line string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "Agreement ceilings") {
			line = l
		}
	}
	for _, m := range regexp.MustCompile(`([a-z_]+) \.(\d+)`).FindAllStringSubmatch(line, -1) {
		name := m[1]
		if a, ok := ceilingAliases[name]; ok {
			name = a
		}
		if _, ok := p.Question(name); !ok {
			t.Logf("LAYER1 ceiling for unknown question %q ignored", m[1])
			continue
		}
		v, _ := strconv.ParseFloat("0."+m[2], 64)
		out[name] = v
	}
	return out
}

// legacyOf decodes stored decisions of a key.
func legacyOf(t *testing.T, st *store.Store, key store.TriageKey) map[int64]map[string]engine.Answer {
	t.Helper()
	out, err := LoadAnswers(t.Context(), &StoreInputs{Store: st}, key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// meanAcc is the mean dev accuracy over questions of layer 1 (or Ondelle).
func meanAcc(qs []DevQuestion, ondelle bool) float64 {
	var s float64
	for _, q := range qs {
		if ondelle {
			s += q.Ondelle.Accuracy
		} else {
			s += q.Layer1.Accuracy
		}
	}
	return s / float64(max(len(qs), 1))
}

func resultsOf(qs []DevQuestion) itemResults {
	out := itemResults{}
	for _, q := range qs {
		out[q.Question] = map[string]itemResult{}
		for _, a := range q.answers {
			r := itemResult{Conf: -1}
			switch {
			case a.key >= 0:
				r = itemResult{Correct: a.key == a.class, Conf: 1 + a.keyLB}
			case a.l1 >= 0:
				r = itemResult{Correct: a.l1 == a.class, Conf: a.l1Conf}
			}
			out[q.Question][strconv.FormatInt(a.item.id, 10)] = r
		}
	}
	return out
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Name the field: the encoder reports the value, not where it lives.
		if p, bad := nonFinite(v, ""); bad {
			t.Fatalf("%s: %v (at %s)", path, err, p)
		}
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// nonFinite walks a report value and returns the first NaN or infinite
// float's path. A report that cannot be stored is a defect, not a value to
// round: the harness must say which field produced it.
func nonFinite(v any, path string) (string, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return "", false
		}
		return nonFinite(rv.Elem().Interface(), path)
	case reflect.Struct:
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if p, bad := nonFinite(rv.Field(i).Interface(), path+"."+f.Name); bad {
				return p, true
			}
		}
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if p, bad := nonFinite(rv.MapIndex(k).Interface(), fmt.Sprintf("%s[%v]", path, k.Interface())); bad {
				return p, true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if p, bad := nonFinite(rv.Index(i).Interface(), fmt.Sprintf("%s[%d]", path, i)); bad {
				return p, true
			}
		}
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return path, true
		}
	}
	return "", false
}
