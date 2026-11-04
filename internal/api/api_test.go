package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

type world struct {
	bus           *events.Bus
	services      Services
	status        *fakeStatus
	learning      *fakeLearning
	jobs          *fakeJobs
	mailboxes     *fakeMailboxes
	runs          *fakeRuns
	triage        *fakeTriage
	search        *fakeSearch
	settings      *fakeSettings
	secrets       *fakeSecrets
	providers     *fakeProviders
	sidecar       *fakeSidecar
	logs          *fakeLogs
	labels        *fakeLabels
	usage         *fakeUsage
	sources       *fakeSources
	hub           *fakeHub
	messages      *fakeMessages
	threads       *fakeThreads
	conversations *fakeConversations
	commitments   *fakeCommitments
	brief         *fakeBrief
	priority      *fakePriority
	agenda        *fakeAgenda
	ask           *fakeAsk
	companies     *fakeCompanies
	held          *fakeHeldRemovals
	shutdown      *fakeShutdown
	boot          *fakeBoot
}

func newWorld() *world {
	info := func(id, kind string, status runstate.State) jobs.Info {
		return jobs.Info{ID: id, Kind: kind, Name: kind, Status: status, StartedAt: time.Unix(1700000000, 0).UTC()}
	}
	setting := func(key string, secret bool, value any) SettingView {
		st := &config.Setting{Key: key, Type: config.TypeString, Default: json.RawMessage(`""`), Description: "test setting",
			Group: "Test", Secret: secret}
		return SettingView{Setting: st, Value: value, Layer: config.LayerFile}
	}
	settings := map[string]SettingView{
		"time.zone":          setting("time.zone", false, "UTC"),
		"http.proxy_url":     setting("http.proxy_url", true, config.SecretMask),
		"mailboxes.selected": setting("mailboxes.selected", false, []string{"store-1"}),
	}
	w := &world{
		bus: events.New(),
		status: &fakeStatus{view: StatusView{DataDir: "/tmp/mailsense", StorePath: "/tmp/mailsense/mailsense.db",
			StoreMeta: map[string]string{"created_version": "test"}, Sources: []SourceInfo{{ID: "outlook", Kind: "outlook"}}}},
		learning: &fakeLearning{status: learning.Status{State: learning.Learned, Stages: []learning.StageStatus{}}, open: true},
		jobs:     &fakeJobs{list: []jobs.Info{info("job-1", "index", runstate.Succeeded), info("job-2", "sync", runstate.Running)}},
		held: &fakeHeldRemovals{
			folders: []HeldFolderView{{SourceID: "outlook", HeldFolder: store.HeldFolder{
				FolderID: 7, StoreID: "store", Path: "Inbox", Held: 3, Stored: 100,
				Since: "2026-11-01T00:00:00Z", AppliesAt: "2026-11-06T00:00:00Z"}}},
			removal: store.Removal{Deleted: 3},
		},
		mailboxes: &fakeMailboxes{
			list:  []store.MailboxView{sourceMailbox("store-1")},
			boxes: []store.MailboxView{sourceMailbox("store-1")},
		},
		runs: &fakeRuns{syncInfo: info("job-sync", "sync", runstate.Running), evidenceInfo: info("job-evidence", "evidence", runstate.Running),
			indexInfo: info("job-index", "index", runstate.Running)},
		triage: &fakeTriage{page: &triage.Page{Results: map[int64]triage.PageRow{}, Limit: triage.TriagePageDefault},
			summary: &triage.Summary{}, result: triage.Result{MessageID: 1}},
		search: &fakeSearch{result: search.Result{}},
		settings: &fakeSettings{views: settings, order: []string{"http.proxy_url", "mailboxes.selected", "time.zone"},
			pending: []string{"log.level"}, secret: map[string]bool{"http.proxy_url": true},
			learned: []config.Learned{{Key: "time.zone", Value: "UTC", Sample: 1}}},
		secrets: &fakeSecrets{view: SecretsView{Kind: "dpapi", ReadOnly: false, Names: []string{"DEMO_API_KEY"}, Expected: []string{"DEMO_API_KEY"}}},
		providers: &fakeProviders{list: []ProviderInfo{{ID: "dec", Protocol: "systemone", Kind: engine.KindDecider}},
			discoverModels: []engine.ProviderModel{{ID: "provider.example.test/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash Standard",
				Type: "chat", ContextTokens: 1048576, MaxOutputTokens: 393216, Path: "/chat/completions",
				Endpoints: []string{"chat/completions"}}}},
		sidecar: &fakeSidecar{status: SidecarStatus{}, installInfo: info("job-install", "sidecar.install", runstate.Running)},
		logs: &fakeLogs{entries: []logx.Entry{{Seq: 1, Level: "INFO", Component: "test", Message: "started"},
			{Seq: 2, Level: "INFO", Component: "test", Message: "working"}}},
		labels: &fakeLabels{
			queue:     layer1.LabelQueue{Question: "category", Strategy: layer1.LabelStrategyMissing, Tasks: []layer1.LabelTask{{MessageID: 1, Question: "category"}}},
			put:       feedback.PutResult{Stored: []feedback.StoredLabel{{MessageID: 1, Question: "category", Source: "manual"}}},
			del:       feedback.PutResult{Stored: []feedback.StoredLabel{{MessageID: 1, Question: "category", Source: "manual"}}},
			startInfo: info("job-layer1", "layer1", runstate.Running), judgeInfo: info("job-judge", "labels.judge", runstate.Running),
		},
		usage: &fakeUsage{rows: []store.UsageRow{
			{Day: "2026-11-04", Provider: "decider", Model: "deepseek", Calls: 3, Failures: 1, InputTokens: 30, OutputTokens: 9, CostUSD: 0.25},
		}},
		sources: &fakeSources{
			start:  SourceActionView{Action: "start", Source: "outlook"},
			resume: SourceActionView{Action: "resume", Source: "outlook"},
			retry:  SourceActionView{Action: "retry-details", Source: "outlook", Retried: 2},
		},
		messages: &fakeMessages{view: MessageView{RowID: 1, Revision: 1, Message: &MessageWire{ID: "m1", Subject: "synthetic"}}},
		threads:  &fakeThreads{view: ThreadView{ConversationID: "C1"}},
		conversations: &fakeConversations{page: conversations.Page{
			Conversations: []conversations.Row{},
			Counts:        map[string]int{},
		}},
		commitments: &fakeCommitments{res: commitments.Result{
			Items:  []commitments.Commitment{},
			Counts: map[string]int{},
		}},
		brief:    &fakeBrief{res: brief.Result{}},
		priority: &fakePriority{items: []priority.Item{}, preview: false},
		agenda:   &fakeAgenda{res: agenda.Result{Items: []agenda.Item{}}},
		ask:      &fakeAsk{view: qa.AskView{ID: 1, Mode: qa.ModeStructured, Intent: qa.IntentPriority, Counts: map[string]int{}, Citations: []qa.Citation{}}, history: []qa.HistoryRow{}},
		companies: &fakeCompanies{list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{}},
			profile: worldsvc.ProfileView{Key: "c:atelier nord", Label: "Atelier Nord"}},
		hub: &fakeHub{
			hub: hub.Hub{
				Gate:    hub.Gate{Open: false, State: learning.Stuck, Labels: hub.LabelTotals{Manual: 1, Judge: 2, Needed: 3, Review: 2}},
				Mailbox: hub.Mailbox{Messages: 4, Stores: 1},
				Recent:  []hub.Answer{{ID: 9, MessageID: 1, Question: "category", Source: store.LabelJudge, Value: json.RawMessage(`"work"`), At: "2026-10-26T09:00:00Z"}},
			},
			answers: hub.AnswerPage{Items: []hub.Answer{{ID: 9, MessageID: 1, Question: "category", Source: store.LabelManual,
				Value: json.RawMessage(`"work"`), At: "2026-10-26T09:00:00Z"}}, NextCursor: 9},
			summary: hub.LabelSummary{LabelTotals: hub.LabelTotals{Manual: 1, Judge: 2, Needed: 3, Review: 2},
				Questions: []hub.Question{{Question: "category", Manual: 1, Judge: 2, Needed: 3, Review: 2}}},
		},
		shutdown: &fakeShutdown{},
		boot: &fakeBoot{views: []BootView{{State: "warming", Total: 1,
			Views: []BootStageView{{Name: "learning", State: "pending"}}}}},
	}
	w.services = Services{
		Bus: w.bus, Status: w.status, Learning: w.learning, Jobs: w.jobs, HeldRemovals: w.held, Mailboxes: w.mailboxes, Runs: w.runs,
		Triage: w.triage, Search: w.search, Settings: w.settings, Secrets: w.secrets, Providers: w.providers,
		Sidecar: w.sidecar, Logs: w.logs, Labels: w.labels, Usage: w.usage, Sources: w.sources, Hub: w.hub, Messages: w.messages,
		Threads: w.threads, Conversations: w.conversations, Commitments: w.commitments, Brief: w.brief,
		Priority: w.priority, Agenda: w.agenda, Ask: w.ask,
		Companies: w.companies, Shutdown: w.shutdown, Boot: w.boot,
	}
	return w
}

func sourceMailbox(key string) store.MailboxView {
	return store.MailboxView{Mailbox: source.Mailbox{Key: key, SourceID: "outlook", StoreID: "store", Name: "Mailbox", Folders: 1, Items: 2}}
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		DescriptorPath:  filepath.Join(t.TempDir(), "runtime", "api.json"),
		MaxBytes:        1024,
		RequestTimeout:  2 * time.Second,
		LongTimeout:     2 * time.Second,
		SSEHeartbeat:    20 * time.Millisecond,
		SSEWriteTimeout: time.Second,
		SSEBuffer:       4,
		SSEMaxClients:   8,
		EventsBuffer:    8,
		ShutdownTimeout: 2 * time.Second,
	}
}

func newTestServer(t *testing.T) (*Server, *httptest.Server, *world) {
	t.Helper()
	return newTestServerWith(t, testOptions(t), newWorld())
}

func newTestServerWith(t *testing.T, opts Options, w *world) (*Server, *httptest.Server, *world) {
	t.Helper()
	srv, err := New(w.services, opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(ctx)
		ts.Close()
		w.bus.Close()
	})
	return srv, ts, w
}

var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type request struct {
	method   string
	path     string
	body     string
	skipAuth bool
	headers  map[string]string
	host     string
}

func do(t *testing.T, ts *httptest.Server, srv *Server, in request) (int, http.Header, []byte) {
	t.Helper()
	var body io.Reader
	if in.body != "" {
		body = strings.NewReader(in.body)
	}
	req, err := http.NewRequest(in.method, ts.URL+in.path, body)
	if err != nil {
		t.Fatal(err)
	}
	if in.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if in.host != "" {
		req.Host = in.host
	}
	for k, v := range in.headers {
		req.Header.Set(k, v)
	}
	if !in.skipAuth {
		req.Header.Set("Authorization", "Bearer "+srv.token)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, data
}

func noAuth(in request) request { in.skipAuth = true; return in }

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, data)
	}
	return out
}

func errorCode(t *testing.T, data []byte) string {
	t.Helper()
	body := decodeMap(t, data)
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func mustStatus(t *testing.T, got, want int, data []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status %d, want %d: %s", got, want, data)
	}
}

func streamRequest(t *testing.T, ts *httptest.Server, srv *Server, path string, auth bool, ctx context.Context) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+srv.token)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return resp, bufio.NewReader(resp.Body), cancel
}

func endpointURL(t *testing.T, ts *httptest.Server) *url.URL {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
