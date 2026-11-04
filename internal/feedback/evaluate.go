package feedback

import (
	"maps"
	"math"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Judged is the triage result set: message row id → question → value.
type Judged map[int64]map[string]taxonomy.Value

// Confusion is a confusion matrix (rows: actual, columns: predicted).
type Confusion struct {
	Columns []string       `json:"columns"`
	Rows    []ConfusionRow `json:"rows"`
}

// ConfusionRow is one actual value's predictions.
type ConfusionRow struct {
	Actual string `json:"actual"`
	Counts []int  `json:"counts"`
}

// QuestionEval scores one question.
type QuestionEval struct {
	Question  string              `json:"question"`
	Kind      engine.QuestionType `json:"kind"`
	Count     int                 `json:"count"`
	Metrics   map[string]float64  `json:"metrics"`
	Confusion *Confusion          `json:"confusion,omitempty"`
}

// Evaluation is the score of the pipeline against the owner's labels and
// against weak labels from reply history.
type Evaluation struct {
	// Manual is the reference score: manual and judge labels only, never
	// the model's own production answer. The JSON name is historical.
	Manual []QuestionEval `json:"manual"`
	// Sources is how many labels of each known source were loaded
	// (manual, judge, judge_answer). Only Reference labels are scored:
	// the counts make the teaching data a deployment actually has visible,
	// including a zero, and carry no prompt or body.
	Sources map[string]int `json:"sources"`
	// WeakReply compares "did the owner reply" with the reply question,
	// on the MODEL's value: the blended value is 70% a prior learned from
	// those same replies (the legacy evaluation leak).
	WeakReply *QuestionEval `json:"weak_reply,omitempty"`
}

// metricPlaces is how metrics are rounded (half-up).
const metricPlaces = 3

// Evaluate scores answered judged values against Reference labels (manual and
// the judge's independent reference labels, one per message and question;
// never the model's production answer) and weak reply labels (model values:
// no leak). Sources reports every loaded source, before Reference resolves
// duplicate pairs, so the quality view shows the deployment's real
// manual/judge/judge_answer label volume.
func Evaluate(judged Judged, labels []Label, weak map[int64]bool, p *taxonomy.Profile) Evaluation {
	ev := Evaluation{Sources: SourceCounts(labels)}
	labels = Reference(labels)
	by := map[string][]Label{}
	for _, l := range labels {
		if _, ok := judged[l.MessageID][l.Question]; ok {
			by[l.Question] = append(by[l.Question], l)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(by)) {
		q, _ := p.Question(name)
		ev.Manual = append(ev.Manual, evalQuestion(p, q, by[name], judged))
	}
	if reply, ok := p.RoleQuestion(taxonomy.RoleReply); ok {
		ev.WeakReply = weakReply(judged, weak, reply.Name, p.Cutoff(reply.Name))
	}
	return ev
}

// weakReply scores the model's reply probability against the weak labels:
// accuracy, precision, recall and F1 at the cutoff, plus the ranking (AUC)
// and calibration (Brier score) of the probability itself. Only an answered
// value carries the model probability: an abstained or refused value is no
// prediction at all.
func weakReply(judged Judged, weak map[int64]bool, question string, cutoff float64) *QuestionEval {
	var pairs [][2]bool
	var probs []float64
	var outcomes []bool
	for _, id := range slices.Sorted(maps.Keys(weak)) {
		if v, ok := judged[id][question]; ok && v.Answered() {
			pairs = append(pairs, [2]bool{weak[id], v.Model >= cutoff})
			probs, outcomes = append(probs, v.Model), append(outcomes, weak[id])
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	e := binary(question, pairs)
	if auc, ok := mathx.AUC(probs, outcomes); ok {
		e.Metrics["auc"] = round3(auc)
	}
	if brier, ok := mathx.Brier(probs, outcomes); ok {
		e.Metrics["brier"] = round3(brier)
	}
	return &e
}

// evalQuestion compares the answered values of one question with its
// reference labels. An abstained or refused value is not a prediction (its
// choice and number are empty/zero by construction), so it is skipped.
func evalQuestion(p *taxonomy.Profile, q *engine.Question, labels []Label, judged Judged) QuestionEval {
	switch q.Type {
	case engine.Choice:
		var pairs [][2]string
		for _, l := range labels {
			if v, ok := judged[l.MessageID][q.Name]; ok && v.Answered() {
				pairs = append(pairs, [2]string{l.Value.Choice, v.Choice})
			}
		}
		return choiceEval(q.Name, pairs)
	case engine.Noul:
		var pairs [][2]bool
		for _, l := range labels {
			if v, ok := judged[l.MessageID][q.Name]; ok && v.Answered() {
				pairs = append(pairs, [2]bool{l.Positive(p), v.Number >= p.Cutoff(q.Name)})
			}
		}
		return binary(q.Name, pairs)
	}
	var errs []float64
	for _, l := range labels {
		if v, ok := judged[l.MessageID][q.Name]; ok && v.Answered() {
			errs = append(errs, math.Abs(v.Number-l.Value.Number))
		}
	}
	return scoreEval(q.Name, errs)
}

// confusion counts tp, fp, tn, fn of (actual, predicted) pairs.
type confusion struct{ tp, fp, tn, fn int }

func count(pairs [][2]bool) confusion {
	var c confusion
	for _, p := range pairs {
		switch {
		case p[0] && p[1]:
			c.tp++
		case !p[0] && p[1]:
			c.fp++
		case !p[0] && !p[1]:
			c.tn++
		default:
			c.fn++
		}
	}
	return c
}

func (c confusion) scores() (precision, recall, f1, accuracy float64) {
	precision = mathx.Ratio(c.tp, c.tp+c.fp)
	recall = mathx.Ratio(c.tp, c.tp+c.fn)
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}
	return precision, recall, f1, mathx.Ratio(c.tp+c.tn, c.tp+c.fp+c.tn+c.fn)
}

func binary(name string, pairs [][2]bool) QuestionEval {
	c := count(pairs)
	p, r, f1, acc := c.scores()
	return QuestionEval{Question: name, Kind: engine.Noul, Count: len(pairs),
		Metrics:   map[string]float64{"accuracy": round3(acc), "precision": round3(p), "recall": round3(r), "f1": round3(f1)},
		Confusion: &Confusion{Columns: []string{"no", "yes"}, Rows: []ConfusionRow{{"no", []int{c.tn, c.fp}}, {"yes", []int{c.fn, c.tp}}}}}
}

func choiceEval(name string, pairs [][2]string) QuestionEval {
	correct := 0
	per := map[string]map[string]int{}
	cols := map[string]bool{}
	for _, p := range pairs {
		if p[0] == p[1] {
			correct++
		}
		if per[p[0]] == nil {
			per[p[0]] = map[string]int{}
		}
		per[p[0]][p[1]]++
		cols[p[0]], cols[p[1]] = true, true
	}
	columns := slices.Sorted(maps.Keys(cols))
	ev := QuestionEval{Question: name, Kind: engine.Choice, Count: len(pairs),
		Metrics: map[string]float64{"accuracy": round3(mathx.Ratio(correct, len(pairs)))}, Confusion: &Confusion{Columns: columns}}
	for _, actual := range slices.Sorted(maps.Keys(per)) {
		row := ConfusionRow{Actual: actual}
		support := 0
		for _, c := range columns {
			row.Counts = append(row.Counts, per[actual][c])
			support += per[actual][c]
		}
		ev.Confusion.Rows = append(ev.Confusion.Rows, row)
		ev.Metrics["recall["+actual+"]"] = round3(mathx.Ratio(per[actual][actual], support))
	}
	return ev
}

func scoreEval(name string, errs []float64) QuestionEval {
	within := 0
	for _, e := range errs {
		if e <= 0.5 {
			within++
		}
	}
	mae, _ := mathx.Mean(errs)
	return QuestionEval{Question: name, Kind: engine.Score, Count: len(errs),
		Metrics: map[string]float64{"mean_absolute_error": round3(mae), "within_half_level": round3(mathx.Ratio(within, len(errs)))}}
}

func round3(v float64) float64 { return mathx.Round(v, metricPlaces) }
