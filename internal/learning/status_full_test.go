package learning_test

// A full stage fan-out over a real store: the learned-looking mailbox of
// status_perf_test plus the real evidence, index, layer-1, calibration and
// questions stages. It is the parity and measurement harness: the stage
// constructors and their order are exactly wireLearning's, so the JSON is
// the status the UI reads.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

type fullWorld struct {
	cfg    *config.Config
	st     *store.Store
	ev     *evidence.Engine
	ix     *index.Indexer
	run    *layer1.Runner
	ids    []int64
	stages []learning.Stage
	clk    *clocktest.Fake
}

func ptrFloat(v float64) *float64 { return &v }

func writeProviders(t testing.TB, cfg *config.Config) {
	t.Helper()
	records := map[string]engine.Provider{
		"subq": {Protocol: engine.ProtocolSystemOne, BaseURL: "http://127.0.0.1:9/v1", Path: "/systemone",
			Model: "snap-1", APIKeySecret: "PROVIDER_DECIDER_API_KEY", MaxQuestions: 16, ContextTokens: 100000,
			CharsPerToken: 3, ReserveTokensPerByte: 1, ProbabilityTolerance: 0.02,
			PriceInput: ptrFloat(0.05), PriceOutput: ptrFloat(0)},
		"example-provider-gemma": {Protocol: engine.ProtocolOpenAIChat, BaseURL: "http://127.0.0.1:9/v1", Path: "/chat/completions",
			Model: "gemma-test", APIKeySecret: "PROVIDER_JUDGE_API_KEY", ContextTokens: 100000, CharsPerToken: 3,
			ReserveTokensPerByte: 1, ReserveOutputTokens: 1024, ResponseFormat: engine.ResponseFormatJSON,
			PriceInput: ptrFloat(0.1), PriceOutput: ptrFloat(0.2)},
		"embeddinggemma": {Protocol: engine.ProtocolOpenAIEmbeddings, Runtime: "llama-server", Path: "/v1/embeddings",
			Model: "embeddinggemma-2", CharsPerToken: 5,
			Embedding: &engine.EmbeddingSpec{Dimensions: 8, QueryTemplate: "q: {text}", DocumentTemplate: "d: {title} {text}",
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

func newFullWorld(tb testing.TB, n int) *fullWorld {
	tb.Helper()
	cfg := configtest.New(tb, map[string]any{
		source.SettingSelected:  []any{perfKey},
		source.SettingDeclared:  []any{perfKey + "=" + mailtest.Owner},
		"time.zone":             "UTC",
		layer1.SettingFolds:     3,
		layer1.SettingGrid:      []any{-2, -1},
		layer1.SettingWorkers:   2,
		layer1.SettingShardRows: 16,
		"triage.decider":        "subq",
		"judge.provider":        "example-provider-gemma",
		"embed.provider":        "embeddinggemma",
	})
	writeProviders(tb, cfg)
	if err := os.WriteFile(cfg.Paths().Resolve(cfg.String(layer1.SettingRequirementsFile)),
		[]byte(`{"defaults": {"min_eval_labels": 6, "family_wise": false}}`), 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := cfg.Set("embed.dimensions", 8); err != nil {
		tb.Fatal(err)
	}
	st, err := store.Open(tb.Context(), cfg, nil, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	caps := source.Capabilities{Bodies: true, Recipients: true, Headers: true, FolderRoles: true, OwnerIdentity: true, Incremental: true}
	if err := st.UpsertSource(tb.Context(), "src", "test", "Test", caps); err != nil {
		tb.Fatal(err)
	}
	if err := st.SaveMailboxes(tb.Context(), "src", []source.Mailbox{{
		Key: perfKey, SourceID: "src", StoreID: "store-1", Name: "Test", Kind: mail.StorePrimary,
		Folders: 1, Items: int64(n), IdentityComplete: true,
	}}); err != nil {
		tb.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	msgs := make([]mail.Message, n)
	for i := range msgs {
		m := mailtest.Message("src", fmt.Sprintf("m%03d", i))
		kind := i % 2
		m.SenderAddress = []string{"billing@vendor.example", "colleague@corp.example"}[kind]
		m.SentRepresentingAddress = m.SenderAddress
		m.Subject, m.Body = fmt.Sprintf("subject %d", i), strings.Repeat("words ", 5+kind*20)
		m.ReceivedAt = t0.Add(time.Duration(i) * time.Hour).Format(mail.TimeLayout)
		m.SentOn, m.InternetMessageID, m.InReplyTo, m.References, m.ConversationID = m.ReceivedAt, fmt.Sprintf("<m%d@x>", i), "", "", ""
		msgs[i] = m
	}
	if err := st.UpsertMessages(tb.Context(), 0, msgs); err != nil {
		tb.Fatal(err)
	}
	if err := st.SaveReported(tb.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		tb.Fatal(err)
	}
	owners, err := st.Owners(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	var verdicts []store.VerdictRow
	if err := st.EachMessage(tb.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch, Revision: r.Revision})
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	if err := st.WriteVerdicts(tb.Context(), owners[perfKey].Identity.Digest(), verdicts); err != nil {
		tb.Fatal(err)
	}
	folders, err := st.ListFolders(tb.Context(), "src")
	if err != nil {
		tb.Fatal(err)
	}
	cursors := map[int64]string{}
	for _, f := range folders {
		cursors[f.ID] = "cursor"
	}
	if err := st.SetCursors(tb.Context(), cursors); err != nil {
		tb.Fatal(err)
	}

	emb := &perfEmbedder{}
	ix := index.New(cfg, st, nil, perfSource{e: emb}, nil)
	ev, err := evidence.New(evidence.Deps{Config: cfg, Store: st, Space: func() (string, error) { return emb.Space(), nil }})
	if err != nil {
		tb.Fatal(err)
	}
	w := &fullWorld{cfg: cfg, st: st, ev: ev, ix: ix}
	if err := st.EachMessage(tb.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		w.ids = append(w.ids, r.RowID)
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	// The real index writes the vectors (space, text version and revision
	// the stage checks) and leaves the embeddings stage done.
	if rep, err := ix.Run(tb.Context(), index.Options{}, nil); err != nil || rep.After.Indexed != n {
		tb.Fatalf("index run: %+v %v", rep, err)
	}
	if _, err := ev.Run(tb.Context(), nil); err != nil {
		tb.Fatal(err)
	}
	prof, err := taxonomy.Load(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	plan, err := layer1.NewPlan(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	key := store.TriageKey{Fingerprint: prof.Fingerprint, Decider: plan.DeciderKey}
	var ds []store.Decision
	for i, id := range w.ids {
		probs := map[string]float64{"action": 0.02, "finance": 0.02, "marketing": 0.02, "notifications": 0.02, "security": 0.02, "spam": 0.02, "work": 0.02}
		best := []string{"finance", "work"}[i%2]
		probs[best] = 0.88
		a := map[string]engine.Answer{"category": {Type: engine.Choice, Choice: best, Probabilities: probs}}
		raw, _ := json.Marshal(a)
		ds = append(ds, store.Decision{MessageID: id, Answers: string(raw)})
		if i%3 == 0 {
			if err := st.PutLabel(tb.Context(), id, "category", fmt.Sprintf("%q", best), "judge"); err != nil {
				tb.Fatal(err)
			}
		}
	}
	if err := st.SaveDecisions(tb.Context(), key, ds); err != nil {
		tb.Fatal(err)
	}
	run, err := layer1.New(layer1.Deps{Config: cfg, Store: st, Evidence: ev, Embedder: perfSource{e: emb}})
	if err != nil {
		tb.Fatal(err)
	}
	if rep, err := run.Run(tb.Context(), nil); err != nil {
		tb.Fatalf("layer1 run: %v %+v", err, rep)
	}
	w.run = run
	w.clk = clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))

	syncStage := syncer.SyncStage(cfg, st)
	w.stages = []learning.Stage{
		syncer.MailboxesStage(cfg, st),
		learning.MachineStage(cfg, w.clk),
		syncStage,
		ev.Stage(),
		ix.Stage(),
		run.LabelsStage(syncStage),
		run.Stage(ev.Stage(), ix.Stage()),
		run.RouteStage(run.Stage(ev.Stage(), ix.Stage())),
		run.QuestionsStage(),
	}
	return w
}

func (w *fullWorld) status(tb testing.TB, stages ...learning.Stage) learning.Status {
	tb.Helper()
	m, err := learning.New(func() string { return perfKey }, w.st, nil, w.clk, stages...)
	if err != nil {
		tb.Fatal(err)
	}
	st, err := m.Status(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	return st
}

// freshStages is one build's stage fan-out with new instances: the same
// constructors wireLearning calls, but no stage-result cache of this round.
// The caches a stage owner already had (the index coverage, the evidence
// fingerprint, the layer-1 population) stay as they were before this round,
// so this is the pre-cache behavior the benchmarks compare against.
func (w *fullWorld) freshStages() []learning.Stage {
	syncStage := syncer.SyncStage(w.cfg, w.st)
	return []learning.Stage{
		syncer.MailboxesStage(w.cfg, w.st),
		learning.MachineStage(w.cfg, w.clk),
		syncStage,
		w.ev.Stage(),
		w.ix.Stage(),
		w.run.LabelsStage(syncStage),
		w.run.Stage(w.ev.Stage(), w.ix.Stage()),
		w.run.RouteStage(w.run.Stage(w.ev.Stage(), w.ix.Stage())),
		w.run.QuestionsStage(),
	}
}

// TestStatusJSONGoldenParity pins the whole status the UI reads: stage ids
// and order, states, progress (with units), blockers and their actions,
// notes, certifications, scope and instant. The golden was captured from
// the uncached fan-out (fresh stage instances, the pre-cache behavior); the
// cached fan-out must serialize byte-for-byte the same.
func TestStatusJSONGoldenParity(t *testing.T) {
	w := newFullWorld(t, 30)
	fresh := w.status(t, w.freshStages()...)
	reused := w.status(t, w.stages...)
	again := w.status(t, w.stages...)
	want := golden(t, fresh)
	for name, st := range map[string]learning.Status{"first cached build": reused, "repeat cached build": again} {
		got, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s differs from the captured status:\n%s", name, string(got))
		}
	}

	// The gate answers from the same status: the cached identity must not
	// open or close it differently. The captured world has blockers, so the
	// gate is closed and its locked status is the golden status.
	m, err := learning.New(func() string { return perfKey }, w.st, nil, w.clk, w.stages...)
	if err != nil {
		t.Fatal(err)
	}
	open, err := m.Open(t.Context())
	if err != nil || open {
		t.Fatalf("gate open=%v err=%v, want closed", open, err)
	}
	var locked *learning.LockedError
	if err := m.Allow(t.Context()); !errors.As(err, &locked) {
		t.Fatalf("gate allowed a blocked world: %v", err)
	}
	lockedJSON, err := json.MarshalIndent(locked.Status, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(lockedJSON) != want {
		t.Fatalf("the locked gate carries another status:\n%s", string(lockedJSON))
	}
}

func golden(t *testing.T, st learning.Status) string {
	t.Helper()
	path := filepath.Join("testdata", "status.json")
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the captured status: %v", err)
	}
	return strings.TrimSuffix(string(want), "\n")
}

// BenchmarkFullStatusBuild measures the coalesced progress refresh: one
// full status build over an unchanged mailbox. "fresh" asks every stage on
// a new instance (the pre-cache behavior); "reused" keeps the stages, so
// their owner keys skip work nothing changed.
func BenchmarkFullStatusBuild(b *testing.B) {
	for _, n := range []int{30, 300} {
		w := newFullWorld(b, n)
		b.Run(fmt.Sprintf("fresh/messages=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w.status(b, w.freshStages()...)
			}
		})
		w.status(b, w.stages...) // warm the owner caches
		b.Run(fmt.Sprintf("reused/messages=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w.status(b, w.stages...)
			}
		})
	}
}

// BenchmarkFullStageStatus measures the stages of one full fan-out after
// their owners' caches are warm: it names what a progress rebuild still
// computes. The mailbox, sync and embeddings stages are the ones this round
// owns; the layer-1 stages are measured as they stand.
func BenchmarkFullStageStatus(b *testing.B) {
	w := newFullWorld(b, 300)
	w.status(b, w.stages...) // warm every owner cache
	for i, stage := range w.stages {
		b.Run(fmt.Sprintf("%02d-%s", i, stage.ID()), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := stage.Status(b.Context()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
