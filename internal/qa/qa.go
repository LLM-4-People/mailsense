// Package qa is the gated, owner-only question answering service: common
// questions are answered deterministically from the priority ranking. A
// free-form question that asks about one known organization is answered from
// that organization's cached profile; one that merely names an organization
// is answered from that organization's own mail, and other free-form
// questions get a written answer grounded in numbered owner messages with
// validated citations. A question with no owner search hit returns an
// explicit no-match retrieval. A missing provider, a warming index, a refused
// budget or a failed call degrades to an explicit retrieval answer with a
// notice that names its cause; it never fabricates one.
package qa

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/textx"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/world"
)

// Answer modes and the retrieval notice codes.
const (
	ModeStructured = "structured"
	ModeGenerated  = "generated"
	ModeRetrieval  = "retrieval"
	ModeComputed   = "computed"
	ModeClarify    = "clarify"

	// NoticeUnavailable means no answer generator is configured: qa.provider
	// is empty or its record/key cannot build a generator. It is the only
	// notice that may send the owner to provider settings.
	NoticeUnavailable = "qa_unavailable"
	// NoticeIndexWarming means the semantic index is genuinely incomplete for
	// eligible mail: some messages have no vector yet. It is transient.
	NoticeIndexWarming = "qa_index_warming"
	// NoticeRetrievalTimeout means the local search missed its budget while
	// the index itself is complete. It is transient too, but it is not
	// "warming": the mailbox is indexed and the next ask normally succeeds.
	NoticeRetrievalTimeout = "qa_retrieval_timeout"
	// NoticeGenerationFailed means a configured provider was called but the
	// call failed or its output did not satisfy the planner contract.
	NoticeGenerationFailed = "qa_generation_failed"
	// NoticeNoGroundedAnswer means the provider answered but no answer or
	// citation survived grounding in the retrieved messages.
	NoticeNoGroundedAnswer = "qa_no_grounded_answer"
	// NoticeAnswerLanguage means the grounded written answer is in another
	// language than the owner requested. It is notice-only: the answer,
	// citations and provider/model stay exactly as generated, and the
	// answer is saved to history but never cached, so a re-ask calls the
	// provider again.
	NoticeAnswerLanguage = "answer_language"
	// NoticeNoMatches means the mailbox has no evidence for the question.
	NoticeNoMatches = "no_matches"
	// NoticeSenderUnknown means a sender question named a person or address
	// that the mailbox cannot resolve, so no message can be attributed to
	// them. It is not a no-match: the sender itself is unknown.
	NoticeSenderUnknown = "qa_sender_unknown"
	// NoticeCompanyUnavailable means the organization profile could not be
	// read; the company shortcut never substitutes unrelated mail.
	NoticeCompanyUnavailable = "company_profile_unavailable"
	// NoticeLocalNoAnswer means the Local route retrieved sources but has no
	// local written-answer model. It is not a provider failure: the owner
	// explicitly asked to stay on this machine.
	NoticeLocalNoAnswer = "qa_local_no_answer"
)

// Question and history bounds.
const (
	MinQuestionRunes   = 3
	MaxQuestionRunes   = 500
	DefaultHistorySize = 20
	MaxHistorySize     = store.QAHistoryMax

	// The two answer languages. LanguageEN and LanguageFR are the one
	// vocabulary of the validator, the wire error and the prompt.
	LanguageEN      = "en"
	LanguageFR      = "fr"
	DefaultLanguage = LanguageFR

	// MaxConversationTurns is how many prior turns of one thread the
	// planner sees. MaxConversationTurnRunes bounds each question or answer
	// in that block, so a long turn cannot crowd out the numbered messages.
	MaxConversationTurns     = 6
	MaxConversationTurnRunes = 400
)

// validLanguage reports whether language is one of the two answer languages.
// The request boundary and the generated-answer language check both ask it, so
// a third language cannot be accepted in one place and refused in the other.
func validLanguage(language string) bool {
	return language == LanguageEN || language == LanguageFR
}

// isFrench reports whether an already validated answer language selects the
// French copy. The validator admits only LanguageEN and LanguageFR, so this is
// the one predicate every answer builder asks: a literal comparison repeated
// per branch is how one copy drifts when the vocabulary changes.
func isFrench(language string) bool { return language == LanguageFR }

// retrievalNotice is the one mapping from a retrieval sentinel to the notice
// and its stable reason. Every planner-context caller (the freeform answer, the
// local-only path and the remote-scope path) asks it, so the three cannot
// disagree about whether a slow index is "warming" or "timed out".
func retrievalNotice(err error) (notice, reason string, ok bool) {
	switch {
	case errors.Is(err, errSearchTimeout):
		return NoticeRetrievalTimeout, "retrieval_timeout", true
	case errors.Is(err, errSearchWarming):
		return NoticeIndexWarming, "semantic_index_warming", true
	}
	return "", "", false
}

var (
	// ErrInvalidQuestion is a question outside MinQuestionRunes..MaxQuestionRunes.
	ErrInvalidQuestion = errors.New("qa: invalid question")
	// ErrInvalidLanguage is a language other than en or fr.
	ErrInvalidLanguage = errors.New("qa: invalid language")
	// ErrInvalidConversation is a conversation id outside the safe opaque
	// alphabet or longer than store.QAConversationIDMax.
	ErrInvalidConversation = errors.New("qa: invalid conversation id")
	// ErrDisabled is qa.enabled off.
	ErrDisabled = errors.New("qa: disabled")
	// ErrUnavailable is a missing key, an unusable provider or a refused call.
	ErrUnavailable = errors.New("qa: generator unavailable")
	// ErrInvalidHistoryID is a history id outside the positive range.
	ErrInvalidHistoryID = errors.New("qa: invalid history id")
)

// Citation is one source of an answer, rehydrated from owner mail.
type Citation struct {
	MessageID  int64  `json:"message_id"`
	Subject    string `json:"subject"`
	SenderName string `json:"sender_name"`
	ReceivedAt string `json:"received_at"`
	Reason     string `json:"reason,omitempty"`
}

// AskView is one answer (the POST response and history item).
type AskView struct {
	ID             int64                    `json:"id"`
	ConversationID string                   `json:"conversation_id"`
	Question       string                   `json:"question"`
	Answer         string                   `json:"answer"`
	Route          Route                    `json:"route"`
	Mode           string                   `json:"mode"`
	Intent         string                   `json:"intent"`
	Counts         map[string]int           `json:"counts"`
	Citations      []Citation               `json:"citations"`
	Provider       string                   `json:"provider"`
	Model          string                   `json:"model"`
	TookMS         int64                    `json:"took_ms"`
	Preview        bool                     `json:"preview"`
	CreatedAt      string                   `json:"created_at"`
	Notice         string                   `json:"notice,omitempty"`
	Plan           *mailquery.Plan          `json:"plan,omitempty"`
	Result         *mailquery.Result        `json:"result,omitempty"`
	Agenda         *agenda.Result           `json:"agenda,omitempty"`
	Clarify        *mailquery.Clarification `json:"clarify,omitempty"`
	// Topic is the structured topic-count answer: the catalogue path it
	// counted, the count and the classification coverage behind it.
	Topic *TopicAnswer `json:"topic,omitempty"`
}

// HistoryRow is one rail row of the stored Q&A history: exactly the fields
// the Ask rail and the Home recent-questions list render. HistoryItem reads
// the full answer when the owner opens the row.
type HistoryRow struct {
	ID        int64  `json:"id"`
	Question  string `json:"question"`
	Mode      string `json:"mode"`
	CreatedAt string `json:"created_at"`
}

// Generator is the one chat call Q&A uses (engine.Generator).
type Generator interface {
	Name() string
	Model() string
	Generate(ctx context.Context, system, user string, opts ...engine.GenerateOptions) (engine.Generation, error)
}

// GeneratorSource builds the configured generator or returns ErrUnavailable.
type GeneratorSource interface {
	Generator() (Generator, error)
}

// Priority is the priority ranking (priority.Service).
type Priority interface {
	Rank(ctx context.Context, filter priority.Filter, limit int) ([]priority.Item, int, bool, error)
	Top(ctx context.Context, n int) ([]priority.Item, error)
	// ThreadStates is the same classifier-backed conversation truth the
	// priority, agenda and company screens show. Ask must not re-resolve a
	// thread with a nil message signal and then contradict those screens.
	ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error)
	// Topics is the whole-mailbox classified-topic census, built during the
	// same read as the ranking. A topic count is answered from it, never
	// from a second mailbox scan; a failed read is an error, never a zero.
	Topics(ctx context.Context) (priority.TopicCensus, error)
}

// Searcher is owner-only retrieval (search.Searcher).
type Searcher interface {
	Search(ctx context.Context, q search.Query) (search.Result, error)
}

// CompanyProfiles is the visible organization book and one organization's
// deep profile (world.Service). It is optional: without it, a question that
// names an organization follows the general planner.
type CompanyProfiles interface {
	List(ctx context.Context, q world.ListQuery) (world.ListResult, error)
	Profile(ctx context.Context, key string, months, eventDays int) (world.ProfileView, error)
}

// Agenda is the calendar-linked "next X" view (agenda.Service).
type Agenda interface {
	Build(ctx context.Context, horizon time.Duration) (agenda.Result, error)
	DefaultHorizon() time.Duration
}

// Gate is the app-wide learning gate. A gate that also implements
// learning.Previewer supplies the one preview rule; a gate that does not is
// preview off.
type Gate interface {
	Allow(ctx context.Context) error
}

// Store is the persisted history, the local plan cache, the owner
// summaries and stored context of cited mail, the conversation scope of a
// retrieved hit and the typed mail-query executor.
type Store interface {
	SaveQA(ctx context.Context, rec store.QAHistory) (store.QAHistory, error)
	ListQA(ctx context.Context, limit int) ([]store.QAHistory, error)
	ListQASummaries(ctx context.Context, limit int) ([]store.QAHistorySummary, error)
	GetQA(ctx context.Context, id int64) (store.QAHistory, bool, error)
	ListQATurns(ctx context.Context, conversationID string, limit int) ([]store.QAHistory, error)
	ClearQA(ctx context.Context) (int64, error)
	SummariesOwner(ctx context.Context, ids []int64) (map[int64]store.Summary, error)
	OwnerIdentity(ctx context.Context) (store.OwnerIdentity, error)
	ContextDetails(ctx context.Context, ids []int64) (map[int64]store.ContextDetail, error)
	ThreadMessages(ctx context.Context, conversationID string, limit int, beforeID int64) (store.ThreadMessagesResult, error)
	ThreadStates(ctx context.Context, conversationIDs []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error)
	PersonCandidates(ctx context.Context, terms []string, limit int) ([]store.PersonCandidate, error)
	LatestThreadForAddresses(ctx context.Context, addresses []string) (string, int64, error)
	GetQAPlan(ctx context.Context, key string) (mailquery.Plan, bool, error)
	SaveQAPlan(ctx context.Context, key, language string, plan mailquery.Plan) error
	ExecuteMailQuery(ctx context.Context, plan mailquery.Plan, loc *time.Location) (mailquery.Result, error)
	// Generation counts the writes that change what an answer reads. An
	// answer cached under an older generation is never reused.
	Generation() uint64
	// RouteGeneration counts route-answer commits. A layer-1 re-score
	// rewrites the certified answers, which a freeform answer cites and
	// ranks, without touching mail or labels; the mail generation alone
	// would serve an answer built on the replaced route for its whole TTL.
	RouteGeneration() uint64
}

// Deps are the Q&A collaborators. Generator may be nil (free-form questions
// then answer in retrieval mode); the rest are required. Companies is
// optional: nil means a question that names an organization goes through the
// general planner. A late wiring point may inject it with SetCompanies.
type Deps struct {
	Store     Store
	Priority  Priority
	Search    Searcher
	Agenda    Agenda
	Gate      Gate
	Clock     clock.Clock
	Generator GeneratorSource
	Companies CompanyProfiles
}

// Service is the gated Q&A service.
type Service struct {
	cfg    *config.Config
	store  Store
	prio   Priority
	search Searcher
	agenda Agenda
	gate   Gate
	clock  clock.Clock
	source GeneratorSource
	log    *slog.Logger

	// companiesMu guards the optional organization book. It is injected at
	// construction or by SetCompanies before (or during) serving, so a
	// concurrent Ask sees either the complete service or no shortcut.
	companiesMu sync.RWMutex
	companies   CompanyProfiles

	// answersMu guards the in-memory freeform answer cache. It holds only
	// successful answers from the IntentFreeform path (generated or computed,
	// including the company shortcut), never structured or sender views.
	answersMu sync.Mutex
	answers   map[string]cachedAnswer

	// plannerMu guards the planner cache identity (the selected record's
	// fingerprint), rebuilt when the settings snapshot or providers file
	// changes.
	plannerMu  sync.Mutex
	plannerKey string
	plannerID  string
}

// maxCachedAnswers bounds the freeform answer cache. When it is full, the
// entry with the oldest insertion time is evicted; a linear scan of at most
// 256 entries is cheaper than maintaining a second index.
const maxCachedAnswers = 256

// answerCacheTTL bounds how long a computed answer is replayed. The mail
// generation only moves on a write, so without a bound "how many this month"
// answered at 23:59 is replayed the next day; a date-relative plan is
// recomputed from the current clock on every miss.
const answerCacheTTL = 10 * time.Minute

// qaSaveTimeout bounds the history write of a completed answer. The write is
// detached from the request context because a browser refresh or the caller's
// own deadline must not discard an answer the owner already paid for; the
// bound only keeps a stuck writer from holding the request open.
const qaSaveTimeout = 5 * time.Second

// cachedAnswer is one successful freeform view. The generation and preview
// are part of the lookup key; they are kept here so the entry is self
// describing. at orders entries for the simple oldest-first eviction.
type cachedAnswer struct {
	view    AskView
	gen     uint64
	preview bool
	at      time.Time
}

// New returns a Q&A service.
func New(cfg *config.Config, d Deps) (*Service, error) {
	if cfg == nil || d.Store == nil || d.Priority == nil || d.Search == nil || d.Gate == nil {
		return nil, errors.New("qa: a config, store, priority, search and gate are required")
	}
	return &Service{cfg: cfg, store: d.Store, prio: d.Priority, search: d.Search, agenda: d.Agenda, gate: d.Gate,
		clock: clock.Or(d.Clock), source: d.Generator, companies: d.Companies, log: logx.For("qa"),
		answers: map[string]cachedAnswer{}}, nil
}

// SetCompanies injects the visible organization book after construction. The
// production wiring builds Q&A before the API world service; this is the one
// late binding that lets both share the same cached company views.
func (s *Service) SetCompanies(c CompanyProfiles) {
	s.companiesMu.Lock()
	s.companies = c
	s.companiesMu.Unlock()
}

// companyProfiles returns the injected organization book, or nil when the
// shortcut is disabled.
func (s *Service) companyProfiles() CompanyProfiles {
	s.companiesMu.RLock()
	defer s.companiesMu.RUnlock()
	return s.companies
}

// previewAnswers reports the one preview rule. A gate that does not
// implement learning.Previewer is preview off.
func (s *Service) previewAnswers(ctx context.Context) bool {
	g, ok := s.gate.(learning.Previewer)
	return ok && g.PreviewAnswers(ctx)
}

// AskRequest is one question, its language, the thread it belongs to and the
// explicit answer route. An empty route is Auto, so an older client keeps the
// behavior it already had.
type AskRequest struct {
	Question       string
	Language       string
	ConversationID string
	Route          Route
}

// Ask answers one standalone question in a new conversation. It validates at
// the trust boundary, uses the gate, and persists every successful answer
// (structured, generated, computed or retrieval) before returning it. The
// generated conversation id is returned to the caller, but no prior turn is
// read: this is the single-turn entry point, and its caches stay the
// context-free keys an older client already populated.
func (s *Service) Ask(ctx context.Context, question, language string) (AskView, error) {
	return s.AskConversation(ctx, AskRequest{Question: question, Language: language})
}

// AskConversation answers one question in a short chat thread. An empty
// conversation id starts a new thread and generates a random 16-hex-character
// id that is returned and persisted with the turn. A non-empty id is
// validated, and the newest turns of that same conversation are the
// planner's chat context: a follow-up resolves against them, while every
// factual claim still has to cite the numbered owner messages. A new thread
// has no prior context, so its first turn may reuse the single-turn cache;
// a continuation is cached under its conversation id and newest prior turn
// id, so a cached answer never crosses threads.
//
// Route is per turn. Local never calls a generator; Remote resolves the one
// configured qa.provider before any branch and never falls back to a local
// answer; Auto keeps the historical computed-or-provider behavior.
func (s *Service) AskConversation(ctx context.Context, req AskRequest) (AskView, error) {
	question, language, conversationID := req.Question, req.Language, req.ConversationID
	q := strings.TrimSpace(question)
	// A question that arrives double-encoded ("RÃ©sume") is repaired once, at
	// the one boundary every question crosses, before it is validated,
	// searched or saved. Repair is conservative (only unambiguous
	// UTF-8-read-as-Windows-1252 sequences change) and idempotent.
	if repaired, n := textx.Repair(q); n > 0 {
		s.log.Info("qa question encoding repaired", "sequences", n)
		q = repaired
	}
	if n := utf8.RuneCountInString(q); n < MinQuestionRunes || n > MaxQuestionRunes {
		return AskView{}, ErrInvalidQuestion
	}
	lang := strings.ToLower(strings.TrimSpace(language))
	if lang == "" {
		lang = DefaultLanguage
	}
	if !validLanguage(lang) {
		return AskView{}, ErrInvalidLanguage
	}
	conversation, ok := store.NormalizeQAConversationID(conversationID)
	if !ok {
		return AskView{}, ErrInvalidConversation
	}
	route, err := ParseRoute(string(req.Route))
	if err != nil {
		return AskView{}, err
	}
	if !s.cfg.Bool(SettingEnabled) {
		return AskView{}, ErrDisabled
	}
	if err := s.gate.Allow(ctx); err != nil {
		return AskView{}, err
	}
	continueConversation := conversation != ""
	if !continueConversation {
		generated, err := newConversationID()
		if err != nil {
			return AskView{}, err
		}
		conversation = generated
	}
	start := s.clock.Now()
	preview := s.previewAnswers(ctx)
	view := AskView{ConversationID: conversation, Question: q, Route: route, Counts: map[string]int{},
		Citations: []Citation{}, Preview: preview}
	intent, senderText := parseIntent(q)
	view.Intent = intent

	// Remote is resolved before any branch. A missing or unusable generator
	// is a typed availability error: the owner chose the remote model and
	// must never receive a quiet local answer instead.
	var remoteGen Generator
	if route == RouteRemote {
		if s.source == nil {
			return AskView{}, &RouteUnavailableError{Route: route, Reason: "unavailable"}
		}
		remoteGen, err = s.source.Generator()
		if err != nil {
			s.log.Warn("qa remote route unavailable", "reason", errorClass(err))
			return AskView{}, &RouteUnavailableError{Route: route, Reason: remoteReason(err), Err: err}
		}
	}

	// Only a continuation reads history. A brand-new thread is empty by
	// construction, and reading it would add a database round trip without
	// context. A failed read is an error, never an invented empty thread.
	// Local has no chat model, so it never pays for prior turns.
	var turns []store.QAHistory
	turnKey := ""
	if continueConversation && route != RouteLocal {
		var err error
		turns, err = s.store.ListQATurns(ctx, conversation, MaxConversationTurns)
		if err != nil {
			return AskView{}, err
		}
		turnKey = conversationTurnKey(conversation, turns)
	}

	switch route {
	case RouteLocal:
		view, err = s.answerLocal(ctx, view, q, lang, intent, senderText)
	case RouteRemote:
		view, err = s.answerRemote(ctx, view, q, lang, intent, senderText, turns, remoteGen)
	default:
		// The provider-less count gate is owned here, before parseIntent's
		// category switch. A count cue can parse directly to a category
		// ("Number of deadlines", "Total deadlines", "Combien d'échéances"),
		// so gating only the IntentFreeform branch would let a scoped or
		// person-framed count reach askStructured for a mailbox-wide total.
		// One gate handles every count request; a matching plain count is
		// answered structurally from the priority totals or the topic
		// census, and every other count is forced onto the freeform path. A
		// configured generator keeps the existing computed path and never
		// enters this gate.
		providerlessCount := false
		folded := mail.Fold(strings.TrimSpace(q))
		if aggregateCountRequest(folded) && !s.generatorAvailable() {
			providerlessCount = true
			intent, phrase, ok := s.providerlessCountIntent(ctx, q)
			switch {
			case ok && intent == IntentTopic:
				view.Intent = IntentTopic
				view, err = s.askTopic(ctx, view, phrase)
			case ok:
				view.Intent = intent
				view, err = s.askStructured(ctx, view, intent)
			default:
				view.Intent = IntentFreeform
				view, err = s.askFreeform(ctx, view, q, lang, preview, turns, turnKey)
			}
		}
		if !providerlessCount {
			view, err = s.answerAuto(ctx, view, q, lang, preview, intent, senderText, turns, turnKey)
		}
	}
	if err != nil {
		return AskView{}, err
	}
	if view.Citations == nil {
		view.Citations = []Citation{}
	}
	if view.Counts == nil {
		view.Counts = map[string]int{}
	}
	view.TookMS = s.clock.Now().Sub(start).Milliseconds()
	if transientRetrieval(view.Notice) {
		// A retrieval failure is not an answer. Saving it would put a
		// permanent "index is preparing" card in history, where it replays
		// long after the index is healthy (the live defect). The turn stays
		// in the open screen; only a real answer is persisted.
		return view, nil
	}
	// safe.Detached owns the detached, bounded write context: the answer was
	// already paid for, so a request that ends now must not discard it, and a
	// panic in the store reaches the caller as an ordinary error instead of
	// aborting the response mid-write.
	saved, err := safe.Detached(ctx, nil, qaSaveTimeout, func(saveCtx context.Context) (store.QAHistory, error) {
		return s.store.SaveQA(saveCtx, store.QAHistory{
			ConversationID: conversation, Route: string(view.Route),
			Question: q, Answer: view.Answer, Mode: view.Mode, Intent: view.Intent, Counts: view.Counts,
			Citations: qaCitations(view.Citations), Provider: view.Provider, Model: view.Model,
			Language: lang, Preview: view.Preview, Plan: view.Plan, Result: view.Result,
			Agenda: view.Agenda, Clarify: view.Clarify, Notice: view.Notice, Topic: qaTopicAnswer(view.Topic),
		})
	})
	if err != nil {
		return AskView{}, err
	}
	view.ID = saved.ID
	view.CreatedAt = saved.CreatedAt
	return view, nil
}

// transientRetrieval reports a notice that describes a retrieval that could
// not run, not an answer. It is never persisted to history.
func transientRetrieval(notice string) bool {
	return notice == NoticeIndexWarming || notice == NoticeRetrievalTimeout
}

// newConversationID returns 8 random bytes as 16 lowercase hex characters.
// The id is opaque and local: it groups Q&A turns and scopes their caches; it
// is never a mailbox conversation id and carries no content.
func newConversationID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("qa: generate conversation id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// answerCacheKey names one freeform answer by the planner that produced it,
// its normalized question, the requested language, the store generation, the
// preview switch, the owner-local day and the zone. A continuation also
// carries its turn key, so an answer cached with one thread's context is
// never served to another. The planner is part of the key: a plan or answer
// chosen by one model is never reused after the owner switches provider.
// The day and zone are part of the key because the prompt embeds both: a
// "today" answer from yesterday, or from another zone, is not the same
// answer.
func answerCacheKey(planner, language, question string, gen, routeGen uint64, preview bool, turnKey, day, zone string) string {
	return scopedQuestionKey(plannerVersion, planner, language, question, turnKey) +
		"|" + strconv.FormatUint(gen, 10) + "|" + strconv.FormatUint(routeGen, 10) +
		"|" + strconv.FormatBool(preview) + "|" + day + "|" + zone
}

// degradedAnswer reports an answer that must not be cached: a provider,
// contract or index fallback is retried on the next ask instead of being
// frozen until the mail generation moves. A no-match retrieval is
// deterministic for the current generation but is also excluded from the
// cache, so every retrieval view stays non-cacheable. A wrong-language
// answer is a real answer that is shown and saved to history, but it is
// not cached: asking again must call the provider again rather than replay
// a notice.
func degradedAnswer(view AskView) bool {
	return view.Notice == NoticeCompanyUnavailable || view.Notice == NoticeAnswerLanguage ||
		view.Mode == ModeRetrieval
}

// emptyPlanResult reports a computed message list with no rows. Such a view
// is not final and not cacheable: a stale text filter may have hidden the
// messages the question needs, so the next ask re-plans with fresh
// retrieval instead of freezing a wrong no-match.
func emptyPlanResult(view AskView) bool {
	if view.Plan == nil || view.Plan.Kind != mailquery.KindMessages || view.Plan.Action != mailquery.ActionList {
		return false
	}
	return view.Counts["total"] == 0
}

// cacheableAnswer reports an answer safe to keep in the in-memory cache. A
// retrieval fallback is never cached: the semantic index may still be
// warming, and a transient empty search must not freeze the question as
// unanswered.
func cacheableAnswer(view AskView) bool {
	return !degradedAnswer(view) && !emptyPlanResult(view)
}

// cachedAnswer returns a copy of one cached freeform answer. An expired
// entry is removed and reported as a miss, so the next ask re-executes the
// plan against the current clock.
func (s *Service) cachedAnswer(key string) (AskView, bool) {
	s.answersMu.Lock()
	defer s.answersMu.Unlock()
	entry, ok := s.answers[key]
	if !ok {
		return AskView{}, false
	}
	if s.clock.Now().Sub(entry.at) >= answerCacheTTL {
		delete(s.answers, key)
		return AskView{}, false
	}
	return cloneAskView(entry.view), true
}

// storeAnswer caches one successful freeform answer, evicting the oldest
// entry when the cache is full.
func (s *Service) storeAnswer(key string, view AskView, gen uint64, preview bool, at time.Time) {
	s.answersMu.Lock()
	defer s.answersMu.Unlock()
	if s.answers == nil {
		s.answers = make(map[string]cachedAnswer, maxCachedAnswers)
	}
	if _, ok := s.answers[key]; !ok && len(s.answers) >= maxCachedAnswers {
		oldestKey := ""
		var oldest cachedAnswer
		for k, entry := range s.answers {
			if oldestKey == "" || entry.at.Before(oldest.at) {
				oldestKey, oldest = k, entry
			}
		}
		delete(s.answers, oldestKey)
	}
	s.answers[key] = cachedAnswer{view: cloneAskView(view), gen: gen, preview: preview, at: at}
}

// cloneAskView returns a view the cache and its callers can own separately.
// Slices and maps are copied; the pointed-to plan, result, agenda and
// clarification are copied too, so a caller cannot mutate cached state.
func cloneAskView(view AskView) AskView {
	out := view
	if view.Counts != nil {
		out.Counts = make(map[string]int, len(view.Counts))
		for k, v := range view.Counts {
			out.Counts[k] = v
		}
	}
	if view.Citations != nil {
		out.Citations = make([]Citation, len(view.Citations))
		copy(out.Citations, view.Citations)
	}
	if view.Plan != nil {
		plan := *view.Plan
		if plan.Unread != nil {
			v := *plan.Unread
			plan.Unread = &v
		}
		if plan.HasAttachments != nil {
			v := *plan.HasAttachments
			plan.HasAttachments = &v
		}
		if plan.ContactAddresses != nil {
			plan.ContactAddresses = append([]string{}, plan.ContactAddresses...)
		}
		out.Plan = &plan
	}
	if view.Result != nil {
		result := *view.Result
		if view.Result.Rows != nil {
			result.Rows = make([]mailquery.Row, len(view.Result.Rows))
			copy(result.Rows, view.Result.Rows)
		}
		out.Result = &result
	}
	if view.Agenda != nil {
		res := *view.Agenda
		if view.Agenda.Items != nil {
			res.Items = make([]agenda.Item, len(view.Agenda.Items))
			copy(res.Items, view.Agenda.Items)
			for i := range res.Items {
				if res.Items[i].Reasons != nil {
					res.Items[i].Reasons = make([]agenda.Reason, len(res.Items[i].Reasons))
					copy(res.Items[i].Reasons, view.Agenda.Items[i].Reasons)
				}
			}
		}
		if view.Agenda.NextEvent != nil {
			next := *view.Agenda.NextEvent
			if next.Reasons != nil {
				next.Reasons = make([]agenda.Reason, len(next.Reasons))
				copy(next.Reasons, view.Agenda.NextEvent.Reasons)
			}
			res.NextEvent = &next
		}
		out.Agenda = &res
	}
	if view.Clarify != nil {
		clarify := *view.Clarify
		if view.Clarify.Options != nil {
			clarify.Options = make([]string, len(view.Clarify.Options))
			copy(clarify.Options, view.Clarify.Options)
		}
		if view.Clarify.Candidates != nil {
			clarify.Candidates = make([]mailquery.ClarifyCandidate, len(view.Clarify.Candidates))
			copy(clarify.Candidates, view.Clarify.Candidates)
		}
		out.Clarify = &clarify
	}
	if view.Topic != nil {
		topic := *view.Topic
		out.Topic = &topic
	}
	return out
}

// History returns the newest stored answers as rail rows. The full answer,
// citations and plan are read only when a row is opened (HistoryItem): a
// list mount must not pay for twenty answer payloads it never renders.
func (s *Service) History(ctx context.Context, limit int) ([]HistoryRow, error) {
	if limit < 1 || limit > MaxHistorySize {
		return nil, fmt.Errorf("qa: history limit must be between 1 and %d, got %d", MaxHistorySize, limit)
	}
	if err := s.gate.Allow(ctx); err != nil {
		return nil, err
	}
	rows, err := s.store.ListQASummaries(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryRow, len(rows))
	for i, row := range rows {
		out[i] = HistoryRow{ID: row.ID, Question: row.Question, Mode: row.Mode, CreatedAt: row.CreatedAt}
	}
	return out, nil
}

// HistoryItem returns one persisted answer with citations rehydrated from
// visible owner mail (a missing message keeps its id, empty fields).
func (s *Service) HistoryItem(ctx context.Context, id int64) (AskView, bool, error) {
	if id <= 0 {
		return AskView{}, false, ErrInvalidHistoryID
	}
	if err := s.gate.Allow(ctx); err != nil {
		return AskView{}, false, err
	}
	row, ok, err := s.store.GetQA(ctx, id)
	if err != nil || !ok {
		return AskView{}, ok, err
	}
	view, err := s.historyView(ctx, row)
	if err != nil {
		return AskView{}, false, err
	}
	return view, true, nil
}

// historyView rehydrates one stored row. It is the one place the legacy
// notice normalization and the citation lookup happen.
func (s *Service) historyView(ctx context.Context, row store.QAHistory) (AskView, error) {
	ids := make([]int64, 0, len(row.Citations))
	for _, c := range row.Citations {
		ids = append(ids, c.MessageID)
	}
	sums, err := s.store.SummariesOwner(ctx, ids)
	if err != nil {
		return AskView{}, err
	}
	route := Route(row.Route)
	if route == "" {
		route = RouteAuto
	}
	view := AskView{ID: row.ID, ConversationID: row.ConversationID, Question: row.Question, Answer: row.Answer,
		Route: route, Mode: row.Mode, Intent: row.Intent,
		Counts: nonNilCounts(row.Counts), Citations: make([]Citation, 0, len(row.Citations)),
		Provider: row.Provider, Model: row.Model, Preview: row.Preview, CreatedAt: row.CreatedAt,
		Plan: row.Plan, Result: row.Result, Agenda: row.Agenda, Clarify: row.Clarify, Notice: row.Notice,
		Topic: topicAnswerFromStore(row.Topic)}
	if row.Mode == ModeGenerated {
		if text, ok := unwrapAnswerEnvelope(row.Answer); ok {
			view.Answer = text
		} else if looksLikeJSON(trimJSONFence(row.Answer)) {
			// A legacy row whose envelope has no usable answer must not
			// display the raw JSON as prose.
			view.Answer = ""
			if view.Notice == "" {
				view.Notice = NoticeNoGroundedAnswer
			}
		}
	}
	if row.Mode == ModeRetrieval {
		switch {
		case view.Notice == "":
			// Older retrieval rows predate the stored notice: a row
			// without sources had no mailbox match, a row with a
			// provider was a failed generation, and one with sources but
			// no provider was unavailable.
			switch {
			case len(row.Citations) == 0:
				view.Notice = NoticeNoMatches
			case row.Provider != "" || row.Model != "":
				view.Notice = NoticeGenerationFailed
			default:
				view.Notice = NoticeUnavailable
			}
		case view.Notice == NoticeUnavailable && (row.Provider != "" || row.Model != ""):
			// Rows written before the notice split used qa_unavailable
			// for a configured provider whose call failed or whose
			// output was unusable. The stored provider identity proves a
			// provider was called, so this was a generation failure, not
			// missing settings.
			view.Notice = NoticeGenerationFailed
		case view.Notice == NoticeUnavailable && row.Provider == "" && row.Model == "" && len(row.Citations) == 0:
			// The same pre-split rows used qa_unavailable for a degraded
			// semantic search. With no provider identity and no
			// citations, the only honest reading is that retrieval did
			// not complete; it was never a warming index we can vouch
			// for now.
			view.Notice = NoticeRetrievalTimeout
		}
	}
	for _, c := range row.Citations {
		view.Citations = append(view.Citations, citationFor(c.MessageID, c.Reason, sums))
	}
	return view, nil
}

// ClearHistory removes every persisted answer.
func (s *Service) ClearHistory(ctx context.Context) error {
	if err := s.gate.Allow(ctx); err != nil {
		return err
	}
	_, err := s.store.ClearQA(ctx)
	return err
}

func qaCitations(in []Citation) []store.QACitation {
	out := make([]store.QACitation, 0, len(in))
	for _, c := range in {
		out = append(out, store.QACitation{MessageID: c.MessageID, Reason: c.Reason})
	}
	return out
}

// qaTopicAnswer projects the topic-count answer to its persisted shape. The
// store keeps the same field set under its own type: internal/store cannot
// import internal/qa, and neither owns a second fact.
func qaTopicAnswer(in *TopicAnswer) *store.QATopicAnswer {
	if in == nil {
		return nil
	}
	return &store.QATopicAnswer{Topic: in.Topic, Subtopic: in.Subtopic, Count: in.Count,
		Classified: in.Classified, Scanned: in.Scanned}
}

// topicAnswerFromStore rehydrates the persisted topic-count answer.
func topicAnswerFromStore(in *store.QATopicAnswer) *TopicAnswer {
	if in == nil {
		return nil
	}
	return &TopicAnswer{Topic: in.Topic, Subtopic: in.Subtopic, Count: in.Count,
		Classified: in.Classified, Scanned: in.Scanned}
}

func nonNilCounts(in map[string]int) map[string]int {
	if in == nil {
		return map[string]int{}
	}
	return in
}

func citationFor(id int64, reason string, sums map[int64]store.Summary) Citation {
	c := Citation{MessageID: id, Reason: reason}
	if sum, ok := sums[id]; ok {
		c.Subject, c.SenderName, c.ReceivedAt = sum.Subject, sum.SenderName, sum.Instant
	}
	return c
}

// plannerVersion invalidates every cached plan when the planner's contract
// changes. It is part of the question key, so a plan cached by an earlier
// planner (for example, a contact ranking for a company question) is never
// reused with different semantics.
const plannerVersion = 11

// plannerIdentity is the configured planner's cache identity: the selected
// record's fingerprint (protocol, endpoint, model, embedding spec). A model
// or endpoint change is a planner change, so plan and answer caches never
// carry across it. An unconfigured planner has no cacheable answer, so it
// gets one explicit namespace rather than a record id.
func (s *Service) plannerIdentity() string {
	id := strings.TrimSpace(s.cfg.String(SettingProvider))
	key := s.cfg.VersionKey(engine.ProvidersFileSetting) + "|" + id
	s.plannerMu.Lock()
	defer s.plannerMu.Unlock()
	if s.plannerKey == key {
		return s.plannerID
	}
	s.plannerKey, s.plannerID = key, "unavailable"
	if id == "" {
		return s.plannerID
	}
	cat, err := engine.LoadCatalog(s.cfg)
	if err != nil {
		return s.plannerID
	}
	if p, ok := cat.Get(id); ok {
		s.plannerID = p.Fingerprint()
	}
	return s.plannerID
}

// questionKey is the opaque cache key of one normalized question under one
// planner. The plan cache never stores the question itself, only this
// SHA-256.
func questionKey(planner, language, question string) string {
	return questionKeyFor(plannerVersion, planner, language, question)
}

// questionKeyFor is questionKey at an explicit planner version; the test
// uses it to prove that a version bump invalidates old entries.
func questionKeyFor(version int, planner, language, question string) string {
	return scopedQuestionKey(version, planner, language, question, "")
}

// planKey scopes one cached plan to one conversation turn. An empty turnKey
// is the context-free single-turn key; a continuation adds its conversation
// id and newest prior turn id, so one thread's plan is never executed for
// another.
func planKey(planner, language, question, turnKey string) string {
	return scopedQuestionKey(plannerVersion, planner, language, question, turnKey)
}

// conversationTurnKey names one continuation without storing any content:
// the service-generated conversation id plus the newest prior turn id. It is
// only called for an existing conversation; a brand-new thread has no prior
// turn and uses the context-free single-turn cache.
func conversationTurnKey(conversationID string, turns []store.QAHistory) string {
	newest := int64(0)
	if len(turns) > 0 {
		newest = turns[len(turns)-1].ID
	}
	return conversationID + ":" + strconv.FormatInt(newest, 10)
}

// scopedQuestionKey hashes the question and, for a continuation, the
// conversation turn key. The plan cache never stores the question itself,
// only this SHA-256.
func scopedQuestionKey(version int, planner, language, question, turnKey string) string {
	norm := strings.Join(strings.Fields(mail.Fold(strings.TrimSpace(question))), " ")
	input := strconv.Itoa(version) + "\x00" + planner + "\x00" + language + "\x00" + norm
	if turnKey != "" {
		input += "\x00" + turnKey
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// zone is the owner's confirmed time zone, or UTC when it is not set. The
// UI reports the fallback; the service never assumes the machine's zone.
func (s *Service) zone() *time.Location {
	name := strings.TrimSpace(s.cfg.String(clock.SettingZone))
	loc, err := clock.Zone(name)
	if err != nil {
		s.log.Warn("qa time zone unavailable; using UTC", "zone", name, "err", err)
		return time.UTC
	}
	return loc
}

// zoneName is the zone label the planner sees. An unconfirmed zone is
// explicit rather than silently replaced by the machine's local time.
func (s *Service) zoneName() string {
	name := strings.TrimSpace(s.cfg.String(clock.SettingZone))
	if name == "" {
		return "UTC (not confirmed)"
	}
	return name
}
