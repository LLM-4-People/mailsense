package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/engine"
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
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/update"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

type fakeStatus struct {
	view  StatusView
	err   error
	calls int
}

func (f *fakeStatus) Status(context.Context) (StatusView, error) {
	f.calls++
	return f.view, f.err
}

type fakeLearning struct {
	allowErr    error
	status      learning.Status
	open        bool
	preview     bool
	statusErr   error
	zoneErr     error
	zone        string
	calls       int
	statusCalls int
}

func (f *fakeLearning) Allow(context.Context) error {
	f.calls++
	return f.allowErr
}

func (f *fakeLearning) GateState(context.Context) learning.GateState {
	f.calls++
	return f.gateState()
}

// gateState models the production derivations: Allow opens under the test
// preview, Open is the real gate, and Preview is the setting on a closed gate.
func (f *fakeLearning) gateState() learning.GateState {
	looked := learning.GateState{Preview: f.preview && !f.open, Open: f.open, Allow: f.allowErr}
	if f.preview {
		looked.Allow = nil
	}
	return looked
}

func (f *fakeLearning) Status(context.Context) (learning.Status, learning.GateState, error) {
	f.statusCalls++
	f.calls++
	return f.status, f.gateState(), f.statusErr
}

func (f *fakeLearning) ConfirmZone(_ context.Context, zone string) error {
	f.zone = zone
	return f.zoneErr
}

type fakeHeldRemovals struct {
	folders   []HeldFolderView
	removal   store.Removal
	err       error
	source    string
	confirmed int64
}

func (f *fakeHeldRemovals) Held(_ context.Context, sourceID string) ([]HeldFolderView, error) {
	f.source = sourceID
	return f.folders, f.err
}

func (f *fakeHeldRemovals) Confirm(_ context.Context, folderID int64) (store.Removal, error) {
	f.confirmed = folderID
	return f.removal, f.err
}

type fakeJobs struct {
	mu    sync.Mutex
	list  []jobs.Info
	calls int
}

func (f *fakeJobs) List() []jobs.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]jobs.Info(nil), f.list...)
}

func (f *fakeJobs) Get(id string) (jobs.Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	for _, info := range f.list {
		if info.ID == id {
			return info, true
		}
	}
	return jobs.Info{}, false
}

func (f *fakeJobs) Cancel(id string) (jobs.Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	for i := range f.list {
		if f.list[i].ID == id {
			f.list[i].Status = runstate.Cancelled
			return f.list[i], true
		}
	}
	return jobs.Info{}, false
}

type fakeMailboxes struct {
	list     []store.MailboxView
	boxes    []store.MailboxView
	err      error
	calls    int
	discover int
	pick     int
}

func (f *fakeMailboxes) List(context.Context) ([]store.MailboxView, error) {
	f.calls++
	return append([]store.MailboxView(nil), f.list...), f.err
}

func (f *fakeMailboxes) Discover(context.Context, string) ([]store.MailboxView, error) {
	f.discover++
	return append([]store.MailboxView(nil), f.boxes...), f.err
}

func (f *fakeMailboxes) Pick(context.Context, []string) ([]store.MailboxView, error) {
	f.pick++
	return append([]store.MailboxView(nil), f.boxes...), f.err
}

func (f *fakeMailboxes) Unpick(context.Context, []string) ([]store.MailboxView, error) {
	f.pick++
	return append([]store.MailboxView(nil), f.boxes...), f.err
}

func (f *fakeMailboxes) Declare(context.Context, string, string) ([]store.MailboxView, error) {
	f.pick++
	return append([]store.MailboxView(nil), f.boxes...), f.err
}

func (f *fakeMailboxes) Undeclare(context.Context, string, string) ([]store.MailboxView, error) {
	f.pick++
	return append([]store.MailboxView(nil), f.boxes...), f.err
}

type fakeRuns struct {
	syncInfo, evidenceInfo, indexInfo jobs.Info
	syncErr, evidenceErr, indexErr    error
	syncCalls                         int
	evidenceCalls                     int
	indexCalls                        int
}

func (f *fakeRuns) StartSync(context.Context, string) (jobs.Info, error) {
	f.syncCalls++
	return f.syncInfo, f.syncErr
}

func (f *fakeRuns) StartEvidence(context.Context) (jobs.Info, error) {
	f.evidenceCalls++
	return f.evidenceInfo, f.evidenceErr
}

func (f *fakeRuns) StartIndex(context.Context, []string) (jobs.Info, error) {
	f.indexCalls++
	return f.indexInfo, f.indexErr
}

type fakeTriage struct {
	page          *triage.Page
	summary       *triage.Summary
	result        triage.Result
	questions     QuestionsView
	pageErr       error
	summaryErr    error
	resultErr     error
	questionErr   error
	readCalls     int
	summaryCalls  int
	resultCall    int
	questionCalls int
	lastPage      triage.PageQuery
}

func (f *fakeTriage) Page(_ context.Context, q triage.PageQuery) (*triage.Page, error) {
	f.readCalls++
	f.lastPage = q
	return f.page, f.pageErr
}

func (f *fakeTriage) Summary(context.Context) (*triage.Summary, error) {
	f.summaryCalls++
	return f.summary, f.summaryErr
}

func (f *fakeTriage) Result(context.Context, int64) (triage.Result, error) {
	f.resultCall++
	return f.result, f.resultErr
}

func (f *fakeTriage) Questions(context.Context) (QuestionsView, error) {
	f.questionCalls++
	return f.questions, f.questionErr
}

type fakeSearch struct {
	result search.Result
	err    error
	calls  int
	query  search.Query
}

func (f *fakeSearch) Search(_ context.Context, q search.Query) (search.Result, error) {
	f.calls++
	f.query = q
	return f.result, f.err
}

type fakeSettings struct {
	mu          sync.Mutex
	views       map[string]SettingView
	order       []string
	pending     []string
	learned     []config.Learned
	setErr      error
	resetErr    error
	reloadErr   error
	rejectErr   error
	unrejectErr error
	secret      map[string]bool
	listCalls   int
	setCalls    int
}

func (f *fakeSettings) List() ([]SettingView, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	out := make([]SettingView, 0, len(f.order))
	for _, key := range f.order {
		out = append(out, f.views[key])
	}
	return out, append([]string(nil), f.pending...)
}

func (f *fakeSettings) Get(key string) (SettingView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.views[key]
	return v, ok
}

func (f *fakeSettings) Set(key string, raw json.RawMessage) (SettingView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if f.setErr != nil {
		return SettingView{}, f.setErr
	}
	v, ok := f.views[key]
	if !ok {
		return SettingView{}, &APIError{Status: 404, Code: "unknown_setting", Message: "unknown setting"}
	}
	v.Value = string(raw)
	f.views[key] = v
	return v, nil
}

func (f *fakeSettings) Reset(key string) (SettingView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resetErr != nil {
		return SettingView{}, f.resetErr
	}
	v, ok := f.views[key]
	if !ok {
		return SettingView{}, &APIError{Status: 404, Code: "unknown_setting", Message: "unknown setting"}
	}
	return v, nil
}

func (f *fakeSettings) Reload() ([]string, error) {
	if f.reloadErr != nil {
		return nil, f.reloadErr
	}
	return append([]string(nil), f.pending...), nil
}

func (f *fakeSettings) Learned() []config.Learned {
	return append([]config.Learned(nil), f.learned...)
}

func (f *fakeSettings) Reject(string, json.RawMessage) ([]config.Learned, error) {
	if f.rejectErr != nil {
		return nil, f.rejectErr
	}
	return append([]config.Learned(nil), f.learned...), nil
}

func (f *fakeSettings) Unreject(string, json.RawMessage) ([]config.Learned, error) {
	if f.unrejectErr != nil {
		return nil, f.unrejectErr
	}
	return append([]config.Learned(nil), f.learned...), nil
}

func (f *fakeSettings) Secret(key string) bool { return f.secret[key] }

type fakeSecrets struct {
	view      SecretsView
	listErr   error
	setErr    error
	deleteErr error
	setHook   func(name, value string)
	setCalls  int
}

func (f *fakeSecrets) List() (SecretsView, error) { return f.view, f.listErr }
func (f *fakeSecrets) Set(name, value string) error {
	f.setCalls++
	if f.setHook != nil {
		f.setHook(name, value)
	}
	return f.setErr
}
func (f *fakeSecrets) Delete(string) error { return f.deleteErr }

type fakeProviders struct {
	list           []ProviderInfo
	roles          []ProviderRoleInfo
	listErr        error
	testResult     engine.ProviderTestResult
	testErr        error
	testCalls      int
	testID         string
	createInfo     ProviderInfo
	createErr      error
	createCalls    int
	created        engine.Provider
	updateInfo     ProviderInfo
	updateErr      error
	updateID       string
	updated        engine.Provider
	deleteErr      error
	deleteID       string
	deleteCalls    int
	discoverModels []engine.ProviderModel
	discoverErr    error
	discoverReq    engine.DiscoverRequest
	discoverN      int
}

func (f *fakeProviders) List(context.Context) ([]ProviderInfo, error)      { return f.list, f.listErr }
func (f *fakeProviders) Roles(context.Context) ([]ProviderRoleInfo, error) { return f.roles, nil }
func (f *fakeProviders) Create(_ context.Context, rec engine.Provider) (ProviderInfo, error) {
	f.createCalls++
	f.created = rec
	return f.createInfo, f.createErr
}
func (f *fakeProviders) Update(_ context.Context, id string, rec engine.Provider) (ProviderInfo, error) {
	f.updateID, f.updated = id, rec
	return f.updateInfo, f.updateErr
}
func (f *fakeProviders) Delete(_ context.Context, id string) error {
	f.deleteCalls++
	f.deleteID = id
	return f.deleteErr
}
func (f *fakeProviders) Test(_ context.Context, id string) (engine.ProviderTestResult, error) {
	f.testCalls++
	f.testID = id
	return f.testResult, f.testErr
}
func (f *fakeProviders) Discover(_ context.Context, req engine.DiscoverRequest) ([]engine.ProviderModel, error) {
	f.discoverN++
	f.discoverReq = req
	return f.discoverModels, f.discoverErr
}

type fakeSidecar struct {
	status       SidecarStatus
	statusErr    error
	installInfo  jobs.Info
	installErr   error
	verifyErr    error
	installCalls int
	verifyCalls  int
}

func (f *fakeSidecar) Status() (SidecarStatus, error) { return f.status, f.statusErr }
func (f *fakeSidecar) Install(context.Context, string, string, string) (jobs.Info, error) {
	f.installCalls++
	return f.installInfo, f.installErr
}
func (f *fakeSidecar) Verify(context.Context, string, string, string) error {
	f.verifyCalls++
	return f.verifyErr
}

type fakeLogs struct {
	mu        sync.Mutex
	entries   []logx.Entry
	health    logx.Health
	firstRead func()
	readCalls int
	lastQuery LogQuery
}

func (f *fakeLogs) Append(entries ...logx.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entries...)
}

// Reset replaces the retained entries, the way a finite ring rotates.
func (f *fakeLogs) Reset(entries ...logx.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append([]logx.Entry(nil), entries...)
}

func (f *fakeLogs) Read(seq uint64) ([]logx.Entry, uint64, uint64) {
	return f.ReadLimit(seq, len(f.entries), false)
}

func (f *fakeLogs) ReadLimit(seq uint64, limit int, newest bool) ([]logx.Entry, uint64, uint64) {
	f.mu.Lock()
	hook := f.firstRead
	if f.readCalls == 0 && hook != nil {
		f.firstRead = nil
		f.mu.Unlock()
		hook()
		f.mu.Lock()
	}
	f.readCalls++
	defer f.mu.Unlock()
	first := len(f.entries)
	for i, e := range f.entries {
		if e.Seq > seq {
			first = i
			break
		}
	}
	if len(f.entries) == 0 {
		return nil, 0, 0
	}
	oldest, last := f.entries[0].Seq, f.entries[len(f.entries)-1].Seq
	if first == len(f.entries) || limit < 1 {
		return nil, oldest, last
	}
	count := len(f.entries) - first
	if count > limit {
		if newest {
			first += count - limit
		}
		count = limit
	}
	out := make([]logx.Entry, count)
	copy(out, f.entries[first:first+count])
	return out, oldest, last
}

func (f *fakeLogs) Bounds() (uint64, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) == 0 {
		return 0, 0
	}
	return f.entries[0].Seq, f.entries[len(f.entries)-1].Seq
}

func (f *fakeLogs) Snapshot(q LogQuery) (LogSnapshot, error) {
	f.lastQuery = q
	entries, oldest, last := f.ReadLimit(q.Since, q.Limit, !q.HasSince)
	base := q.Since
	if !q.HasSince || q.Since < oldest {
		base = oldest - 1
	}
	truncated := last > 0 && last > base && last-base > uint64(q.Limit)
	return LogSnapshot{Entries: LogEntryViews(entries), Health: f.health, OldestSeq: oldest, LastSeq: last, Truncated: truncated}, nil
}

type fakeLabels struct {
	queue          layer1.LabelQueue
	queueErr       error
	put            feedback.PutResult
	putErr         error
	del            feedback.PutResult
	delErr         error
	startInfo      jobs.Info
	startErr       error
	judgeInfo      jobs.Info
	judgeErr       error
	lastQuery      layer1.LabelQuery
	lastContains   layer1.LabelQuery
	lastContainsID int64
	lastDeleteID   int64
	lastDeleteQ    string
	queueCalls     int
	containsCalls  int
	putCalls       int
	deleteCalls    int
	startCalls     int
	judgeCalls     int
}

func (f *fakeLabels) Start(context.Context) (jobs.Info, error) {
	f.startCalls++
	return f.startInfo, f.startErr
}

func (f *fakeLabels) StartJudge(context.Context, layer1.JudgeOptions) (jobs.Info, error) {
	f.judgeCalls++
	return f.judgeInfo, f.judgeErr
}

func (f *fakeLabels) LabelQueue(_ context.Context, q layer1.LabelQuery) (layer1.LabelQueue, error) {
	f.queueCalls++
	f.lastQuery = q
	return f.queue, f.queueErr
}

func (f *fakeLabels) LabelQueueContains(_ context.Context, q layer1.LabelQuery, messageID int64) (bool, error) {
	f.containsCalls++
	f.lastContains = q
	f.lastContainsID = messageID
	for _, task := range f.queue.Tasks {
		if task.MessageID == messageID && task.Question == q.Question {
			return true, f.queueErr
		}
	}
	return false, f.queueErr
}

func (f *fakeLabels) PutLabels(context.Context, []feedback.PutItem) (feedback.PutResult, error) {
	f.putCalls++
	return f.put, f.putErr
}

func (f *fakeLabels) DeleteLabel(_ context.Context, messageID int64, question string) (feedback.PutResult, error) {
	f.deleteCalls++
	f.lastDeleteID, f.lastDeleteQ = messageID, question
	return f.del, f.delErr
}

type fakeUsage struct {
	rows     []store.UsageRow
	err      error
	lastDays int
	calls    int
}

func (f *fakeUsage) List(_ context.Context, days int) ([]store.UsageRow, error) {
	f.calls++
	f.lastDays = days
	return f.rows, f.err
}

type fakeSources struct {
	start, resume, retry                SourceActionView
	startErr, resumeErr, retryErr       error
	startCalls, resumeCalls, retryCalls int
	lastID                              string
}

func (f *fakeSources) Start(_ context.Context, sourceID string) (SourceActionView, error) {
	f.startCalls++
	f.lastID = sourceID
	return f.start, f.startErr
}

func (f *fakeSources) Resume(_ context.Context, sourceID string) (SourceActionView, error) {
	f.resumeCalls++
	f.lastID = sourceID
	return f.resume, f.resumeErr
}

func (f *fakeSources) RetryDetails(_ context.Context, sourceID string) (SourceActionView, error) {
	f.retryCalls++
	f.lastID = sourceID
	return f.retry, f.retryErr
}

type fakeHub struct {
	hub         hub.Hub
	answers     hub.AnswerPage
	summary     hub.LabelSummary
	hubErr      error
	answersErr  error
	summaryErr  error
	hubCalls    int
	answerCalls int
	summaryCall int
	lastQuery   hub.AnswerQuery
}

func (f *fakeHub) Hub(context.Context) (hub.Hub, error) {
	f.hubCalls++
	return f.hub, f.hubErr
}

func (f *fakeHub) Answers(_ context.Context, q hub.AnswerQuery) (hub.AnswerPage, error) {
	f.answerCalls++
	f.lastQuery = q
	return f.answers, f.answersErr
}

func (f *fakeHub) LabelSummary(context.Context) (hub.LabelSummary, error) {
	f.summaryCall++
	return f.summary, f.summaryErr
}

type fakeMessages struct {
	view           MessageView
	list           MessageListView
	err            error
	calls          int
	listCalls      int
	lastList       store.MessageListQuery
	backfillInfo   jobs.Info
	backfillErr    error
	backfillCalls  int
	backfillSource string
	asset          AttachmentAsset
	assetErr       error
	assetCalls     int
	lastAttachment int
}

func (f *fakeMessages) List(_ context.Context, q store.MessageListQuery) (MessageListView, error) {
	f.listCalls++
	f.lastList = q
	return f.list, f.err
}

func (f *fakeMessages) Get(context.Context, int64) (MessageView, error) {
	f.calls++
	return f.view, f.err
}

func (f *fakeMessages) Attachment(_ context.Context, _ int64, position int) (AttachmentAsset, error) {
	f.assetCalls++
	f.lastAttachment = position
	return f.asset, f.assetErr
}

func (f *fakeMessages) StartBackfill(_ context.Context, sourceID string) (jobs.Info, error) {
	f.backfillCalls++
	f.backfillSource = sourceID
	return f.backfillInfo, f.backfillErr
}

type fakePriority struct {
	items      []priority.Item
	total      int
	preview    bool
	err        error
	calls      int
	lastFilter priority.Filter
	lastLimit  int
	lastOffset int
}

func (f *fakePriority) Rank(_ context.Context, filter priority.Filter, limit int) ([]priority.Item, int, bool, error) {
	f.calls++
	f.lastFilter, f.lastLimit = filter, limit
	return f.items, f.total, f.preview, f.err
}

func (f *fakePriority) RankPage(ctx context.Context, filter priority.Filter, limit, offset int) (priority.Page, error) {
	items, total, preview, err := f.Rank(ctx, filter, limit)
	f.lastOffset = offset
	available := max(total, offset+len(items))
	counts := make(map[string]int, len(priority.Filters))
	for _, name := range priority.Filters {
		counts[string(name)] = 0
	}
	counts[string(filter)] = total
	return priority.Page{Items: items, Total: total, Available: available, Offset: offset,
		HasMore: offset+len(items) < available, Preview: preview, Counts: counts,
		GeneratedAt: time.Unix(0, 0).UTC()}, err
}

type fakeAsk struct {
	view             qa.AskView
	history          []qa.HistoryRow
	historyItem      qa.AskView
	historyItemOK    bool
	err              error
	historyErr       error
	historyItemErr   error
	clearErr         error
	calls            int
	historyCall      int
	historyItemCalls int
	clearCalls       int
	question         string
	language         string
	conversationID   string
	route            string
	lastLimit        int
	lastHistoryID    int64
}

func (f *fakeAsk) Ask(ctx context.Context, question, language string) (qa.AskView, error) {
	return f.AskConversation(ctx, qa.AskRequest{Question: question, Language: language})
}

func (f *fakeAsk) AskConversation(_ context.Context, req qa.AskRequest) (qa.AskView, error) {
	f.calls++
	f.question, f.language, f.conversationID, f.route = req.Question, req.Language, req.ConversationID, string(req.Route)
	return f.view, f.err
}

func (f *fakeAsk) History(_ context.Context, limit int) ([]qa.HistoryRow, error) {
	f.historyCall++
	f.lastLimit = limit
	return f.history, f.historyErr
}

func (f *fakeAsk) HistoryItem(_ context.Context, id int64) (qa.AskView, bool, error) {
	f.historyItemCalls++
	f.lastHistoryID = id
	return f.historyItem, f.historyItemOK, f.historyItemErr
}

func (f *fakeAsk) ClearHistory(context.Context) error {
	f.clearCalls++
	return f.clearErr
}

// fakeUpdate is the release-detection surface: each method records its call,
// its last version and answers the status the test installed. Check never
// touches the network here; the update package tests own that.
type fakeUpdate struct {
	status      update.Status
	checkStatus update.Status
	skipStatus  update.Status
	seenStatus  update.Status
	err         error
	statusCalls int
	checkCalls  int
	skipCalls   int
	seenCalls   int
	lastVersion string
}

func (f *fakeUpdate) Status() update.Status {
	f.statusCalls++
	return f.status
}

func (f *fakeUpdate) Check(context.Context) (update.Status, error) {
	f.checkCalls++
	return f.checkStatus, f.err
}

func (f *fakeUpdate) Skip(version string) (update.Status, error) {
	f.skipCalls++
	f.lastVersion = version
	return f.skipStatus, f.err
}

func (f *fakeUpdate) MarkSeen(version string) (update.Status, error) {
	f.seenCalls++
	f.lastVersion = version
	return f.seenStatus, f.err
}

type fakeShutdown struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeShutdown) Request(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

type fakeAgenda struct {
	res      agenda.Result
	err      error
	horizons []time.Duration
}

func (f *fakeAgenda) Build(_ context.Context, horizon time.Duration) (agenda.Result, error) {
	f.horizons = append(f.horizons, horizon)
	return f.res, f.err
}

func (f *fakeAgenda) DefaultHorizon() time.Duration { return 3 * time.Hour }

type fakeCompanies struct {
	list          worldsvc.ListResult
	profile       worldsvc.ProfileView
	listErr       error
	profileErr    error
	listCalls     int
	profileCalls  int
	lastLimit     int
	lastQuery     worldsvc.ListQuery
	lastKey       string
	lastKeys      []string
	lastMonths    int
	lastEventDays int
}

func (f *fakeCompanies) List(_ context.Context, q worldsvc.ListQuery) (worldsvc.ListResult, error) {
	f.listCalls++
	f.lastLimit, f.lastQuery = q.Limit, q
	return f.list, f.listErr
}

func (f *fakeCompanies) Profile(_ context.Context, key string, months, eventDays int) (worldsvc.ProfileView, error) {
	f.profileCalls++
	f.lastKey, f.lastMonths, f.lastEventDays = key, months, eventDays
	return f.profile, f.profileErr
}

func (f *fakeCompanies) Profiles(_ context.Context, keys []string, months, eventDays int) (map[string]worldsvc.ProfileView, error) {
	if f.profileErr != nil {
		return nil, f.profileErr
	}
	out := make(map[string]worldsvc.ProfileView, len(keys))
	for _, key := range keys {
		f.profileCalls++
		out[key] = f.profile
	}
	f.lastKeys = append([]string{}, keys...)
	f.lastMonths, f.lastEventDays = months, eventDays
	return out, nil
}

type fakeThreads struct {
	view       ThreadView
	err        error
	calls      int
	lastID     string
	lastLimit  int
	lastBefore int64
	lastAt     int64
}

func (f *fakeThreads) Get(_ context.Context, conversationID string, limit int, beforeID, atID int64) (ThreadView, error) {
	f.calls++
	f.lastID, f.lastLimit, f.lastBefore, f.lastAt = conversationID, limit, beforeID, atID
	return f.view, f.err
}

type fakeConversations struct {
	page      conversations.Page
	err       error
	calls     int
	lastQuery conversations.Query
}

func (f *fakeConversations) Page(_ context.Context, q conversations.Query) (conversations.Page, error) {
	f.calls++
	f.lastQuery = q
	return f.page, f.err
}

type fakeCommitments struct {
	res        commitments.Result
	err        error
	calls      int
	lastFilter commitments.Filter
}

func (f *fakeCommitments) List(_ context.Context, filter commitments.Filter) (commitments.Result, error) {
	f.calls++
	f.lastFilter = filter
	return f.res, f.err
}

type fakeBrief struct {
	res       brief.Result
	err       error
	calls     int
	lastSince string
}

func (f *fakeBrief) Brief(_ context.Context, since string) (brief.Result, error) {
	f.calls++
	f.lastSince = since
	return f.res, f.err
}
