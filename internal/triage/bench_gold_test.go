package triage

// Export for an independent gold-label judge, and the scorer of its labels.
//
// The export directory holds (scripts/bench-deciders.sh):
//   taxonomy.json          the questions the providers answered (+ fingerprint)
//   gold-input.jsonl       {id, text}: the gold subset as the providers saw it,
//                          without history or label-example blocks, no answers
//   answers-<p>.jsonl      {id, answers}: provider p's raw answers (all sampled
//                          messages; the judge never reads these)
//   gold-labels.jsonl      {id, answers: {question: value}}: written by the judge
// Ids are a short hash of the message identity, never a row or address.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source/archive"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Export file names.
const (
	benchGoldLabels  = "gold-labels.jsonl"
	benchAnswersGlob = "answers-*.jsonl"
)

func benchAnswersFile(provider string) string { return "answers-" + provider + ".jsonl" }

type answersLine struct {
	ID      string                   `json:"id"`
	Answers map[string]engine.Answer `json:"answers"`
}

type goldLine struct {
	ID      string                     `json:"id"`
	Answers map[string]json.RawMessage `json:"answers"`
}

// exportBench writes the taxonomy, the gold subset's model view and every
// provider's answers.
func exportBench(ctx context.Context, cfg *config.Config, dir string, s *session, st *store.Store, smp *sample, first map[string]answerSet) error {
	ids := map[int64]string{}
	msgs := map[int64]mail.Message{}
	for _, row := range smp.ids {
		m, err := st.MessageByRow(ctx, row)
		if err != nil {
			return err
		}
		ids[row], msgs[row] = goldexport.ID(&m), m
	}
	write := func(name string, lines []any) error {
		var buf bytes.Buffer
		for _, l := range lines {
			b, err := json.Marshal(l)
			if err != nil {
				return err
			}
			buf.Write(append(b, '\n'))
		}
		return fsx.WriteFileAtomic(filepath.Join(dir, name), buf.Bytes(), fsx.FilePerm, cfg.Snapshot().Replace(nil))
	}
	tax, err := json.MarshalIndent(goldexport.Taxonomy{Fingerprint: s.profile.Fingerprint, Questions: s.profile.Questions}, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(filepath.Join(dir, goldexport.TaxonomyFile), append(tax, '\n'), fsx.FilePerm, cfg.Snapshot().Replace(nil)); err != nil {
		return err
	}
	view := modelView(s.composer)
	var gold []any
	for _, row := range smp.gold {
		m := msgs[row]
		gold = append(gold, goldexport.Input{ID: ids[row], Text: view.State(row, &m)})
	}
	if err := write(goldexport.InputFile, gold); err != nil {
		return err
	}
	for name, set := range first {
		var lines []any
		for _, row := range slices.Sorted(maps.Keys(set)) {
			lines = append(lines, answersLine{ID: ids[row], Answers: set[row]})
		}
		if err := write(benchAnswersFile(name), lines); err != nil {
			return err
		}
	}
	benchLog.Info("bench export written", "gold", len(gold), "providers", len(first))
	return nil
}

// goldReport scores every provider's answers against the gold labels.
type goldReport struct {
	Fingerprint string                   `json:"fingerprint"`
	Labelled    int                      `json:"labelled_messages"`
	Labels      int                      `json:"labels"`
	Providers   map[string]*goldProvider `json:"providers"`
}

// goldProvider: Questions are feedback.Evaluate's per-question scores
// (choice accuracy; noul accuracy/precision/recall/F1 at the profile
// cutoff, 0.5 for every built-in noul question; score MAE and
// within_half_level = |Δ| ≤ 0.5).
type goldProvider struct {
	Answered   int                     `json:"answered_messages"`
	Unanswered int                     `json:"unanswered_labels"`
	Questions  []feedback.QuestionEval `json:"questions"`
}

// scoreGold reads the export directory and scores every answers file
// against gold-labels.jsonl. Every problem (unknown id or question,
// invalid value, an answers file that does not match the taxonomy) is
// reported at once; nothing is scored then.
func scoreGold(dir string) (*goldReport, error) {
	p, err := benchProfile(dir)
	if err != nil {
		return nil, err
	}
	exported := map[string]bool{}
	if err := readJSONL(filepath.Join(dir, goldexport.InputFile), func(_ int, raw []byte) error {
		var g goldexport.Input
		if err := jsonx.DecodeStrict(raw, &g); err != nil {
			return err
		}
		exported[g.ID] = true
		return nil
	}); err != nil {
		return nil, err
	}
	index := map[string]int64{}
	var labels []feedback.Label
	var problems []string
	err = readJSONL(filepath.Join(dir, benchGoldLabels), func(n int, raw []byte) error {
		var g goldLine
		if err := jsonx.DecodeStrict(raw, &g); err != nil {
			return err
		}
		switch _, dup := index[g.ID]; {
		case !exported[g.ID]:
			problems = append(problems, fmt.Sprintf("%s line %d: id %q was not exported", benchGoldLabels, n, g.ID))
			return nil
		case dup:
			problems = append(problems, fmt.Sprintf("%s line %d: id %q labelled twice", benchGoldLabels, n, g.ID))
			return nil
		}
		index[g.ID] = int64(len(index) + 1)
		for _, q := range slices.Sorted(maps.Keys(g.Answers)) {
			l, err := goldLabel(p, q, g.Answers[q])
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s line %d: %v", benchGoldLabels, n, err))
				continue
			}
			l.MessageID = index[g.ID]
			labels = append(labels, l)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	answers, aproblems, err := readAnswers(dir, p, index)
	if err != nil {
		return nil, err
	}
	if problems = append(problems, aproblems...); len(problems) > 0 {
		return nil, fmt.Errorf("bench: gold scoring refused:\n  - %s", strings.Join(problems, "\n  - "))
	}
	rep := &goldReport{Fingerprint: p.Fingerprint, Labelled: len(index), Labels: len(labels), Providers: map[string]*goldProvider{}}
	for name, judged := range answers {
		gp := &goldProvider{Answered: len(judged)}
		for _, l := range labels {
			if _, ok := judged[l.MessageID][l.Question]; !ok {
				gp.Unanswered++
			}
		}
		gp.Questions = feedback.Evaluate(judged, labels, nil, p).Manual
		rep.Providers[name] = gp
	}
	return rep, nil
}

// benchProfile is the built-in profile, which must be the one exported.
func benchProfile(dir string) (*taxonomy.Profile, error) {
	raw, err := os.ReadFile(filepath.Join(dir, goldexport.TaxonomyFile))
	if err != nil {
		return nil, fmt.Errorf("bench: %w", err)
	}
	var tax goldexport.Taxonomy
	if err := jsonx.DecodeStrict(raw, &tax); err != nil {
		return nil, fmt.Errorf("bench: %s: %w", goldexport.TaxonomyFile, err)
	}
	p, err := taxonomy.Default()
	if err != nil {
		return nil, err
	}
	if p.Fingerprint != tax.Fingerprint {
		return nil, fmt.Errorf("bench: the export's taxonomy %s is not the built-in profile %s", tax.Fingerprint, p.Fingerprint)
	}
	return p, nil
}

func goldLabel(p *taxonomy.Profile, question string, raw json.RawMessage) (feedback.Label, error) {
	q, ok := p.Question(question)
	if !ok {
		return feedback.Label{}, fmt.Errorf("unknown question %q", question)
	}
	v, err := jsonx.DecodeOne(raw)
	if err != nil {
		return feedback.Label{}, fmt.Errorf("%s: %v", question, err)
	}
	l, err := taxonomy.ParseLabel(q, v)
	if err != nil {
		return feedback.Label{}, err
	}
	// The gold sample is this benchmark's reference truth: feedback scores
	// manual labels only, so the harness classifies it as manual.
	return feedback.Label{Question: q.Name, Type: q.Type, Value: l, Source: store.LabelManual}, nil
}

// readAnswers reads every answers file into judged values keyed by the
// gold index (messages without a gold label are skipped); answers that do
// not match the taxonomy are problems.
func readAnswers(dir string, p *taxonomy.Profile, index map[string]int64) (map[string]feedback.Judged, []string, error) {
	files, err := filepath.Glob(filepath.Join(dir, benchAnswersGlob))
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("bench: no %s in %s", benchAnswersGlob, dir)
	}
	out := map[string]feedback.Judged{}
	var problems []string
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "answers-"), ".jsonl")
		judged := feedback.Judged{}
		err := readJSONL(f, func(n int, raw []byte) error {
			var a answersLine
			if err := jsonx.DecodeStrict(raw, &a); err != nil {
				return err
			}
			values := map[string]taxonomy.Value{}
			for q, ans := range a.Answers {
				if qq, ok := p.Question(q); !ok || qq.Type != ans.Type {
					problems = append(problems, fmt.Sprintf("%s line %d: answer %q does not match the taxonomy", filepath.Base(f), n, q))
					continue
				}
				values[q] = taxonomy.FromAnswer(ans)
			}
			if row, ok := index[a.ID]; ok {
				judged[row] = values
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		out[name] = judged
	}
	return out, problems, nil
}

// readJSONL calls fn for every non-empty line (1-based numbers), through
// the one JSONL reader (archive.EachLine) at the schema's line cap.
func readJSONL(path string, fn func(n int, raw []byte) error) error {
	d, err := config.Defaults()
	if err != nil {
		return err
	}
	if err := archive.EachLine(path, d.Int("archive.max_line_bytes"), fn); err != nil {
		return fmt.Errorf("bench: %w", err)
	}
	return nil
}
