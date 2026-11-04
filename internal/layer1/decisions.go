package layer1

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// rowIndex is a message → row lookup with its row count: a Design's
// feature rows, or the membership-only set the re-score reads.
type rowIndex interface {
	Len() int
	Row(id int64) (int, bool)
}

// membership is a rowIndex over a known set of message ids (no features):
// the re-score reads the configured decider's answers for the stored
// population only.
type membership map[int64]int

// membershipOf indexes ids in the order given (they are sorted by the
// caller: the rows are only a membership test).
func membershipOf(ids []int64) membership {
	m := make(membership, len(ids))
	for i, id := range ids {
		m[id] = i
	}
	return m
}

func (m membership) Len() int { return len(m) }

func (m membership) Row(id int64) (int, bool) {
	row, ok := m[id]
	return row, ok
}

// answerState is what one decider answer of a message gives a question.
type answerState uint8

const (
	answerNone      answerState = iota // no answer
	answerOK                           // a valid distribution
	answerRefused                      // the decider declined
	answerDropped                      // invalid (probabilities off, unknown option or level)
	answerAbstained                    // an abstained answer: never a target
)

// compactAnswers are one decisions source's answers to one question, per
// design row: the class distribution (float32, rows × classes) and the
// state. Answers are turned into these while they are read (one decoded
// message at a time), so a run never holds the decoded decisions of the
// whole mailbox (bugs review #8: ≈ 6 KB per message and source).
type compactAnswers struct {
	classes int
	probs   []float32
	state   []answerState
}

// readDecisions streams a decider key's answers into compact per-question
// distributions over the reader's rows (messages without a row: skipped).
func readDecisions(ctx context.Context, in Inputs, key store.TriageKey, p *taxonomy.Profile, rows rowIndex, tolerance float64) (map[string]*compactAnswers, error) {
	out := map[string]*compactAnswers{}
	for i := range p.Questions {
		q := &p.Questions[i]
		k := classesOf(q)
		out[q.Name] = &compactAnswers{classes: k, probs: make([]float32, rows.Len()*k), state: make([]answerState, rows.Len())}
	}
	err := in.Decisions(ctx, key, func(id int64, answers map[string]engine.Answer) error {
		row, ok := rows.Row(id)
		if !ok {
			return nil
		}
		for i := range p.Questions {
			q := &p.Questions[i]
			a, has := answers[q.Name]
			if !has {
				continue
			}
			c := out[q.Name]
			switch {
			case a.Refused:
				c.state[row] = answerRefused
			case a.Abstain != "":
				c.state[row] = answerAbstained
			default:
				t, err := answerTarget(q, a, tolerance)
				if err != nil {
					c.state[row] = answerDropped
					continue
				}
				c.state[row] = answerOK
				for j, m := range t.Mass(c.classes) {
					c.probs[row*c.classes+j] = float32(m)
				}
			}
		}
		return nil
	})
	return out, err
}

// distribution is row's validated stored distribution as read (ok false:
// none, or invalid). It stays the stored form: the route offers it
// without re-normalising (route.probsTolerance accepts the float32
// rounding).
func (c *compactAnswers) distribution(row int) ([]float64, bool) {
	if c == nil || c.state[row] != answerOK {
		return nil, false
	}
	p := make([]float64, c.classes)
	for j := range p {
		p[j] = float64(c.probs[row*c.classes+j])
	}
	if _, err := Soft(p, float32Sum); err != nil {
		return nil, false
	}
	return p, true
}

// top is row's most likely class and its probability (ok false: no valid
// answer). It is the decider feature block's one read.
func (c *compactAnswers) top(row int) (int, float64, bool) {
	p, ok := c.distribution(row)
	if !ok {
		return 0, 0, false
	}
	best := 0
	for i := range p {
		if p[i] > p[best] {
			best = i
		}
	}
	return best, p[best], true
}

// target is row's target (ok false: none) and its state.
func (c *compactAnswers) target(row int) (Target, answerState) {
	p, ok := c.distribution(row)
	if !ok {
		if c == nil {
			return nil, answerNone
		}
		return nil, c.state[row]
	}
	t, err := Soft(p, float32Sum)
	if err != nil {
		return nil, answerDropped
	}
	return t, answerOK
}

// float32Sum is the tolerance of a distribution validated in float64
// then stored in float32 (rounding only).
const float32Sum = 1e-5
