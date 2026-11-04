package layer1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

var testKey = source.StoreKey("src", "store-1")

// writeTestProviders installs the complete provider records the layer-1
// tests select (no production default exists).
func writeTestProviders(t *testing.T, cfg *config.Config) {
	t.Helper()
	records := map[string]engine.Provider{
		"ondelle": {Protocol: engine.ProtocolSystemOne, BaseURL: "http://127.0.0.1:9/v1", Path: "/systemone",
			Model: "snap-1", APIKeySecret: "PROVIDER_DECIDER_API_KEY", MaxQuestions: 16, ContextTokens: 100000,
			CharsPerToken: 3, ReserveTokensPerByte: 1, ProbabilityTolerance: 0.02,
			PriceInput: ptr(0.05), PriceOutput: ptr(0)},
		"clef": {Protocol: engine.ProtocolSystemOne, BaseURL: "http://127.0.0.1:9/v1", Path: "/systemone",
			Model: "clef", APIKeySecret: "PROVIDER_CLEF_API_KEY", MaxQuestions: 16, ContextTokens: 100000,
			CharsPerToken: 3, ReserveTokensPerByte: 1, ProbabilityTolerance: 0.02,
			PriceInput: ptr(0.05), PriceOutput: ptr(0)},
		"example-provider-gemma": {Protocol: engine.ProtocolOpenAIChat, BaseURL: "http://127.0.0.1:9/v1", Path: "/chat/completions",
			Model: "gemma-test", APIKeySecret: "PROVIDER_JUDGE_API_KEY", ContextTokens: 100000, CharsPerToken: 3,
			ReserveTokensPerByte: 1, ReserveOutputTokens: 1024, ResponseFormat: engine.ResponseFormatJSON,
			PriceInput: ptr(0.1), PriceOutput: ptr(0.2)},
		"embeddinggemma": {Protocol: engine.ProtocolOpenAIEmbeddings, Runtime: "llama-server", Path: "/v1/embeddings",
			Model: "embeddinggemma-2", CharsPerToken: 5,
			Embedding: &engine.EmbeddingSpec{Dimensions: 768, Matryoshka: []int{512, 256, 128},
				QueryTemplate: "task: search result | query: {text}", DocumentTemplate: "title: {title} | text: {text}",
				EmptyTitle: "none", NormTolerance: 0.01, MaxBatch: 32, ReservedTokens: 8}},
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "providers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func ptr(v float64) *float64 { return &v }

// testConfig is a temp config with the layer-1 provider records installed
// and the role selectors pointing at them.
func testConfig(t *testing.T, overrides map[string]any) *config.Config {
	t.Helper()
	cfg := configtest.New(t, overrides)
	writeTestProviders(t, cfg)
	for key, id := range map[string]string{triage.DeciderSetting: "ondelle", "judge.provider": "example-provider-gemma", "embed.provider": "embeddinggemma"} {
		if _, ok := overrides[key]; ok {
			continue
		}
		if err := cfg.Set(key, id); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// fakeEmbedder names a vector space; the test writes its vectors itself.
type fakeEmbedder struct{ engine.Embedder }

func (fakeEmbedder) Space() string   { return "fake/space/8" }
func (fakeEmbedder) Dimensions() int { return 8 }

type fakeSource struct{}

func (fakeSource) Embedder() (engine.Embedder, error) { return fakeEmbedder{}, nil }

// world is a store with n received owner messages of two kinds (their
// vectors and their senders differ), the ondelle teacher's answers to the
// category question and judge labels on some.
type world struct {
	t   *testing.T
	cfg *config.Config
	st  *store.Store
	ev  *evidence.Engine
	r   *Runner
	ids []int64
	p   *taxonomy.Profile
}

func newWorld(t *testing.T, n int, overrides map[string]any) *world {
	t.Helper()
	settings := map[string]any{source.SettingSelected: []any{testKey}, source.SettingDeclared: []any{testKey + "=" + mailtest.Owner},
		"time.zone": "Europe/Paris", SettingFolds: 3, SettingGrid: []any{-2, -1}, SettingWorkers: 2,
		SettingShardRows: 16, triage.DeciderSetting: "ondelle", "judge.provider": "example-provider-gemma", "embed.provider": "embeddinggemma"}
	for k, v := range overrides {
		settings[k] = v
	}
	w := &world{t: t, cfg: configtest.New(t, settings)}
	writeTestProviders(t, w.cfg)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_eval_labels": 6, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), w.cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w.st = st
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{Bodies: true, Recipients: true, Headers: true,
		FolderRoles: true, OwnerIdentity: true}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	var msgs []mail.Message
	for i := range n {
		m := mailtest.Message("src", fmt.Sprintf("m%03d", i))
		m.StoreID = "store-1"
		kind := i % 2
		m.SenderAddress = []string{"billing@vendor.example", "colleague@corp.example"}[kind]
		m.SentRepresentingAddress = m.SenderAddress
		m.Subject, m.Body = fmt.Sprintf("subject %d", i), strings.Repeat("words ", 5+kind*20)
		m.Recipients = []mail.Recipient{{Kind: mail.To, Address: mailtest.Owner}}
		m.ReceivedAt = t0.Add(time.Duration(i) * time.Hour).Format(mail.TimeLayout)
		m.SentOn, m.InternetMessageID, m.InReplyTo, m.References, m.ConversationID = m.ReceivedAt, fmt.Sprintf("<m%d@x>", i), "", "", ""
		msgs = append(msgs, m)
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	var verdicts []store.VerdictRow
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		w.ids = append(w.ids, r.RowID)
		verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch, Revision: r.Revision})
		return nil
	})
	if err := st.WriteVerdicts(t.Context(), mailtest.Owner, verdicts); err != nil {
		t.Fatal(err)
	}
	if w.ev, err = evidence.New(evidence.Deps{Config: w.cfg, Store: st}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ev.Run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	w.vectors()
	if w.p, err = taxonomy.Load(w.cfg); err != nil {
		t.Fatal(err)
	}
	w.teach()
	if w.r, err = New(Deps{Config: w.cfg, Store: st, Evidence: w.ev, Embedder: fakeSource{}}); err != nil {
		t.Fatal(err)
	}
	return w
}

// kind is the class of a message (its index parity).
func (w *world) kind(i int) int { return i % 2 }

func (w *world) vectors() {
	r := rand.New(rand.NewPCG(1, 1))
	var rows []store.EmbeddingRow
	for i, id := range w.ids {
		v := make([]float32, 8)
		for j := range v {
			v[j] = float32(r.NormFloat64() * 0.2)
		}
		v[w.kind(i)] += 1
		u, _ := vector.Truncate(v, 8)
		rows = append(rows, store.EmbeddingRow{MessageID: id, Space: "fake/space/8", TextHash: fmt.Sprint(i), Vector: vector.Quantize(u)})
	}
	if err := w.st.SaveEmbeddings(w.t.Context(), rows); err != nil {
		w.t.Fatal(err)
	}
}

// teach stores the decider's category answers (finance or work, soft)
// and judge category labels on every third message.
func (w *world) teach() {
	plan, err := NewPlan(w.cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	key := store.TriageKey{Fingerprint: w.p.Fingerprint, Decider: plan.DeciderKey}
	var ds []store.Decision
	for i, id := range w.ids {
		probs := map[string]float64{"action": 0.02, "finance": 0.02, "marketing": 0.02, "notifications": 0.02, "security": 0.02, "spam": 0.02, "work": 0.02}
		best := []string{"finance", "work"}[w.kind(i)]
		probs[best] = 0.88
		a := map[string]engine.Answer{"category": {Type: engine.Choice, Choice: best, Probabilities: probs}}
		raw, _ := json.Marshal(a)
		ds = append(ds, store.Decision{MessageID: id, Answers: string(raw)})
		if i%3 == 0 {
			if err := w.st.PutLabel(w.t.Context(), id, "category", fmt.Sprintf("%q", best), "judge"); err != nil {
				w.t.Fatal(err)
			}
		}
	}
	if err := w.st.SaveDecisions(w.t.Context(), key, ds); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) run() Report {
	w.t.Helper()
	rep, err := w.r.Run(w.t.Context(), nil)
	if err != nil {
		w.t.Fatalf("%v %+v", err, rep)
	}
	return rep
}

func (w *world) answers() map[int64]map[string]engine.Answer {
	w.t.Helper()
	out, err := LoadAnswers(w.t.Context(), &StoreInputs{Store: w.st}, store.TriageKey{Fingerprint: w.p.Fingerprint, Decider: Decider})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func TestRunTrainsPromotesAndAnswers(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	rep := w.run()
	if rep.State != runstate.Succeeded || len(rep.Promoted) != 1 || rep.Promoted[0] != "category" {
		t.Fatalf("%+v", rep)
	}
	answers := w.answers()
	if len(answers) != 60 {
		t.Fatalf("answered %d of 60", len(answers))
	}
	right := 0
	cq, _ := w.p.Question("category")
	for i, id := range w.ids {
		a := answers[id]["category"]
		if a.Abstain == "" || a.Choice != "" || a.Confidence != nil {
			t.Fatalf("20 evaluation labels certify nothing at 0.99: every answer abstains, without a usable value: %+v", a)
		}
		if p, ok := selective.Probs(cq, a); ok && cq.Options[argmax(p)].Name == []string{"finance", "work"}[w.kind(i)] {
			right++
		}
		if _, other := answers[id]["urgency"]; other {
			t.Fatal("a question without a teacher was answered")
		}
	}
	if right < 57 {
		t.Fatalf("layer 1 agrees with the teacher on %d of 60", right)
	}
	var cat QuestionReport
	for _, q := range rep.Questions {
		if q.Question == "category" {
			cat = q
		}
	}
	if cat.Wall <= 0 {
		t.Error("the question's wall time is not reported") // regression: a deferred write to an unnamed result
	}
	if cat.Eval != 20 || cat.OOF == nil || cat.Sources["judge"].Used != 20 || cat.Sources["decider"].Used != 40 || cat.Sources["decider"].Targets != 60 {
		t.Fatalf("category %+v", cat)
	}
	// the stage: blocked by the questions without a teacher, each named
	st, err := w.r.Stage().Status(t.Context())
	if err != nil || st.State != learning.Blocked {
		t.Fatalf("%+v %v", st, err)
	}
	codes := map[string]string{}
	for _, b := range st.Blockers {
		codes[b.Subject] = b.Code
		if len(b.Actions) == 0 {
			t.Errorf("blocker %+v without an action", b)
		}
	}
	if codes["urgency"] != BlockNoTeacher || codes["category"] != BlockTooFewLabels { // 60 messages: below min_effective_train
		t.Fatalf("blockers %v", codes)
	}
	// nothing changed: the next run keeps the same active model
	again := w.run()
	if len(again.Promoted) != 0 {
		t.Fatalf("an unchanged run promoted %v", again.Promoted)
	}
	active, _ := w.st.ActiveLayer1(t.Context())
	if len(active) != 1 {
		t.Fatalf("%d active models", len(active))
	}
}

func TestStageFollowsItsInputs(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	pending := &fixedStage{id: "evidence", state: learning.Running}
	st, _ := w.r.Stage(pending).Status(t.Context())
	if st.State != learning.Pending {
		t.Fatalf("an earlier stage not done: %+v", st)
	}
	if st, _ := w.r.Stage().Status(t.Context()); st.State != learning.Running {
		t.Fatalf("never trained: %+v", st)
	}
	w.run()
	// a few new labels of a source that does not evaluate (fewer than
	// layer1.retrain.min_new_labels) keep it current
	_ = w.st.PutLabel(t.Context(), w.ids[1], "category", `"work"`, "imported")
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != current {
		t.Fatalf("one new label made the run stale: %+v", lr)
	}
	// an evaluation label: the verdicts are void until re-scored
	_ = w.st.PutLabel(t.Context(), w.ids[1], "category", `"work"`, "judge")
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != relabelled {
		t.Fatalf("an evaluation label change: %+v", lr)
	}
	if st, _ := w.r.QuestionsStage().Status(t.Context()); st.State != learning.Running {
		t.Fatalf("questions stage with void verdicts: %+v", st)
	}
	if rep, err := w.r.Rescore(t.Context()); err != nil || !rep.Rescored {
		t.Fatalf("re-score %+v %v", rep, err)
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != current {
		t.Fatalf("after the re-score: %+v", lr)
	}
	// a setting layer 1 reads makes it stale
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.r.Stage().Status(t.Context()); st.State != learning.Running {
		t.Fatalf("after a settings change: %+v", st)
	}
}

type fixedStage struct {
	id    string
	state learning.State
}

func (f *fixedStage) ID() string { return f.id }
func (f *fixedStage) Status(context.Context) (learning.StageStatus, error) {
	return learning.StageStatus{State: f.state}, nil
}

func TestACanceledRunKeepsTheActiveModels(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 40, nil)
	w.run()
	before := w.answers()
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil { // new inputs: a real run
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	rep, err := w.r.Run(ctx, func(jobs.Progress) { cancel() }) // cancelled after the first question
	if err == nil || rep.State != runstate.Cancelled || rep.RunID == 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if active, _ := w.st.ActiveLayer1(t.Context()); len(active) != 1 {
		t.Fatal("a cancelled run removed the active model")
	}
	if len(w.answers()) != len(before) {
		t.Fatal("a cancelled run removed the answers")
	}
	if st, _ := w.r.Stage().Status(t.Context()); st.State != learning.Running {
		t.Fatalf("after a cancelled run: running again, not done (%+v)", st.State)
	}
}

func TestBusyAndPromotionRefusal(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 40, map[string]any{SettingMaxNLLRegression: 0})
	if !w.r.single.Begin() {
		t.Fatal("slot")
	}
	if _, err := w.r.Run(t.Context(), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	w.r.single.End()
	w.run()
	// an active model of the same rows that looks far better than any candidate
	active, _ := w.st.ActiveLayer1(t.Context())
	a := active[0]
	var m Metrics
	_ = json.Unmarshal([]byte(a.Metrics), &m)
	m.NLL = 0
	raw, _ := json.Marshal(m)
	better := a
	better.VersionKey, better.Metrics = "better", string(raw)
	id, err := w.st.SaveLayer1Candidate(t.Context(), better)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.PromoteLayer1(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil { // a new version
		t.Fatal(err)
	}
	rep := w.run()
	if len(rep.Promoted) != 0 || !strings.Contains(rep.Kept["category"], "worse") {
		t.Fatalf("a regression was promoted: promoted %v kept %v", rep.Promoted, rep.Kept)
	}
	// a corrupt active blob is ignored: the next candidate replaces it
	corrupt := a
	corrupt.VersionKey, corrupt.Params = "corrupt", []byte{0}
	if id, err = w.st.SaveLayer1Candidate(t.Context(), corrupt); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PromoteLayer1(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingGrid, []any{-4}); err != nil {
		t.Fatal(err)
	}
	if rep := w.run(); len(rep.Promoted) != 1 {
		t.Fatalf("a corrupt active model blocked promotion: %+v", rep)
	}
	if active, _ := w.st.ActiveLayer1(t.Context()); len(active) != 1 || active[0].VersionKey == "corrupt" {
		t.Fatalf("one active model per question: %+v", active)
	}
}

func TestColdStartTrainsOnEvaluationLabelsAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	if err := writeTeachers(w.cfg, `{"sources": {"decider": null, "legacy": null}}`); err != nil {
		t.Fatal(err)
	}
	rep := w.run()
	var cat QuestionReport
	for _, q := range rep.Questions {
		if q.Question == "category" {
			cat = q
		}
	}
	if cat.Train != 20 || cat.Eval != 20 || cat.OOF == nil || cat.OOF.Accuracy < 0.8 {
		t.Fatalf("cold start %+v", cat)
	}
}

func writeTeachers(cfg *config.Config, doc string) error {
	return os.WriteFile(cfg.Paths().Resolve(cfg.String(SettingTeachersFile)), []byte(doc), 0o600)
}

func writeRequirements(cfg *config.Config, doc string) error {
	return os.WriteFile(cfg.Paths().Resolve(cfg.String(SettingRequirementsFile)), []byte(doc), 0o600)
}

// excluding wraps inputs so that some messages never get a target (the
// dev harness's guard relies on TargetIDs to prove it).
type excluding struct {
	*StoreInputs
	out map[int64]bool
}

func (e excluding) Labels(ctx context.Context) ([]store.LabelRow, error) {
	all, err := e.StoreInputs.Labels(ctx)
	var keep []store.LabelRow
	for _, l := range all {
		if !e.out[l.MessageID] {
			keep = append(keep, l)
		}
	}
	return keep, err
}

func (e excluding) Decisions(ctx context.Context, k store.TriageKey, fn func(int64, map[string]engine.Answer) error) error {
	return e.StoreInputs.Decisions(ctx, k, func(id int64, a map[string]engine.Answer) error {
		if e.out[id] {
			return nil
		}
		return fn(id, a)
	})
}

func TestTargetIDsShowEveryMessageThatTrained(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	base := &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}}
	tr, err := Train(t.Context(), plan, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids := tr.TargetIDs(); !ids[w.ids[0]] || !ids[w.ids[1]] {
		t.Fatal("trained messages missing from TargetIDs")
	}
	out := map[int64]bool{w.ids[0]: true, w.ids[3]: true} // a judge-labelled message and another
	tr, err = Train(t.Context(), plan, excluding{base, out}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := tr.TargetIDs()
	if ids[w.ids[0]] || ids[w.ids[3]] || !ids[w.ids[1]] {
		t.Fatal("an excluded message still has a target")
	}
	// excluded messages are still answered (their rows exist)
	if _, ok := tr.Predict("category", w.ids[0]); !ok {
		t.Fatal("an excluded message is not predicted")
	}
}

func TestQuestionsStageReadsTheRequirements(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	req := `{"defaults": {"min_effective_train": 10, "min_eval_labels": 6, "target_accuracy": 0.6, "min_coverage": 0.3, "z": 1.0, "family_wise": false, "request_labels": 40}}`
	if err := writeRequirements(w.cfg, req); err != nil {
		t.Fatal(err)
	}
	rep := w.run()
	var cat Verdict
	for _, v := range rep.Verdicts {
		if v.Question == "category" {
			cat = v
		}
	}
	if !cat.Meets || cat.Coverage < 0.3 {
		t.Fatalf("category %+v", cat)
	}
	st, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil || st.State != learning.Done || st.Progress.Done != 1 || st.Progress.Total != 17 {
		t.Fatalf("%+v %v", st, err)
	}
	for _, b := range st.Blockers {
		if b.Code != BlockRequirement || b.Subject == "category" {
			t.Fatalf("blocker %+v", b)
		}
	}
	// the layer1 stage itself blocks only on what it cannot train
	l1, _ := w.r.Stage().Status(t.Context())
	for _, b := range l1.Blockers {
		if b.Subject == "category" || b.Code == BlockRequirement {
			t.Fatalf("layer1 blocker %+v", b)
		}
		if b.Code == BlockNoTeacher && b.Actions[1].Params["count"] != "40" {
			t.Fatalf("the judge is asked for request_labels: %+v", b.Actions)
		}
	}
}
