// Package hub is the pre-gate home and answer-history read side: it
// explains the learning gate, counts what the owner still has to review,
// and lists the current labels. It owns no write path and returns no mail
// content; corrections stay on POST /api/labels.
package hub

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// RecentAnswerCount is how many current labels the hub shows.
const RecentAnswerCount = 6

// BlockerLimit is how many learning blockers the hub mirror carries.
const BlockerLimit = 3

// Read-cache bounds. Each hub cache holds one generated screen value under a
// store generation; the byte budgets bound what one store answer can pin.
// The per-entry estimates are the Weigh basis: a label count is two short
// strings and an int, an answer row's raw label value is measured exactly.
const (
	hubLabelCountsMaxBytes = 256 << 10
	hubMailboxMaxBytes     = 4 << 10
	hubRecentMaxBytes      = 64 << 10

	hubLabelCountBytes       = 96
	hubLabelReviewEntryBytes = 64
	hubMailboxCountBytes     = 64
	hubAnswerPageBytes       = 48
	hubAnswerRowBytes        = 64
)

// storeReader is the hub's read side: the aggregates Home reads and the
// generation each one follows.
type storeReader interface {
	LabelGeneration() uint64
	MailGeneration() uint64
	LabelCounts(context.Context) ([]store.LabelCount, error)
	ReviewCounts(context.Context) (map[string]int, error)
	MailboxCounts(context.Context) (int, int, error)
	NewestReceivedAt(context.Context) (time.Time, error)
	AnswerRows(context.Context, store.AnswerFilter) ([]store.AnswerRow, bool, error)
}

type labelCountsValue struct {
	counts []store.LabelCount
	review map[string]int
}

type mailboxCountValue struct {
	messages int
	stores   int
	newest   time.Time
}

// Learning is the status and gate answer the hub reads: the same shape as
// the API's learning service.
type Learning interface {
	// Status reads the status and the gate in one reading, so a screen cannot
	// show a status from one state beside a gate from another.
	Status(context.Context) (learning.Status, learning.GateState, error)
}

// Sync is the sync owner's liveness: the last run outcomes. The hub never
// derives "synced" from a learning stage again; a stage record can survive
// mail that stopped arriving.
type Sync interface {
	Status() syncer.Status
}

// Deps are the read collaborators. Now is the wall clock (nil: time.Now).
type Deps struct {
	Store    *store.Store
	Learning Learning
	Sync     Sync
	Now      func() time.Time
}

// Service answers the hub and label-summary reads.
type Service struct {
	store    storeReader
	learning Learning
	sync     Sync
	now      func() time.Time

	labels  *viewcache.Cache[uint64, labelCountsValue]
	mailbox *viewcache.Cache[uint64, mailboxCountValue]
	recent  *viewcache.Cache[uint64, AnswerPage]
}

// weighLabelCounts estimates one label-count aggregate: a count row carries
// two short strings and an int, and the review map one entry per status.
func weighLabelCounts(v labelCountsValue) int {
	return len(v.counts)*hubLabelCountBytes + len(v.review)*hubLabelReviewEntryBytes
}

// weighMailboxCount estimates the mailbox scalar triple: no slices, no maps.
func weighMailboxCount(mailboxCountValue) int { return hubMailboxCountBytes }

// weighAnswerPage estimates one page: each answer row's fixed fields plus its
// strings, with the raw label value measured exactly.
func weighAnswerPage(p AnswerPage) int {
	n := hubAnswerPageBytes
	for _, a := range p.Items {
		n += hubAnswerRowBytes + len(a.Question) + len(a.Source) + len(a.At) + len(a.Value)
	}
	return n
}

// New builds the read service.
func New(d Deps) *Service {
	s := &Service{store: d.Store, learning: d.Learning, sync: d.Sync, now: d.Now}
	if s.now == nil {
		s.now = time.Now
	}
	s.labels = viewcache.New[uint64, labelCountsValue](viewcache.Options[uint64, labelCountsValue]{
		TTL: 10 * time.Second, Size: 2, MaxBytes: hubLabelCountsMaxBytes, Weigh: weighLabelCounts,
		Now: s.now, Component: "hub",
		Keep: func(gen uint64, _ labelCountsValue) bool { return gen == s.store.LabelGeneration() },
	})
	s.mailbox = viewcache.New[uint64, mailboxCountValue](viewcache.Options[uint64, mailboxCountValue]{
		TTL: 10 * time.Second, Size: 2, MaxBytes: hubMailboxMaxBytes, Weigh: weighMailboxCount,
		Now: s.now, Component: "hub",
		Keep: func(gen uint64, _ mailboxCountValue) bool { return gen == s.store.MailGeneration() },
	})
	s.recent = viewcache.New[uint64, AnswerPage](viewcache.Options[uint64, AnswerPage]{
		TTL: 10 * time.Second, Size: 2, MaxBytes: hubRecentMaxBytes, Weigh: weighAnswerPage,
		Now: s.now, Component: "hub",
		Keep: func(gen uint64, _ AnswerPage) bool { return gen == s.store.LabelGeneration() },
	})
	return s
}

// Close joins the background rebuilds of the three caches, bounded by ctx. It
// reports whether all of them joined: false means a store read outlived ctx,
// so the store owner keeps the store open. It is idempotent.
func (s *Service) Close(ctx context.Context) bool {
	joined := s.labels.Close(ctx)
	joined = s.mailbox.Close(ctx) && joined
	return s.recent.Close(ctx) && joined
}

// Hub is the pre-gate home view: the gate, its label questions and
// blockers, the mailbox counts and the newest manual answers. It never
// carries a subject, sender, body or snippet. Gemma's suggestions are
// history on the Answers screen, not this rail: the rail is the owner's
// own trail, not a second suggestion list.
type Hub struct {
	GeneratedAt time.Time `json:"generated_at"`
	Gate        Gate      `json:"gate"`
	Mailbox     Mailbox   `json:"mailbox"`
	Recent      []Answer  `json:"recent_answers"`
}

// Gate mirrors the learning status the owner has to open: the gate's own
// answer, the overall state, the label counts and the top blockers.
type Gate struct {
	Open      bool          `json:"open"`
	State     string        `json:"state"`
	Labels    LabelTotals   `json:"labels"`
	Questions []Question    `json:"questions"`
	Blockers  []BlockerView `json:"blockers"`
	// Certified and Total are the questions stage's per-question gate:
	// how many questions have a certified route and how many the profile
	// has. The rest abstain; the gate is open as soon as one is certified.
	Certified int `json:"certified"`
	Total     int `json:"total"`
	// LabelsDone and LabelsTotal are the labels stage's progress: the
	// evaluation labels the certification needs, including the labels the
	// automatic judge buys. They move without a manual label.
	LabelsDone  int `json:"labels_done"`
	LabelsTotal int `json:"labels_total"`
}

// LabelTotals sums the label counts over every question.
type LabelTotals struct {
	Manual int `json:"manual"`
	Judge  int `json:"judge"`
	Needed int `json:"needed"`
	Review int `json:"review"`
}

// Question is one profile question's label position. Needed is the
// labels stage's requirement for it; Review is the number of judge labels
// without a manual label for the same message and question.
type Question struct {
	Question string `json:"question"`
	Manual   int    `json:"manual"`
	Judge    int    `json:"judge"`
	Needed   int    `json:"needed"`
	Review   int    `json:"review"`
}

// BlockerView is one learning blocker, unchanged from the learning stage
// (code, subject, diagnostic detail, measure). Actions stay on the
// learning screen.
type BlockerView struct {
	Code    string            `json:"code"`
	Subject string            `json:"subject,omitempty"`
	Detail  string            `json:"detail,omitempty"`
	Measure *learning.Measure `json:"measure,omitempty"`
}

// Mailbox is what the app currently sees: visible owner messages and the
// picked stores with visible mail, whether the last sync run completed
// successfully, and the evidence behind that answer. Synced is run truth, not
// "the learning stage once said done": a failed run clears it, a boot with no
// run yet starts false, and the timestamps let the UI say why.
type Mailbox struct {
	Messages      int    `json:"messages"`
	Stores        int    `json:"stores"`
	Synced        bool   `json:"synced"`
	Running       bool   `json:"running"`
	Stale         bool   `json:"stale"`
	LastAttempt   string `json:"last_attempt,omitempty"`
	LastSuccess   string `json:"last_success,omitempty"`
	NewestMessage string `json:"newest_message,omitempty"`
	SyncError     string `json:"sync_error,omitempty"`
}

// Answer is one current label (manual or judge) of visible owner mail. The
// value is raw label JSON; Type is filled by the API from the profile.
type Answer struct {
	ID        int64           `json:"id"`
	MessageID int64           `json:"message_id"`
	Question  string          `json:"question"`
	Source    string          `json:"source"`
	Value     json.RawMessage `json:"value"`
	At        string          `json:"at"`
}

// AnswerQuery selects one page of current labels, newest first.
type AnswerQuery struct {
	Question  string
	Source    string
	MessageID int64
	Cursor    int64
	Limit     int
}

// AnswerPage is one page of current labels; NextCursor is the last
// returned id.
type AnswerPage struct {
	Items      []Answer `json:"items"`
	NextCursor int64    `json:"next_cursor"`
	HasMore    bool     `json:"has_more"`
}

// LabelSummary is the label position the Answers screen and the hub share:
// the totals and every question with labels or a labels-stage blocker. It
// carries no mailbox counts and no second copy of the gate blockers, which
// the learning view already owns.
type LabelSummary struct {
	LabelTotals
	Questions []Question `json:"questions"`
}

// Hub reads the gate, the label position and the newest answers.
func (s *Service) Hub(ctx context.Context) (Hub, error) {
	state, err := s.labelState(ctx)
	if err != nil {
		return Hub{}, err
	}
	counts, err := s.mailboxCounts(ctx)
	if err != nil {
		return Hub{}, err
	}
	recent, err := s.recentAnswers(ctx)
	if err != nil {
		return Hub{}, err
	}
	syncStatus := syncer.Status{}
	if s.sync != nil {
		syncStatus = s.sync.Status()
	}
	mailbox := Mailbox{
		Messages: counts.messages, Stores: counts.stores,
		Synced:  syncStatus.Synced && counts.stores > 0,
		Running: syncStatus.Running, Stale: !syncStatus.Synced || counts.stores == 0,
		SyncError: syncStatus.LastError,
	}
	// mail.Stamp is the one "a derived instant is an empty string until it
	// exists" rule, so these need no zero check of their own.
	mailbox.LastAttempt = mail.Stamp(syncStatus.LastAttempt)
	mailbox.LastSuccess = mail.Stamp(syncStatus.LastSuccess)
	mailbox.NewestMessage = mail.Stamp(counts.newest)
	return Hub{
		GeneratedAt: s.now().UTC(),
		Gate: Gate{
			Open:        state.open,
			State:       state.status.State,
			Labels:      state.totals,
			Questions:   state.questions,
			Blockers:    state.blockers,
			Certified:   state.certified,
			Total:       state.total,
			LabelsDone:  state.labelDone,
			LabelsTotal: state.labelTotal,
		},
		Mailbox: mailbox,
		Recent:  recent.Items,
	}, nil
}

// Answers lists current manual/judge labels of visible owner mail, newest
// first by the label rowid. The API validates the query.
func (s *Service) Answers(ctx context.Context, q AnswerQuery) (AnswerPage, error) {
	rows, hasMore, err := s.store.AnswerRows(ctx, store.AnswerFilter{
		Question: q.Question, Source: q.Source, MessageID: q.MessageID, Cursor: q.Cursor, Limit: q.Limit,
	})
	if err != nil {
		return AnswerPage{}, err
	}
	page := AnswerPage{Items: make([]Answer, len(rows)), HasMore: hasMore}
	for i, row := range rows {
		page.Items[i] = Answer{ID: row.ID, MessageID: row.MessageID, Question: row.Question, Source: row.Source,
			Value: json.RawMessage(row.Value), At: row.At}
		page.NextCursor = row.ID
	}
	return page, nil
}

// LabelSummary reads the same label position as the hub, without mailbox
// counts or recent answers.
func (s *Service) LabelSummary(ctx context.Context) (LabelSummary, error) {
	state, err := s.labelState(ctx)
	if err != nil {
		return LabelSummary{}, err
	}
	return LabelSummary{LabelTotals: state.totals, Questions: state.questions}, nil
}

// labelCounts returns the two stored label aggregates behind the hub. An
// expired pair is served immediately while one background rebuild runs.
func (s *Service) labelCounts(ctx context.Context) ([]store.LabelCount, map[string]int, error) {
	value, err := s.labels.Get(ctx, s.store.LabelGeneration(), func(ctx context.Context) (labelCountsValue, error) {
		counts, review, err := s.buildLabelCounts(ctx)
		if err != nil {
			return labelCountsValue{}, err
		}
		return labelCountsValue{counts: counts, review: review}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return value.counts, value.review, nil
}

func (s *Service) buildLabelCounts(ctx context.Context) ([]store.LabelCount, map[string]int, error) {
	counts, err := s.store.LabelCounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	review, err := s.store.ReviewCounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	return counts, review, nil
}

// mailboxCounts returns the whole-mailbox count and the newest received
// mail. It reads messages, folders and scope, never labels; an expired pair
// is served immediately while one background rebuild runs. The newest stamp
// belongs in the same cached value so a warm hub survives a closed store and
// a hub read stays one cached pair of queries, not one extra query.
func (s *Service) mailboxCounts(ctx context.Context) (mailboxCountValue, error) {
	return s.mailbox.Get(ctx, s.store.MailGeneration(), func(ctx context.Context) (mailboxCountValue, error) {
		messages, stores, err := s.store.MailboxCounts(ctx)
		if err != nil {
			return mailboxCountValue{}, err
		}
		newest, err := s.store.NewestReceivedAt(ctx)
		if err != nil {
			return mailboxCountValue{}, err
		}
		return mailboxCountValue{messages: messages, stores: stores, newest: newest}, nil
	})
}

// recentAnswers returns the newest manual labels for the Home rail, serving
// a stale page immediately while one background rebuild runs.
func (s *Service) recentAnswers(ctx context.Context) (AnswerPage, error) {
	return s.recent.Get(ctx, s.store.LabelGeneration(), func(ctx context.Context) (AnswerPage, error) {
		return s.Answers(ctx, AnswerQuery{Source: store.LabelManual, Limit: RecentAnswerCount})
	})
}

// labelState is the one aggregation of the label position: the learning
// status, the per-question counts, the labels-stage requirement and the
// blockers. Hub and LabelSummary both read it.
type labelState struct {
	status     learning.Status
	open       bool
	questions  []Question
	totals     LabelTotals
	blockers   []BlockerView
	certified  int
	total      int
	labelDone  int
	labelTotal int
}

func (s *Service) labelState(ctx context.Context) (labelState, error) {
	status, gate, err := s.learning.Status(ctx)
	if err != nil {
		return labelState{}, err
	}
	open := gate.Open
	counts, review, err := s.labelCounts(ctx)
	if err != nil {
		return labelState{}, err
	}
	labels, labelsFound := stage(status, learning.StageLabels)
	labelDone, labelTotal := 0, 0
	if labelsFound {
		labelDone, labelTotal = int(labels.Progress.Done), int(labels.Progress.Total)
	}
	byQuestion := map[string]*Question{}
	question := func(name string) *Question {
		q := byQuestion[name]
		if q == nil {
			q = &Question{Question: name}
			byQuestion[name] = q
		}
		return q
	}
	for _, c := range counts {
		q := question(c.Question)
		switch c.Source {
		case store.LabelManual:
			q.Manual += c.Count
		case store.LabelJudge:
			q.Judge += c.Count
		}
	}
	for name, n := range review {
		question(name).Review = n
	}
	if labelsFound {
		for _, b := range labels.Blockers {
			if b.Subject == "" {
				continue
			}
			q := question(b.Subject)
			if b.Code == layer1.BlockTooFewEval && b.Measure != nil {
				q.Needed = int(math.Round(b.Measure.Need))
				if q.Needed < 0 {
					q.Needed = 0
				}
			}
		}
	}
	questions := make([]Question, 0, len(byQuestion))
	var totals LabelTotals
	for _, q := range byQuestion {
		questions = append(questions, *q)
		totals.Manual += q.Manual
		totals.Judge += q.Judge
		totals.Needed += q.Needed
		totals.Review += q.Review
	}
	sort.Slice(questions, func(i, j int) bool {
		ri, rj := remaining(questions[i]), remaining(questions[j])
		if ri != rj {
			return ri > rj
		}
		if questions[i].Review != questions[j].Review {
			return questions[i].Review > questions[j].Review
		}
		return questions[i].Question < questions[j].Question
	})
	certified, total := 0, 0
	if qs, ok := stage(status, learning.StageQuestions); ok {
		certified, total = int(qs.Progress.Done), int(qs.Progress.Total)
	}
	return labelState{
		// The learning service's gate answer is authoritative; a status
		// that already learned every stage is open even when a stale
		// snapshot passed open=false.
		status: status, open: open || status.State == learning.Learned, questions: questions, totals: totals,
		blockers: topBlockers(status, labelsFound, labels), certified: certified, total: total,
		labelDone: labelDone, labelTotal: labelTotal,
	}, nil
}

// remaining is how many labels a question still needs: the requirement
// minus the manual labels already stored, never negative.
func remaining(q Question) int {
	if q.Needed <= q.Manual {
		return 0
	}
	return q.Needed - q.Manual
}

// stage finds one stage status by id.
func stage(status learning.Status, id string) (learning.StageStatus, bool) {
	for _, st := range status.Stages {
		if st.ID == id {
			return st, true
		}
	}
	return learning.StageStatus{}, false
}

// topBlockers keeps the learning order: the labels-stage blockers, or the
// first blockers of the other stages when the labels stage has none.
func topBlockers(status learning.Status, labelsFound bool, labels learning.StageStatus) []BlockerView {
	out := make([]BlockerView, 0, BlockerLimit)
	add := func(b learning.Blocker) bool {
		out = append(out, BlockerView{Code: b.Code, Subject: b.Subject, Detail: b.Detail, Measure: b.Measure})
		return len(out) == BlockerLimit
	}
	if labelsFound {
		for _, b := range labels.Blockers {
			if add(b) {
				return out
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, st := range status.Stages {
		if st.ID == learning.StageLabels {
			continue
		}
		for _, b := range st.Blockers {
			if add(b) {
				return out
			}
		}
	}
	return out
}
