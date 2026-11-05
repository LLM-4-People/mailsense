// Package priority ranks the owner's important mail deterministically from
// the stored route answers and, while the learning gate's PreviewAnswers
// rule allows it, the route's threshold-free preview answers. It is pure
// scoring plus a small gated, cached service: no model is called and no new
// answer is invented.
//
// The score is one policy owned here (not a setting per weight):
//
//	suspicious >= .5                       +100
//	decision_needed >= .5                   +45
//	deadline level 5/4/3/2                  +45/+35/+25/+8
//	urgency level 4/3/2/1                   +40/+30/+12/+3
//	reply_needed >= .5                      +30
//	is_question >= .5                        +8
//	call_to_action >= .5                    +12
//	category action/work                    +12/+4
//	category notifications/marketing/spam   -18/-24/-30
//	effort level 3/2                         +8/+4
//	request_count level 3/2                  +8/+4
//	recency                                  +12·exp(-age_days/14)
//
// Security is a subject category, not a threat: only the suspicious answer
// raises the threat score or enters the security bucket. A negative category
// never masks a suspicious message. Automated bulk mail (a bulk category or
// an automated-service relationship) owns no reply and never enters the
// ranking, however urgent its text or however close its deadline. Otherwise
// an item enters the ranking only when it carries a substantive positive
// signal and scores at least 20; for non-bulk mail a suspicious, decision,
// reply, urgent or deadline signal always enters. Ties break by score, then
// received time, then message id, all descending.
package priority

import (
	"cmp"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Ranking policy constants.
const (
	DefaultLimit = 8
	MaxLimit     = 50
	MaxCached    = 200
	CacheTTL     = 15 * time.Second
	// maxCacheTTL bounds any cached ranking, so a mailbox never serves a
	// ranking older than this. An unchanged snapshot lives the full window;
	// a generation, route-key or preview change rebuilds immediately.
	maxCacheTTL = 5 * time.Minute

	// rankingBuildTimeout bounds every ranking build, cold or refresh, so a
	// stuck store read cannot hold a caller or the service's Close forever.
	rankingBuildTimeout = 2 * time.Minute

	// snippetRunes is the item snippet length. fillSnippets reads one
	// bounded body head for the cached items and covers the rare body that
	// begins with more whitespace than the projection.
	snippetRunes = 180

	recentDays   = 14.0
	recentPoints = 12.0
	minScore     = 20.0
)

// closeTimeout bounds Close's wait for in-flight builds. A build that misses
// it is reported; the store owner still closes after the wait. It is a
// variable only so a test can shorten the wait; production never changes it.
var closeTimeout = 10 * time.Second

// Signals is the thresholded, plain-value view of the answers used by the
// ranking and the topic census. Noul questions are 0/1 at .5; score
// questions are their nearest level; category, topic and asks_for are the
// chosen option ("" when absent). Probabilities are never exposed.
type Signals struct {
	Urgency         int    `json:"urgency"`
	ReplyNeeded     int    `json:"reply_needed"`
	Suspicious      int    `json:"suspicious"`
	DecisionNeeded  int    `json:"decision_needed"`
	Deadline        int    `json:"deadline"`
	Effort          int    `json:"effort"`
	RequestCount    int    `json:"request_count"`
	IsQuestion      int    `json:"is_question"`
	CallToAction    int    `json:"call_to_action"`
	InfoSufficiency int    `json:"info_sufficiency"`
	Category        string `json:"category"`
	AsksFor         string `json:"asks_for"`
	Stance          string `json:"stance"`
	Relationship    string `json:"relationship"`
	// Subject is the canonical topic/subtopic hierarchy derived from the
	// answers above. It is the one subject fact the ranking, Brief, Ask and
	// thread resolver read.
	Subject subject.Subject `json:"subject"`
}

// TopicEvidence is the number of recent message ids a census row carries.
const TopicEvidence = 3

// TopicCount is one topic of the whole-mailbox census: every owner message
// whose classified topic is Topic, with up to three recent evidence ids and
// the closed subtopic breakdown beneath it.
type TopicCount struct {
	Topic     string          `json:"topic"`
	Count     int             `json:"count"`
	Evidence  []int64         `json:"evidence"`
	Subtopics []SubtopicCount `json:"subtopics,omitempty"`
}

// SubtopicCount is one topic's subtopic rollup. Subtopic is the identifier
// inside the topic, never a full path; the pair is the hierarchy.
type SubtopicCount struct {
	Subtopic string  `json:"subtopic"`
	Count    int     `json:"count"`
	Evidence []int64 `json:"evidence"`
}

// TopicCensus is the classified-topic rollup of the mailbox the priority
// read model already streams. It is not derived from the ranking: a message
// that never enters the ranked list still counts here. Scanned is every
// owner message the store read, Classified is those carrying a topic, and
// Topics is sorted by count then name.
type TopicCensus struct {
	Topics     []TopicCount `json:"topics"`
	Classified int          `json:"classified"`
	Scanned    int          `json:"scanned"`
}

// Item is one ranked message as the priority API serves it.
type Item struct {
	MessageID      int64           `json:"message_id"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Subject        string          `json:"subject"`
	SenderName     string          `json:"sender_name"`
	SenderAddress  string          `json:"sender_address"`
	ReceivedAt     string          `json:"received_at"`
	FolderRole     mail.FolderRole `json:"folder_role"`
	HasAttachments bool            `json:"has_attachments"`
	Snippet        string          `json:"snippet"`
	Score          float64         `json:"score"`
	Reasons        []string        `json:"reasons"`
	Preview        bool            `json:"preview"`
	Signals        Signals         `json:"signals"`
	// Action is the one semantic consequence of the message and its thread:
	// what the owner must do, whether someone is waiting, and where any due
	// pressure came from. Screens read it instead of re-deriving it.
	Action action.Class `json:"action"`
	// Thread state is the conversation truth: a colleague's reply suppresses
	// the owner's reply obligation without exposing the colleague's body.
	ThreadState      string `json:"thread_state,omitempty"`
	ThreadReason     string `json:"thread_reason,omitempty"`
	ThreadActor      string `json:"thread_actor,omitempty"`
	ThreadLastAt     string `json:"thread_last_at,omitempty"`
	ThreadVisibility string `json:"thread_visibility,omitempty"`
	ThreadReplyTo    int64  `json:"thread_reply_to,omitempty"`
	// ReplyOpen is the derived obligation this row carries: the one truth
	// behind the Reply filter and the thread state.
	ReplyOpen bool `json:"reply_open"`
}

// Message is the message metadata the pure scorer reads.
type Message struct {
	MessageID      int64
	ConversationID string
	Subject        string
	SenderName     string
	SenderAddress  string
	ReceivedAt     string
	FolderRole     mail.FolderRole
	HasAttachments bool
	// StoredDue is an open follow-up date the mailbox stores for this
	// message; it is a fact, not a classifier estimate.
	StoredDue bool
	Body      string
	Revision  int64
}

// Input is one message's typed values for Score.
type Input struct {
	Message Message
	// Route is the stored route answer map for the current fingerprint.
	Route map[string]route.Answer
	// Preview is the threshold-free preview answer map; it replaces only
	// a route answer that abstained with route.AbstainNotCertified.
	Preview map[string]engine.Answer
	// Profile is the triage profile that defines the questions (the
	// preview trust boundary needs their types and distributions).
	Profile *taxonomy.Profile
	// Thread is the resolved conversation state; the zero value means no
	// thread evidence, so the message-level reply signal is used.
	Thread threadintel.Assessment
	Now    time.Time
}

// Score ranks one message. ok false means it does not enter the ranking.
// A malformed preview fails instead of becoming an answer. The returned item
// carries the snippet of Input.Message.Body.
func Score(in Input) (Item, bool, error) {
	item, ok, _, err := scoreWithSignals(in, true)
	return item, ok, err
}

// scoreWithSignals ranks one message and returns its signals even when it does not
// enter the ranking, so the topic census can count every read message
// without decoding its answers twice. withSnippet is false for the ranking
// build, which fills the cached items' snippets from bounded store heads
// after the ranking is known.
func scoreWithSignals(in Input, withSnippet bool) (Item, bool, Signals, error) {
	if in.Profile == nil {
		return Item{}, false, Signals{}, errors.New("priority: a triage profile is required")
	}
	rs, err := signalsFor(in.Profile, in.Route, in.Preview)
	if err != nil {
		return Item{}, false, Signals{}, err
	}
	at, dated := mail.Parse(in.Message.ReceivedAt)
	item, ok := scoreRowSignals(in, rs, at, dated, withSnippet)
	return item, ok, rs.sig, nil
}

// scoreSignals applies the one weight table. include is the entry rule:
// a substantive positive signal and a score of at least 20, or one of the
// always-included signals. Automated bulk mail is excluded before scoring:
// it owns no reply and never becomes an Important row, even when its text
// names urgency, a deadline or a call to action. bulk and the parsed receive
// instant are derived once per row by rowSignalsFor/the reply pass.
func scoreSignals(rs rowSignals, now, at time.Time, dated bool, reply bool) ([]string, float64, bool) {
	sig := rs.sig
	suspicious := sig.Suspicious == 1
	if rs.bulk {
		return nil, 0, false
	}
	decision := sig.DecisionNeeded == 1

	score := 0.0
	if suspicious {
		score += 100
	}
	if decision {
		score += 45
	}
	switch sig.Deadline {
	case 5:
		score += 45
	case 4:
		score += 35
	case 3:
		score += 25
	case 2:
		score += 8
	}
	switch sig.Urgency {
	case 4:
		score += 40
	case 3:
		score += 30
	case 2:
		score += 12
	case 1:
		score += 3
	}
	if reply {
		score += 30
	}
	if rs.question {
		score += 8
	}
	if rs.cta {
		score += 12
	}
	switch sig.Category {
	case "action":
		score += 12
	case "work":
		score += 4
	case "notifications":
		if !suspicious {
			score -= 18
		}
	case "marketing":
		if !suspicious {
			score -= 24
		}
	case "spam":
		if !suspicious {
			score -= 30
		}
	}
	switch sig.Effort {
	case 3:
		score += 8
	case 2:
		score += 4
	}
	switch sig.RequestCount {
	case 3:
		score += 8
	case 2:
		score += 4
	}
	age, present := ageDays(at, now, dated)
	recency := 0.0
	if present {
		recency = recentPoints * math.Exp(-age/recentDays)
	}
	score += recency
	recent := present && age <= recentDays

	act := rs.cta || sig.Category == "action" || actionAsk(sig.AsksFor)
	// The bands are the classifier's one rule (action.Bands): the ranking's
	// reasons and filters must not classify the same levels differently.
	bands := sig.Bands()
	workload := bands.Workload()
	urgent := bands.UrgencyHigh()
	deadline := bands.DeadlineSoon()
	positive := suspicious || decision || sig.Deadline >= 2 || sig.Urgency >= 1 || reply ||
		rs.question || rs.cta || act || sig.Category == "action" || sig.Category == "work" || workload
	forced := suspicious || decision || reply || urgent || deadline || act
	if !forced && (!positive || score < minScore) {
		return nil, 0, false
	}
	reasons := make([]string, 0, 9)
	if suspicious {
		reasons = append(reasons, "security")
	}
	if decision {
		reasons = append(reasons, "decision")
	}
	if reply {
		reasons = append(reasons, "reply")
	}
	if urgent {
		reasons = append(reasons, "urgent")
	}
	if deadline {
		reasons = append(reasons, "deadline")
	}
	if act {
		reasons = append(reasons, "action")
	}
	if rs.question {
		reasons = append(reasons, "question")
	}
	if workload {
		reasons = append(reasons, "workload")
	}
	if recent {
		reasons = append(reasons, "recent")
	}
	return reasons, score, true
}

// actionAsk reports whether the dominant ask is concrete work the owner must
// do, read from the classifier's one ask table. It is distinct from a reply: a
// payment or a meeting request is an action even when no answer is owed. An
// explicit ask always enters the ranking, so "what to act on" cannot lose an
// old request to recency.
func actionAsk(asksFor string) bool {
	_, concrete := action.AskAction(asksFor)
	return concrete
}

// ageDays is how old at is at now, clamped at zero for a future instant.
// dated reports whether the stored timestamp could be read; an undated
// message contributes no recency, never the age of the zero instant.
func ageDays(at, now time.Time, dated bool) (float64, bool) {
	if !dated {
		return 0, false
	}
	age := now.Sub(at).Hours() / 24
	if age < 0 {
		age = 0
	}
	return age, true
}

// Snippet collapses whitespace and cuts a plain-text body to max runes. It
// never receives HTML (the body it reads is the canonical plain text).
// Snippet is the first maxRunes non-space runes of body with every
// whitespace run collapsed to one space. It stops once the requested runes
// are collected: a 2,000-character body must not be flattened in full to
// keep 180 runes.
func Snippet(body string, maxRunes int) string {
	if maxRunes <= 0 || body == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(body), maxRunes*4))
	count := 0
	pendingSpace := false
	for _, r := range body {
		if unicode.IsSpace(r) {
			if count > 0 {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			if count >= maxRunes {
				break
			}
			b.WriteByte(' ')
			count++
			pendingSpace = false
		}
		if count >= maxRunes {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

// matches reports whether an item satisfies a filter.
func (f Filter) matches(it Item) bool {
	switch f {
	case FilterAll:
		return true
	case FilterReply:
		return it.ReplyOpen
	case FilterUrgent:
		return it.Signals.Bands().Urgent()
	case FilterSecurity:
		// Security is a subject category, not a threat: a password reset or a
		// verification code belongs here only when the suspicious judgment is
		// positive. Spam is a bulk threat class and stays in the threat bucket.
		return it.Signals.Suspicious == 1 || it.Signals.Category == "spam"
	case FilterDecision:
		// The action classification is the one truth: an older message that
		// asked for a verdict no longer counts once the thread moved on.
		return it.Action.Primary == action.Decide && it.Action.Waiting == action.WaitingOwner
	case FilterDeadline:
		return it.Signals.Bands().DeadlineSoon()
	case FilterWorkload:
		return it.Signals.Bands().Workload()
	}
	return false
}

// rankedItem is one cached item with its parsed instant for ordering and the
// message revision its scoring row carried.
type rankedItem struct {
	item     Item
	received time.Time
	revision int64
}

// rankLess orders higher scores first, then newer messages, then higher ids.
func rankLess(a, b rankedItem) int {
	if a.item.Score != b.item.Score {
		return cmp.Compare(b.item.Score, a.item.Score)
	}
	if !a.received.Equal(b.received) {
		return b.received.Compare(a.received)
	}
	return cmp.Compare(b.item.MessageID, a.item.MessageID)
}

// topHeap is a bounded min-heap of the best MaxCached items: the root is
// the worst kept item, so a stream of any size needs only this memory.
type topHeap struct{ items []rankedItem }

func (h *topHeap) Len() int           { return len(h.items) }
func (h *topHeap) Less(i, j int) bool { return rankLess(h.items[j], h.items[i]) < 0 }
func (h *topHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *topHeap) Push(x any)         { h.items = append(h.items, x.(rankedItem)) }
func (h *topHeap) Pop() any {
	old := h.items
	n := len(old)
	out := old[n-1]
	h.items = old[:n-1]
	return out
}

func (h *topHeap) add(r rankedItem) {
	if len(h.items) < MaxCached {
		heap.Push(h, r)
		return
	}
	if rankLess(r, h.items[0]) < 0 {
		heap.Pop(h)
		heap.Push(h, r)
	}
}

// Store is the priority read side (store.Store).
type Store interface {
	// EachPriorityMessage decodes only the answers named by needed (the
	// profile questions the ranking reads); every answer is decoded when it
	// is empty.
	EachPriorityMessage(ctx context.Context, fingerprint string, includePreview bool, needed []store.QuestionKey, fn func(store.PriorityRow) error) error
	// EachPriorityReply is the body-less variant for the pass that only
	// resolves reply obligations and conversation ids. needed has the same
	// meaning as in EachPriorityMessage.
	EachPriorityReply(ctx context.Context, fingerprint string, includePreview bool, needed []store.QuestionKey, fn func(store.PriorityRow) error) error
	// PriorityHeads reads the bounded body heads of the cached items in one
	// query, with their revisions so a head that no longer matches its row
	// is discarded.
	PriorityHeads(ctx context.Context, ids []int64) (map[int64]store.PriorityBody, error)
	// MessageBody reads one owner-visible message's full body, used only
	// when the bounded head cannot fill the item snippet.
	MessageBody(ctx context.Context, messageID int64) (string, error)
	// MailGeneration and RouteGeneration identify the mail and the stored
	// answers a thread-state resolution was computed from, so the priority
	// service reuses it until either moves.
	MailGeneration() uint64
	RouteGeneration() uint64
}

// Gate is the app-wide learning gate.
type Gate interface {
	Allow(ctx context.Context) error
}

// previewGate is the learning machine's preview rule. It mirrors
// triage.previewGate: a gate that does not implement it is preview off
// (tests and older fakes keep the previous default).
type previewGate interface {
	PreviewAnswers(ctx context.Context) bool
}

// previewing reports whether preview values may substitute answers. The gate
// owns the one rule (learning.Machine.PreviewAnswers: the explicit setting is
// on and the real gate is closed), so a certified route is never relabelled as
// a best guess. It deliberately does not fall back to cfg.Preview().
func (s *Service) previewing(ctx context.Context) bool {
	g, ok := s.gate.(previewGate)
	return ok && g.PreviewAnswers(ctx)
}

// Threads resolves cross-store conversation state. It is optional: a nil
// provider keeps the message-level reply signal (the old behavior).
type Threads interface {
	ThreadStates(ctx context.Context, conversationIDs []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error)
}

// Admission is the resource guard's one decision for a ranking build. A nil
// dependency means no admission: tests, tools and a build without a guard run
// exactly as before. The error is returned unchanged to the caller (wrapped
// only to mark a pause), so a guard's typed ceiling error keeps its identity
// for the API's resource_limit mapping.
type Admission interface {
	Admit(ctx context.Context) error
}

// ErrPaused marks a build that stopped because admission refused it. The
// guard's own error stays in the chain (errors.Is(resource.ErrLimited) and
// errors.As(*resource.LimitError) still match); this sentinel lets the
// service log a pause as a pause instead of a store failure. Priority does
// not import the resource package: the guard is adapted at the app's one
// wiring point.
var ErrPaused = errors.New("priority: ranking build paused by admission")

// Deps are the service collaborators. Store, Gate and Config are required;
// Clock defaults to the wall clock, Threads and Admission are optional.
type Deps struct {
	Store  Store
	Gate   Gate
	Config *config.Config
	Clock  clock.Clock
	// RouteKey is the configuration identity the route rows are stored
	// under (layer1.RouteKey in the app). Priority cannot import layer1, so
	// the key is injected at the one wiring point.
	RouteKey func() (string, error)
	Threads  Threads
	// Admission gates every ranking build. The app constructs the service
	// before its API wire owns the guard, so SetAdmission is the late
	// binding; this field serves construction-time owners.
	Admission Admission
}

// Service is the gated, cached priority ranking.
type Service struct {
	store   Store
	gate    Gate
	cfg     *config.Config
	clock   clock.Clock
	threads Threads
	mu      sync.Mutex
	cached  *cachedRanking
	// refreshing reports one background rebuild in flight. It is guarded by
	// mu so an expired cache is never rebuilt by two requests.
	refreshing bool
	// refreshDone closes when the one background refresh finishes. A cold
	// build for another snapshot identity waits on it instead of scanning
	// beside the refresh; it is created and cleared under mu.
	refreshDone chan struct{}
	// cold is the one first build in flight. Every cold reader joins it;
	// a caller that stops waiting never cancels the shared build.
	cold *coldBuild
	// closed stops new builds and is set before Close cancels a running one.
	closed bool
	// closeJoined records the bounded join result so a repeated Close reports
	// the same outcome. A concurrent Close that arrives before the first one
	// settles reports false: it could not confirm the join.
	closeJoined bool
	// baseCtx owns the service's builds: Close cancels it so a shutdown does
	// not wait for a store scan that no response will read.
	baseCtx context.Context
	cancel  context.CancelFunc
	// builds counts cold and refresh builds for Close's bounded join.
	builds sync.WaitGroup
	// threadMu guards the resolved conversation states, reused across
	// ranking builds while the mail and the stored answers are unchanged.
	threadMu     sync.Mutex
	threadKey    string
	threadStates map[string]threadintel.Assessment
	// routeKey is the injected configuration identity of the route rows.
	routeKey func() (string, error)
	// admissionMu guards the late-bound admission dependency. It is read once
	// per phase check, never held across a store read.
	admissionMu sync.RWMutex
	admission   Admission
}

// coldBuild is one in-flight first build. done closes after c or err is set.
type coldBuild struct {
	preview  bool
	routeKey string
	mailGen  uint64
	routeGen uint64
	done     chan struct{}
	c        *cachedRanking
	err      error
}

type cachedRanking struct {
	preview  bool
	routeKey string
	// mailGen and routeGen are the store generations the build started
	// from. A commit that writes new mail or new route answers under the
	// same configuration (a re-score, a new pass) must not serve this
	// snapshot beyond the TTL. The build records the values it sampled, so
	// a write that lands mid-build leaves older values and the next read
	// rebuilds instead of trusting a mixed scan.
	mailGen  uint64
	routeGen uint64
	// at is when the snapshot finished building, not when the build started:
	// a build longer than CacheTTL must not install an already-expired cache
	// and rebuild forever.
	at     time.Time
	build  time.Duration
	items  []rankedItem
	counts map[Filter]int
	states map[string]threadintel.Assessment
	topics TopicCensus
}

// New returns a priority service.
func New(cfg *config.Config, d Deps) (*Service, error) {
	if cfg == nil || d.Store == nil || d.Gate == nil || d.RouteKey == nil {
		return nil, errors.New("priority: a config, store, gate and route key are required")
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Service{store: d.Store, gate: d.Gate, cfg: cfg, clock: clock.Or(d.Clock), threads: d.Threads,
		routeKey: d.RouteKey, baseCtx: baseCtx, cancel: cancel, admission: d.Admission}, nil
}

// SetAdmission installs or clears the resource guard after construction. The
// app builds the service before its API wire owns the guard, so this is the
// one late binding; it must run before the first read (the API wire does). A
// nil value restores the no-admission behavior.
func (s *Service) SetAdmission(a Admission) {
	s.admissionMu.Lock()
	s.admission = a
	s.admissionMu.Unlock()
}

// admit asks the guard whether a heavy ranking build may run or continue.
// A nil dependency always admits. The guard's error is wrapped only with the
// local pause marker, so its typed ceiling detail still reaches the API.
func (s *Service) admit(ctx context.Context) error {
	s.admissionMu.RLock()
	a := s.admission
	s.admissionMu.RUnlock()
	if a == nil {
		return nil
	}
	if err := a.Admit(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrPaused, err)
	}
	return nil
}

// Rank returns one filtered page, the number of candidates that pass the
// filter (not the whole mailbox), and whether the ranking's signals used
// preview answers.
// ThreadStates returns the resolved state of every conversation that has a
// ranked message. It is the one thread truth the priority list and the
// agenda share, so "waiting on you" cannot mean two different things on two
// screens.
func (s *Service) ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error) {
	c, err := s.ranking(ctx)
	if err != nil {
		return nil, err
	}
	// The cached snapshot is immutable after the build; every caller only
	// reads it (world, conversations, agenda, commitments). Returning it
	// avoids copying tens of thousands of assessments on every boot.
	return c.states, nil
}

// Top returns the n best items of the unfiltered ranking (Q&A's context
// merge and signals). n is bounded by the cache size.
func (s *Service) Top(ctx context.Context, n int) ([]Item, error) {
	if n < 1 || n > MaxCached {
		return nil, countError("top", n)
	}
	c, err := s.ranking(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, min(n, len(c.items)))
	for _, r := range c.items {
		out = append(out, r.item)
		if len(out) == n {
			break
		}
	}
	return out, nil
}

// Actions returns the ranked messages that carry a concrete owner action,
// best first. It is the one source the commitments service reads for its
// action kind; the caller owns the page size and the dedupe against reply
// and decision items. Read, none and wait are not actions and never appear.
func (s *Service) Actions(ctx context.Context, limit int) ([]Item, error) {
	if limit < 1 || limit > MaxCached {
		return nil, countError("action", limit)
	}
	c, err := s.ranking(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, min(limit, len(c.items)))
	for _, r := range c.items {
		if !r.item.Action.Primary.OwnerAction() {
			continue
		}
		out = append(out, r.item)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Decisions returns the ranked messages whose action truth is a live owner
// decision, best first. It is the one source the commitments service reads
// for its decision kind; unlike the API page, it is not bounded by MaxLimit,
// so the read model's counts are not truncated by a screen size. The cache
// itself still bounds the set at MaxCached.
func (s *Service) Decisions(ctx context.Context, limit int) ([]Item, error) {
	if limit < 1 || limit > MaxCached {
		return nil, countError("decision", limit)
	}
	c, err := s.ranking(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, min(limit, len(c.items)))
	for _, r := range c.items {
		if !FilterDecision.matches(r.item) {
			continue
		}
		out = append(out, r.item)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Topics returns the whole-mailbox topic census built during the same read
// as the ranking. It is not the ranked list: every owner message the store
// streamed is counted, including mail that never scores.
func (s *Service) Topics(ctx context.Context) (TopicCensus, error) {
	c, err := s.ranking(ctx)
	if err != nil {
		return TopicCensus{}, err
	}
	return cloneTopicCensus(c.topics), nil
}

func cloneTopicCensus(in TopicCensus) TopicCensus {
	out := TopicCensus{Classified: in.Classified, Scanned: in.Scanned,
		Topics: make([]TopicCount, len(in.Topics))}
	for i, topic := range in.Topics {
		out.Topics[i] = TopicCount{Topic: topic.Topic, Count: topic.Count,
			Evidence: append([]int64(nil), topic.Evidence...)}
		out.Topics[i].Subtopics = make([]SubtopicCount, len(topic.Subtopics))
		for j, sub := range topic.Subtopics {
			out.Topics[i].Subtopics[j] = SubtopicCount{Subtopic: sub.Subtopic, Count: sub.Count,
				Evidence: append([]int64(nil), sub.Evidence...)}
		}
	}
	return out
}

// topicRollup accumulates one topic's count, its three most recent evidence
// ids and the same bounded evidence for each subtopic while the store streams
// the mailbox.
type topicRollup struct {
	topic    string
	count    int
	evidence []topicEvidence
	subs     map[string]*subtopicRollup
}

type subtopicRollup struct {
	subtopic string
	count    int
	evidence []topicEvidence
}

type topicEvidence struct {
	messageID int64
	at        time.Time
}

func (r *topicRollup) add(messageID int64, at time.Time, subtopic string) {
	r.count++
	r.evidence = addEvidence(r.evidence, messageID, at)
	subtopic = strings.TrimSpace(subtopic)
	if subtopic == "" {
		return
	}
	if r.subs == nil {
		r.subs = map[string]*subtopicRollup{}
	}
	sub := r.subs[subtopic]
	if sub == nil {
		sub = &subtopicRollup{subtopic: subtopic}
		r.subs[subtopic] = sub
	}
	sub.count++
	sub.evidence = addEvidence(sub.evidence, messageID, at)
}

// addEvidence keeps the three newest ids, newest first then higher id.
func addEvidence(evidence []topicEvidence, messageID int64, at time.Time) []topicEvidence {
	candidate := topicEvidence{messageID: messageID, at: at}
	if len(evidence) < TopicEvidence {
		evidence = append(evidence, candidate)
		slices.SortFunc(evidence, topicEvidenceLess)
		return evidence
	}
	if topicEvidenceLess(candidate, evidence[TopicEvidence-1]) < 0 {
		evidence[TopicEvidence-1] = candidate
		slices.SortFunc(evidence, topicEvidenceLess)
	}
	return evidence
}

// topicEvidenceLess orders newer mail first, then the higher id, so the
// three kept ids are the most recent evidence for the topic.
func topicEvidenceLess(a, b topicEvidence) int {
	if c := b.at.Compare(a.at); c != 0 {
		return c
	}
	return cmp.Compare(b.messageID, a.messageID)
}

// topicCensusFromRollups turns the per-topic accumulators into the stable
// wire order: count descending, then topic name.
func topicCensusFromRollups(rollups map[string]*topicRollup, scanned int) TopicCensus {
	topics := make([]TopicCount, 0, len(rollups))
	classified := 0
	for _, rollup := range rollups {
		if rollup.count == 0 {
			continue
		}
		classified += rollup.count
		ids := evidenceIDs(rollup.evidence)
		subs := make([]SubtopicCount, 0, len(rollup.subs))
		for _, sub := range rollup.subs {
			subs = append(subs, SubtopicCount{Subtopic: sub.subtopic, Count: sub.count,
				Evidence: evidenceIDs(sub.evidence)})
		}
		slices.SortFunc(subs, func(a, b SubtopicCount) int {
			if a.Count != b.Count {
				return cmp.Compare(b.Count, a.Count)
			}
			return strings.Compare(a.Subtopic, b.Subtopic)
		})
		topics = append(topics, TopicCount{Topic: rollup.topic, Count: rollup.count, Evidence: ids, Subtopics: subs})
	}
	slices.SortFunc(topics, func(a, b TopicCount) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return strings.Compare(a.Topic, b.Topic)
	})
	return TopicCensus{Topics: topics, Classified: classified, Scanned: scanned}
}

func evidenceIDs(evidence []topicEvidence) []int64 {
	ids := make([]int64, 0, len(evidence))
	for _, item := range evidence {
		ids = append(ids, item.messageID)
	}
	return ids
}

// ranking returns the cached ranking for this preview flag. A warm, fresh
// cache is returned directly. An unchanged snapshot stays fresh for the whole
// maxCacheTTL window, so an idle mailbox does not rescan its routed rows every
// CacheTTL. An expired snapshot is returned immediately while one background
// rebuild runs, so no request waits for a cache expiry. The first build (and a
// preview switch) blocks because there is no matching snapshot to serve yet.
// freshness is the adaptive age bound for a snapshot. The base TTL is the
// interactive contract; a build that takes longer than half of it gets three
// times its own duration (bounded), so a slow mailbox never rebuilds
// continuously and starves the reads it is meant to serve. The cached branch
// keeps an unchanged snapshot for maxCacheTTL instead.
func freshness(c *cachedRanking) time.Duration {
	ttl := CacheTTL
	if c.build > CacheTTL/2 {
		ttl = 3 * c.build
	}
	return min(max(ttl, CacheTTL), maxCacheTTL)
}

func (s *Service) ranking(ctx context.Context) (*cachedRanking, error) {
	if err := s.gate.Allow(ctx); err != nil {
		return nil, err
	}
	// The gate owns the preview rule. Read it once per ranking request so the
	// build, the store query and the snapshot identity all see one value; a
	// data race or a mid-build gate flip must not mix preview and route rows.
	preview := s.previewing(ctx)
	now := s.clock.Now()
	// The route key is part of the snapshot identity: a configuration change
	// must not serve the previous configuration's ranking even inside the
	// TTL.
	routeKey, err := s.routeKey()
	if err != nil {
		return nil, err
	}

	for {
		// The generations are part of the snapshot identity: every read of
		// the ranking is built from the mail and route rows of one moment.
		mailGen, routeGen := s.store.MailGeneration(), s.store.RouteGeneration()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, context.Canceled
		}
		if c := s.cached; c != nil && c.preview == preview && c.routeKey == routeKey {
			// A snapshot from an older generation is still the best answer
			// this request can have: a sync batch must not block the first
			// screen behind a full rebuild. Serve it and rebuild behind the
			// request; the same one guarded build covers an expired window.
			//
			// An unchanged snapshot is rebuilt at most every maxCacheTTL:
			// its mail rows, route answers, route key and preview are the
			// same, so only age-dependent scores (the recent window and
			// deadlines) can drift, and maxCacheTTL bounds that drift. A
			// generation, route-key or preview change still rebuilds
			// immediately.
			unchanged := c.mailGen == mailGen && c.routeGen == routeGen
			fresh := now.Sub(c.at) < freshness(c)
			if unchanged {
				fresh = now.Sub(c.at) < maxCacheTTL
			}
			// One rebuild inside the service: a cold build for another
			// snapshot identity (a preview flip, a generation move) already
			// scans, so this request keeps the cached snapshot and refreshes
			// on a later read.
			if unchanged && (fresh || s.refreshing) {
				s.mu.Unlock()
				return c, nil
			}
			if s.refreshing || s.cold != nil {
				s.mu.Unlock()
				return c, nil
			}
			s.mu.Unlock()
			// Above a ceiling a stale ranking is still a good ranking: serve
			// it, start nothing, and let the next admitted read refresh.
			if err := s.admit(ctx); err != nil {
				if errors.Is(err, ErrPaused) {
					logRefreshPaused(err)
				}
				return c, nil
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return nil, context.Canceled
			}
			if s.refreshing || s.cold != nil || s.cached != c {
				// Another reader claimed the build slot, or a newer snapshot
				// landed while admission ran. Answer with the current one.
				cur := s.cached
				s.mu.Unlock()
				if cur != nil && cur.preview == preview && cur.routeKey == routeKey {
					return cur, nil
				}
				return c, nil
			}
			s.refreshing = true
			s.refreshDone = make(chan struct{})
			s.builds.Add(1)
			s.mu.Unlock()
			go s.refreshRanking(ctx, preview, now, c, routeKey)
			return c, nil
		}
		if call := s.cold; call != nil {
			s.mu.Unlock()
			select {
			case <-call.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if call.preview != preview || call.routeKey != routeKey ||
				call.mailGen != mailGen || call.routeGen != routeGen {
				// The owner switched the preview or the configuration, or a
				// commit landed while the first build ran. Wait for that
				// build to clear, then build the current one.
				now = s.clock.Now()
				continue
			}
			s.mu.Lock()
			c, err := call.c, call.err
			s.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return c, nil
		}
		if s.refreshing {
			// Coalesce with the one background build: a cold snapshot for a
			// different identity waits for the refresh to finish instead of
			// starting a second full scan beside it.
			done := s.refreshDone
			s.mu.Unlock()
			if done == nil {
				continue
			}
			select {
			case <-done:
				now = s.clock.Now()
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.mu.Unlock()
		// No snapshot and no build in flight: this request must scan. Above
		// a ceiling it refuses with the guard's own error; nothing is read
		// and the next request retries.
		if err := s.admit(ctx); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, context.Canceled
		}
		if s.cached != nil && s.cached.preview == preview && s.cached.routeKey == routeKey {
			// A snapshot (or a refresh's result) landed while admission ran.
			s.mu.Unlock()
			continue
		}
		if s.cold != nil || s.refreshing {
			// Another reader started the build while admission ran; loop to
			// join it instead of scanning twice.
			s.mu.Unlock()
			continue
		}
		call := &coldBuild{preview: preview, routeKey: routeKey, mailGen: mailGen, routeGen: routeGen, done: make(chan struct{})}
		s.cold = call
		s.builds.Add(1)
		s.mu.Unlock()
		go s.runColdBuild(ctx, call, now)

		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
		c, err := call.c, call.err
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// buildDetached runs one ranking build off the request path and returns its
// result or its failure. safe.Detached owns the build's context (detached from
// the request that ends with its response, bounded by rankingBuildTimeout,
// cancelled through baseCtx by Close) and its panic path, because the cold and
// refresh latches are what every ranking read waits on. This function owns what
// only a ranking build needs: the resource guard gets the last word inside the
// same bounded context, a panic becomes this package's own error, and the
// install is left to the caller, whose admission rule differs between the two.
// what names the work in the log and the error.
func (s *Service) buildDetached(ctx context.Context, what string, fn func(context.Context) (*cachedRanking, error)) (*cachedRanking, error) {
	c, err := safe.Detached(ctx, s.baseCtx, rankingBuildTimeout, func(buildCtx context.Context) (*cachedRanking, error) {
		c, err := fn(buildCtx)
		if err == nil {
			err = s.admit(buildCtx)
		}
		return c, err
	})
	var panicErr *safe.PanicError
	if errors.As(err, &panicErr) {
		logx.For("priority").Error(what+" panicked", "panic_type", fmt.Sprintf("%T", panicErr.Value))
		err = panicError(what, panicErr.Value)
	}
	return c, err
}

// runColdBuild executes the one first build and publishes its result to
// every caller waiting on call. The build is detached from the caller that
// happened to create it: a response ending early is not a reason to repeat
// the scan for the next caller. Close still cancels it through baseCtx.
func (s *Service) runColdBuild(ctx context.Context, call *coldBuild, now time.Time) {
	defer s.builds.Done()
	// A panic must still clear the one cold latch: every ranking read waits
	// on call.done, and a done channel no goroutine closes would freeze the
	// screen (and every service that joins the thread truth) until exit.
	defer func() {
		if p := recover(); p != nil {
			logx.For("priority").Error("ranking build panicked", "panic_type", fmt.Sprintf("%T", p))
			s.mu.Lock()
			call.err = panicError("ranking build", p)
			s.mu.Unlock()
		}
		s.mu.Lock()
		if s.cold == call {
			s.cold = nil
		}
		select {
		case <-call.done:
		default:
			close(call.done)
		}
		s.mu.Unlock()
	}()
	c, err := s.buildDetached(ctx, "ranking build", func(buildCtx context.Context) (*cachedRanking, error) {
		return s.buildRanking(buildCtx, call.preview, now, call.routeKey)
	})

	s.mu.Lock()
	if err == nil {
		s.cached = c
		call.c = c
	} else {
		call.err = err
	}
	s.mu.Unlock()
}

// refreshRanking rebuilds one expired ranking off the request path. The
// request context is detached (it ends with the response) and bounded by
// rankingBuildTimeout. A failure keeps the old cache, and a result is
// installed only when the snapshot it was based on is still current, so a
// preview switch or a newer build is never overwritten.
func (s *Service) refreshRanking(ctx context.Context, preview bool, now time.Time, base *cachedRanking, routeKey string) {
	defer s.builds.Done()
	// The refresh owns the one waiters' channel: every exit, including a
	// panic, must clear refreshing and release the cold readers waiting on
	// it. A stale channel would wedge the next ranking read forever.
	defer func() {
		if p := recover(); p != nil {
			logx.For("priority").Error("ranking refresh panicked", "panic_type", fmt.Sprintf("%T", p))
		}
		s.mu.Lock()
		s.refreshing = false
		if s.refreshDone != nil {
			close(s.refreshDone)
			s.refreshDone = nil
		}
		s.mu.Unlock()
	}()
	c, err := s.buildDetached(ctx, "ranking refresh", func(buildCtx context.Context) (*cachedRanking, error) {
		return s.buildRanking(buildCtx, preview, now, routeKey)
	})
	s.mu.Lock()
	if err == nil && s.cached == base && !s.closed {
		s.cached = c
	}
	s.mu.Unlock()
	switch {
	case err == nil:
	case errors.Is(err, ErrPaused):
		logRefreshPaused(err)
	default:
		logx.For("priority").Warn("priority ranking refresh failed; serving the cached ranking", "err", err)
	}
}

// Close stops new builds, cancels the one in flight and waits up to
// closeTimeout for both it and any refresh. It is idempotent. Production
// store reads honor the context, so the wait normally ends immediately; a
// build that ignores cancellation is logged and Close reports false, so the
// store owner keeps the store open instead of closing it under the build.
func (s *Service) Close() bool {
	s.mu.Lock()
	if s.closed {
		joined := s.closeJoined
		s.mu.Unlock()
		return joined
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.builds.Wait()
		close(done)
	}()
	joined := true
	select {
	case <-done:
	case <-time.After(closeTimeout):
		joined = false
		logx.For("priority").Warn("priority builds did not stop before shutdown")
	}
	s.mu.Lock()
	s.closeJoined = joined
	s.mu.Unlock()
	return joined
}

// buildRanking computes one complete ranking without touching the cache or
// its lock. The returned snapshot is immutable after this call.
func (s *Service) buildRanking(ctx context.Context, preview bool, now time.Time, routeKey string) (*cachedRanking, error) {
	start := s.clock.Now()
	logx.For("priority").Info("priority ranking build started", "preview", preview)
	profile, err := taxonomy.Load(s.cfg)
	if err != nil {
		return nil, err
	}
	c := &cachedRanking{preview: preview, routeKey: routeKey, counts: map[Filter]int{}}
	states := map[string]threadintel.Assessment{}
	// One scan holds one record per owner row: its metadata and the decoded
	// classifier evidence the scoring pass reads. The evidence used to live in
	// a second per-message map, which doubled the mailbox-sized retention for
	// no consumer. The thread resolver's view is derived from these records
	// only for the duration of its one call (threadSignalsFor).
	var rows *rowBuffer
	// The scan holds the rows this snapshot ranks. The generations are
	// sampled before the scan and re-checked after it, so the rows, their
	// signals and the recorded generations always describe the same
	// mailbox. The metadata second scan this replaces could merge a row
	// from a newer generation into an older snapshot.
	var mailGen, routeGen uint64
	var conversations map[string]bool
	for attempt := 0; ; attempt++ {
		// Every attempt is a full mailbox scan: admission is asked before
		// each one, including the raced-build retry.
		if err := s.admit(ctx); err != nil {
			return nil, err
		}
		mailGen, routeGen = s.store.MailGeneration(), s.store.RouteGeneration()
		var err error
		rows, conversations, err = s.replySignals(ctx, profile, routeKey, preview)
		if err != nil {
			return nil, err
		}
		if s.store.MailGeneration() == mailGen && s.store.RouteGeneration() == routeGen {
			break
		}
		if attempt == 1 {
			// The mailbox is changing under every pass. Rank the stable rows
			// this pass saw and record their generation, so the next request
			// refreshes behind the snapshot instead of serving it as fresh.
			logx.For("priority").Warn("priority ranking built while the mailbox was changing")
			break
		}
	}
	c.mailGen, c.routeGen = mailGen, routeGen
	// The scan is the heaviest read. Ask again before the thread resolution,
	// which is the other whole-mailbox read: a pause here abandons the build
	// before it allocates further.
	if err := s.admit(ctx); err != nil {
		return nil, err
	}
	if s.threads != nil && len(conversations) > 0 {
		ids := make([]string, 0, len(conversations))
		for id := range conversations {
			ids = append(ids, id)
		}
		key := fmt.Sprintf("%d|%d|%s|%t", mailGen, routeGen, routeKey, preview)
		var err error
		states, err = s.conversationStates(ctx, key, ids, threadSignalsFor(rows))
		if err != nil {
			return nil, err
		}
	}
	h := &topHeap{}
	topicRollups := map[string]*topicRollup{}
	rows.each(func(row *rankedRow) {
		rs := row.signals
		item, include := scoreRowSignals(rowInput(row, now, states[row.conversationID]), rs, row.received, row.dated, false)
		if rs.sig.Subject.Topic != "" {
			rollup := topicRollups[rs.sig.Subject.Topic]
			if rollup == nil {
				rollup = &topicRollup{topic: rs.sig.Subject.Topic}
				topicRollups[rs.sig.Subject.Topic] = rollup
			}
			rollup.add(row.messageID, row.received, rs.sig.Subject.Subtopic)
		}
		if !include {
			return
		}
		for _, f := range Filters {
			if f.matches(item) {
				c.counts[f]++
			}
		}
		h.add(rankedItem{item: item, received: row.received, revision: row.revision})
	})
	scanned := rows.len()
	slices.SortFunc(h.items, rankLess)
	if err := s.fillSnippets(ctx, h.items); err != nil {
		return nil, err
	}
	c.items = h.items
	c.states = states
	c.topics = topicCensusFromRollups(topicRollups, scanned)
	// The snapshot becomes usable now, not at the instant the build began:
	// freshness is the time the owner can rely on it, not the age of the
	// work that produced it.
	c.at = s.clock.Now()
	c.build = c.at.Sub(start)
	return c, nil
}

// rankedRow is the one record the scan retains for scoring: the row metadata
// and the decoded classifier evidence. Route and preview answer maps stay out:
// their signals are already computed, and keeping the maps would retain tens
// of megabytes on a large mailbox.
type rankedRow struct {
	messageID      int64
	revision       int64
	conversationID string
	subject        string
	senderName     string
	senderAddress  string
	receivedAt     string
	received       time.Time
	dated          bool
	folderRole     mail.FolderRole
	hasAttachments bool
	storedDue      bool
	signals        rowSignals
}

// rowBlockRows bounds one block of the scan buffer. The mailbox size is not
// known when the scan starts, so the build cannot size its retention once;
// growing a single slice of these records would copy several mailbox-sizes of
// bytes through repeated reallocations. Fixed blocks allocate each record
// once and leave at most one partly-filled block for a small mailbox.
const rowBlockRows = 256

// rowBuffer is the build's per-row retention: one record per owner message,
// appended in fixed blocks and read back in scan order.
type rowBuffer struct {
	blocks [][]rankedRow
	n      int
}

func (b *rowBuffer) add(row rankedRow) {
	if len(b.blocks) == 0 || len(b.blocks[len(b.blocks)-1]) == rowBlockRows {
		b.blocks = append(b.blocks, make([]rankedRow, 0, rowBlockRows))
	}
	last := len(b.blocks) - 1
	b.blocks[last] = append(b.blocks[last], row)
	b.n++
}

func (b *rowBuffer) len() int { return b.n }

// rowInput is the pure scorer's view of one scan record. The build and its
// benchmark both score through it, so a field added to the row cannot drift
// out of the measured path.
func rowInput(row *rankedRow, now time.Time, thread threadintel.Assessment) Input {
	return Input{
		Message: Message{MessageID: row.messageID, ConversationID: row.conversationID, Subject: row.subject,
			SenderName: row.senderName, SenderAddress: row.senderAddress, ReceivedAt: row.receivedAt,
			FolderRole: row.folderRole, HasAttachments: row.hasAttachments, StoredDue: row.storedDue, Revision: row.revision},
		Now: now, Thread: thread,
	}
}

// each visits every row once, in the order the store streamed it.
func (b *rowBuffer) each(fn func(*rankedRow)) {
	for _, block := range b.blocks {
		for i := range block {
			fn(&block[i])
		}
	}
}

// threadSignalsFor is the thread resolver's view of one scan. It is built
// immediately before the one resolution call and dropped when that call
// returns: the ranking pass reads the same evidence from the row itself, so
// the copy is not retained.
func threadSignalsFor(rows *rowBuffer) map[int64]threadintel.Signal {
	signals := make(map[int64]threadintel.Signal, rows.len())
	rows.each(func(row *rankedRow) {
		rs := &row.signals
		signals[row.messageID] = threadintel.Signal{
			ReplyNeeded: rs.wantsReply,
			IsQuestion:  rs.question,
			AsksFor:     rs.sig.AsksFor,
			Topic:       rs.sig.Subject.Topic,
			Subtopic:    rs.sig.Subject.Subtopic,
			// The thread veto is the same bulk predicate as the action one, so
			// a suspicious message inside a bulk category is not hidden from
			// the action classifier as an automated row.
			Automated: rs.bulk,
		}
	})
	return signals
}

// replySignals is pass 1: the conversations and the per-message classifier
// evidence of every owner row, from the stored answers and without reading
// any body. The thread resolver needs the directed reply answer, the wider
// question signal, the dominant ask and the subject path; the ranking pass
// reuses the same decoded evidence, so each row is visited and interpreted
// exactly once.
func (s *Service) replySignals(ctx context.Context, profile *taxonomy.Profile, routeKey string, preview bool) (*rowBuffer, map[string]bool, error) {
	conversations := map[string]bool{}
	rows := &rowBuffer{}
	keys := neededQuestions(profile)
	// One scratch answer map for the whole scan: each row's merge clears and
	// refills it, so the build does not allocate a map per message.
	answers := make(map[string]engine.Answer, len(keys))
	err := s.store.EachPriorityReply(ctx, routeKey, preview, keys, func(row store.PriorityRow) error {
		received, dated := mail.Parse(row.ReceivedAt)
		var pv map[string]engine.Answer
		if preview {
			pv = row.Preview
		}
		rs, err := signalsForKeysInto(profile, keys, row.Route, pv, answers)
		if err != nil {
			return err
		}
		rows.add(rankedRow{
			messageID: row.MessageID, revision: row.Revision, conversationID: row.ConversationID,
			subject: row.Subject, senderName: row.SenderName, senderAddress: row.SenderAddress,
			receivedAt: row.ReceivedAt, received: received, dated: dated,
			folderRole: row.FolderRole, hasAttachments: row.HasAttachments, storedDue: row.StoredDue, signals: rs,
		})
		if row.ConversationID != "" {
			conversations[row.ConversationID] = true
		}
		return nil
	})
	return rows, conversations, err
}

// fillSnippets fills the cached items' snippets from one bounded body-head
// batch. The scoring pass reads no body; this is the only common-path body
// read, and it is capped by MaxCached. An absent head or a revision that no
// longer matches the scored row leaves the snippet empty (never a zero-filled
// body). A truncated head that cannot fill the snippet (a body that begins
// with more whitespace than the projection) reads that one full body, so the
// snippet is always the first snippetRunes non-space runes when the body has
// them. Any batch or fallback error fails the build before a partial cache is
// installed.
func (s *Service) fillSnippets(ctx context.Context, items []rankedItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	seen := make(map[int64]bool, len(items))
	for _, r := range items {
		if seen[r.item.MessageID] {
			continue
		}
		seen[r.item.MessageID] = true
		ids = append(ids, r.item.MessageID)
	}
	heads, err := s.store.PriorityHeads(ctx, ids)
	if err != nil {
		return fmt.Errorf("priority: read cached body heads: %w", err)
	}
	for i := range items {
		r := &items[i]
		head, ok := heads[r.item.MessageID]
		if !ok || head.Revision != r.revision {
			r.item.Snippet = ""
			continue
		}
		snippet := Snippet(head.Body, snippetRunes)
		if head.Truncated && utf8.RuneCountInString(snippet) < snippetRunes {
			body, err := s.store.MessageBody(ctx, r.item.MessageID)
			if err != nil {
				return fmt.Errorf("priority: read full body of message %d: %w", r.item.MessageID, err)
			}
			snippet = Snippet(body, snippetRunes)
		}
		r.item.Snippet = snippet
	}
	return nil
}

// conversationStates resolves the cross-store state of every conversation
// the ranking contains. The resolution is reused while the mail and the
// stored answers are unchanged: it reads every message of those
// conversations and is the build's dominant cost, yet nothing in it moves
// until a message, a route answer or the profile does.
func (s *Service) conversationStates(ctx context.Context, key string, ids []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	s.threadMu.Lock()
	if s.threadKey == key && s.threadStates != nil {
		states := s.threadStates
		s.threadMu.Unlock()
		return states, nil
	}
	s.threadMu.Unlock()
	states, err := s.threads.ThreadStates(ctx, ids, signals)
	if err != nil {
		return nil, err
	}
	s.threadMu.Lock()
	s.threadKey, s.threadStates = key, states
	s.threadMu.Unlock()
	return states, nil
}
