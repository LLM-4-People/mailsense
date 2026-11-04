// Package api is the loopback HTTP surface of the desktop app: a
// per-launch bearer token, JSON endpoints for the service layer, and an
// SSE stream for progress and logs. Handlers stay thin: they validate a
// request, make one service call and encode the result. The service
// structs are the wire schema; only the views below are API-owned.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/action"
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
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// Services are the collaborators of every handler. They are interfaces so
// the HTTP layer can be tested with fakes; the production adapter lives in
// cmd/mailsense.
type Services struct {
	Bus           *events.Bus
	Status        StatusAPI
	Resources     ResourcesAPI
	Learning      LearningAPI
	Jobs          JobsAPI
	HeldRemovals  HeldRemovalsAPI
	Mailboxes     MailboxesAPI
	Runs          RunsAPI
	Triage        TriageAPI
	Search        SearchAPI
	Settings      SettingsAPI
	Secrets       SecretsAPI
	Providers     ProvidersAPI
	Sidecar       SidecarAPI
	Logs          LogsAPI
	Labels        Layer1API
	Hub           HubAPI
	Messages      MessagesAPI
	Threads       ThreadAPI
	Conversations ConversationsAPI
	Commitments   CommitmentsAPI
	Brief         BriefAPI
	Priority      PriorityAPI
	Agenda        AgendaAPI
	Ask           AskAPI
	Companies     CompaniesAPI
	Shutdown      ShutdownAPI
	Boot          BootAPI
}

// Gate is the app-wide learning gate.
type Gate interface {
	Allow(ctx context.Context) error
}

// LearningAPI is the gate and its status query. Status and GateState report
// the real gate answer (never the uncertified test preview) and carry the one
// effective preview rule, not the raw learning.test_mode setting.
type LearningAPI interface {
	Gate
	// Status reads the full status together with the gate, so the learning
	// screen cannot paint a status from one state beside a gate from another.
	Status(ctx context.Context) (learning.Status, learning.GateState, error)
	GateState(ctx context.Context) learning.GateState
	ConfirmZone(ctx context.Context, zone string) error
}

// StatusAPI returns the service-owned part of the status view.
type StatusAPI interface {
	Status(ctx context.Context) (StatusView, error)
}

// ResourcesAPI reports the live host readings and the effective ceilings.
type ResourcesAPI interface {
	State(ctx context.Context) (resource.Status, error)
}

// JobsAPI is the jobs manager.
type JobsAPI interface {
	List() []jobs.Info
	Get(id string) (jobs.Info, bool)
	Cancel(id string) (jobs.Info, bool)
}

// HeldRemovalsAPI is the owner confirmation path for removals the syncer
// held back. Listing is a read; confirming is the syncer's delete owner, so
// the API never removes a message itself.
type HeldRemovalsAPI interface {
	Held(ctx context.Context, sourceID string) ([]HeldFolderView, error)
	Confirm(ctx context.Context, folderID int64) (store.Removal, error)
}

// MailboxesAPI owns discovery, selection and identity.
type MailboxesAPI interface {
	List(ctx context.Context) ([]store.MailboxView, error)
	Discover(ctx context.Context, sourceID string) ([]store.MailboxView, error)
	// The write methods return the mailbox views the change produced: the
	// same snapshot their service publishes, so the route does not read the
	// list again for its response.
	Pick(ctx context.Context, keys []string) ([]store.MailboxView, error)
	Unpick(ctx context.Context, keys []string) ([]store.MailboxView, error)
	Declare(ctx context.Context, key, address string) ([]store.MailboxView, error)
	Undeclare(ctx context.Context, key, address string) ([]store.MailboxView, error)
}

// RunsAPI starts learning work as jobs.
type RunsAPI interface {
	StartSync(ctx context.Context, sourceID string) (jobs.Info, error)
	StartEvidence(ctx context.Context) (jobs.Info, error)
	StartIndex(ctx context.Context, stores []string) (jobs.Info, error)
}

// TriageAPI is the gated triage read side. Questions is the one
// pre-gate read: the profile's typed questions, never mail content.
type TriageAPI interface {
	Page(ctx context.Context, q triage.PageQuery) (*triage.Page, error)
	Summary(ctx context.Context) (*triage.Summary, error)
	Result(ctx context.Context, rowID int64) (triage.Result, error)
	Questions(ctx context.Context) (QuestionsView, error)
}

// SearchAPI is the gated search.
type SearchAPI interface {
	Search(ctx context.Context, q search.Query) (search.Result, error)
}

// PriorityAPI is the gated owner-only important-mail ranking.
type PriorityAPI interface {
	Rank(ctx context.Context, filter priority.Filter, limit int) ([]priority.Item, int, bool, error)
	// RankPage returns the page and every filter's count from one snapshot.
	RankPage(ctx context.Context, filter priority.Filter, limit, offset int) (priority.Page, error)
}

// ThreadAPI is the gated owner-only conversation view. It returns the
// owner-visible messages plus the same resolved thread state priority and
// agenda show; shared/delegate content never crosses it. beforeID pages
// older mail exclusively; atID ends the page at one message inclusively.
type ThreadAPI interface {
	Get(ctx context.Context, conversationID string, limit int, beforeID, atID int64) (ThreadView, error)
}

// ConversationsAPI is the gated thread-first mailbox page: one digest row
// per conversation with its resolved state, participants and organization.
// Search, state filter and keyset pagination are service-owned.
type ConversationsAPI interface {
	Page(ctx context.Context, q conversations.Query) (conversations.Page, error)
}

// CommitmentsAPI is the gated deterministic open-loop read model: due
// follow-ups, conversations waiting on the owner and mail asking for a
// decision. The service owns evidence, dedupe, ordering and facet counts.
type CommitmentsAPI interface {
	List(ctx context.Context, f commitments.Filter) (commitments.Result, error)
}

// BriefAPI is the gated deterministic Brief: one composition of commitments,
// conversation facts and the priority ranking. The service owns every count,
// boundary and evidence id; the handler validates since only.
type BriefAPI interface {
	Brief(ctx context.Context, since string) (brief.Result, error)
}

// AgendaAPI is the calendar-linked "next X" view over the world model.
type AgendaAPI interface {
	Build(ctx context.Context, horizon time.Duration) (agenda.Result, error)
	DefaultHorizon() time.Duration
}

// AskAPI is the gated Q&A service and its local history.
type AskAPI interface {
	Ask(ctx context.Context, question, language string) (qa.AskView, error)
	AskConversation(ctx context.Context, req qa.AskRequest) (qa.AskView, error)
	History(ctx context.Context, limit int) ([]qa.HistoryRow, error)
	HistoryItem(ctx context.Context, id int64) (qa.AskView, bool, error)
	ClearHistory(ctx context.Context) error
}

// CompaniesAPI is the gated owner-only external-organization explorer. The
// world service owns the ranking, the deep profile and the authoritative
// thread overlay; the handler validates the request only.
type CompaniesAPI interface {
	List(ctx context.Context, q worldsvc.ListQuery) (worldsvc.ListResult, error)
	Profile(ctx context.Context, key string, months, eventDays int) (worldsvc.ProfileView, error)
	// Profiles reads several profiles in one store batch. It is the warm
	// path: five profiles must not scan the mailbox five times.
	Profiles(ctx context.Context, keys []string, months, eventDays int) (map[string]worldsvc.ProfileView, error)
}

// SettingsAPI is the configuration surface. Secret reports whether a
// setting is secret, so the error mapper can hide its value.
type SettingsAPI interface {
	List() ([]SettingView, []string)
	Get(key string) (SettingView, bool)
	Set(key string, raw json.RawMessage) (SettingView, error)
	Reset(key string) (SettingView, error)
	Reload() ([]string, error)
	Learned() []config.Learned
	Reject(key string, raw json.RawMessage) ([]config.Learned, error)
	Unreject(key string, raw json.RawMessage) ([]config.Learned, error)
	Secret(key string) bool
}

// SecretsAPI never returns a value.
type SecretsAPI interface {
	List() (SecretsView, error)
	Set(name, value string) error
	Delete(name string) error
}

// ProviderDiscoverRequest is the model-list discovery body: one base URL,
// an optional protocol (openai_chat when absent), and either a plaintext
// form key or the name of a stored secret.
type ProviderDiscoverRequest = engine.DiscoverRequest

// ProviderDiscoveryView is the normalized official model list. Models is
// always an array, sorted by id by the engine.
type ProviderDiscoveryView struct {
	Models []engine.ProviderModel `json:"models"`
}

// ProvidersAPI is the provider record surface: list, create, update,
// delete, the protocol-aware test and official model-list discovery.
// Every write validates the whole record set before it is persisted.
type ProvidersAPI interface {
	List(ctx context.Context) ([]ProviderInfo, error)
	Roles(ctx context.Context) ([]ProviderRoleInfo, error)
	Create(ctx context.Context, rec engine.Provider) (ProviderInfo, error)
	Update(ctx context.Context, id string, rec engine.Provider) (ProviderInfo, error)
	Delete(ctx context.Context, id string) error
	Test(ctx context.Context, id string) (engine.ProviderTestResult, error)
	Discover(ctx context.Context, req engine.DiscoverRequest) ([]engine.ProviderModel, error)
}

// SidecarAPI is the managed-runtime surface.
type SidecarAPI interface {
	Status() (SidecarStatus, error)
	Install(ctx context.Context, runtime, model, variant string) (jobs.Info, error)
	Verify(ctx context.Context, runtime, model, variant string) error
}

// LogQuery is one log read: a tail, or everything after Since.
type LogQuery struct {
	Since    uint64
	HasSince bool
	Limit    int
}

// LogsAPI is the log ring seam.
type LogsAPI interface {
	Snapshot(q LogQuery) (LogSnapshot, error)
	ReadLimit(seq uint64, limit int, newest bool) (entries []logx.Entry, oldest, last uint64)
	Bounds() (oldest, last uint64)
}

// MessagesAPI reads the owner mail list, one message for the labelling
// card, and starts the one-time display-HTML backfill of a source.
type MessagesAPI interface {
	List(ctx context.Context, q store.MessageListQuery) (MessageListView, error)
	Get(ctx context.Context, rowID int64) (MessageView, error)
	// Attachment returns one stored inline image. The handler keeps the
	// bytes out of every JSON surface; only the binary route calls it.
	Attachment(ctx context.Context, rowID int64, position int) (AttachmentAsset, error)
	StartBackfill(ctx context.Context, sourceID string) (jobs.Info, error)
}

// AttachmentAsset is one inline image read for the binary route.
type AttachmentAsset struct {
	Position    int
	Name        string
	ContentType string
	Size        int64
	Inline      bool
	SHA256      string
	Bytes       []byte
}

// ShutdownAPI signals the main goroutine once.
type ShutdownAPI interface {
	Request(ctx context.Context) error
}

// Layer1API is the local classifier, its judge runs and the label queue.
type Layer1API interface {
	Start(ctx context.Context) (jobs.Info, error)
	StartJudge(ctx context.Context, o layer1.JudgeOptions) (jobs.Info, error)
	LabelQueue(ctx context.Context, q layer1.LabelQuery) (layer1.LabelQueue, error)
	// LabelQueueContains answers whether one queue offers a message: the
	// only pre-gate message read the app allows.
	LabelQueueContains(ctx context.Context, q layer1.LabelQuery, messageID int64) (bool, error)
	PutLabels(ctx context.Context, items []feedback.PutItem) (feedback.PutResult, error)
}

// HubAPI is the pre-gate read side: the hub, its label summary and the
// current answer history.
type HubAPI interface {
	Hub(ctx context.Context) (hub.Hub, error)
	Answers(ctx context.Context, q hub.AnswerQuery) (hub.AnswerPage, error)
	LabelSummary(ctx context.Context) (hub.LabelSummary, error)
}

// SourceInfo names one registered connector.
type SourceInfo struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// StatusView is the status screen. The adapter fills the service-owned
// fields; the server fills the process-owned ones.
type StatusView struct {
	Version        string            `json:"version"`
	Launch         string            `json:"launch"`
	PID            int               `json:"pid"`
	StartedAt      time.Time         `json:"started_at"`
	URL            string            `json:"url"`
	DataDir        string            `json:"data_dir"`
	StorePath      string            `json:"store_path"`
	StoreMeta      map[string]string `json:"store_meta"`
	SystemLanguage string            `json:"system_language"`
	SystemLocale   string            `json:"system_locale"`
	TimeZone       string            `json:"time_zone"`
	Sources        []SourceInfo      `json:"sources"`
	JobsRunning    int               `json:"jobs_running"`
	SSEClients     int               `json:"sse_clients"`
	RestartPending []string          `json:"restart_pending"`
	// Resources is the live host reading and the effective ceilings. It is
	// absent when the API was built without a guard (tests and tools).
	Resources *resource.Status `json:"resources,omitempty"`
}

// LearningView is the one gate query. Open is the real gate (the latch),
// never the test preview; Preview is the effective preview rule: the test
// setting is on and the real gate is closed.
type LearningView struct {
	Status  learning.Status `json:"status"`
	Open    bool            `json:"open"`
	Preview bool            `json:"preview"`
}

// GateView is the open/latched gate projection: the one Allow decision plus
// the effective preview rule. Status is present only while the real gate is
// closed; the locked path builds the full learning status, and it is the same
// status the locked screen renders.
type GateView struct {
	Allowed bool             `json:"allowed"`
	Open    bool             `json:"open"`
	Preview bool             `json:"preview"`
	Status  *learning.Status `json:"status,omitempty"`
}

// PriorityView is the gated priority page. Items is always an array (never
// null); total counts the candidates that pass the filter, not the mailbox.
// Available is the snapshot's pageable depth (at most priority.MaxCached) and
// may be smaller than total; has_more says whether another page exists.
type PriorityView struct {
	Items     []PriorityRow `json:"items"`
	Total     int           `json:"total"`
	Available int           `json:"available"`
	Offset    int           `json:"offset"`
	HasMore   bool          `json:"has_more"`
	// Counts is every filter's ranked total from the same snapshot as Items.
	// The Important chips read it instead of asking for one page per chip.
	Counts      map[string]int `json:"counts"`
	GeneratedAt string         `json:"generated_at"`
	Preview     bool           `json:"preview"`
}

// PriorityRow is one ranked row of the Home and Important lists: the fields
// those screens render. The domain item also carries the full classifier
// signal vector, the score and the thread evidence the ranking and the
// commitments read; shipping all of it put ~13 signals on every row of the
// biggest list. The subject hierarchy stays because the row shows it.
type PriorityRow struct {
	MessageID      int64           `json:"message_id"`
	Subject        string          `json:"subject"`
	SenderName     string          `json:"sender_name"`
	SenderAddress  string          `json:"sender_address"`
	ReceivedAt     string          `json:"received_at"`
	HasAttachments bool            `json:"has_attachments"`
	Snippet        string          `json:"snippet"`
	Reasons        []string        `json:"reasons"`
	Preview        bool            `json:"preview"`
	Signals        PrioritySignals `json:"signals"`
	Action         PriorityAction  `json:"action"`
	ThreadState    string          `json:"thread_state,omitempty"`
	ThreadActor    string          `json:"thread_actor,omitempty"`
}

// PrioritySignals is the one signal the Home row shows: the subject path.
type PrioritySignals struct {
	Subject PrioritySubject `json:"subject"`
}

// PrioritySubject is the canonical topic/subtopic pair of the row.
type PrioritySubject struct {
	Topic    string `json:"topic,omitempty"`
	Subtopic string `json:"subtopic,omitempty"`
}

// PriorityAction is what the row must say about the owner's move.
type PriorityAction struct {
	Primary action.Primary `json:"primary"`
	Waiting action.Waiting `json:"waiting"`
}

// PriorityRows projects the ranked domain items into the list contract at
// the one boundary that serves them.
func PriorityRows(items []priority.Item) []PriorityRow {
	out := make([]PriorityRow, len(items))
	for i, item := range items {
		out[i] = PriorityRow{
			MessageID: item.MessageID, Subject: item.Subject,
			SenderName: item.SenderName, SenderAddress: item.SenderAddress,
			ReceivedAt: item.ReceivedAt, HasAttachments: item.HasAttachments, Snippet: item.Snippet,
			Reasons: item.Reasons, Preview: item.Preview,
			Signals:     PrioritySignals{Subject: PrioritySubject{Topic: item.Signals.Subject.Topic, Subtopic: item.Signals.Subject.Subtopic}},
			Action:      PriorityAction{Primary: item.Action.Primary, Waiting: item.Action.Waiting},
			ThreadState: item.ThreadState, ThreadActor: item.ThreadActor,
		}
	}
	return out
}

// ThreadView is the gated conversation card. Messages is always an array
// (never null) and stays chronological even when Truncated is true;
// Count, FirstAt and LastAt cover the whole owner-visible conversation,
// while Messages is the requested page.
type ThreadView struct {
	ConversationID string                  `json:"conversation_id"`
	Subject        string                  `json:"subject"`
	State          ThreadStateView         `json:"state"`
	Messages       []ThreadMessageView     `json:"messages"`
	Participants   []ThreadParticipantView `json:"participants"`
	FirstAt        string                  `json:"first_at"`
	LastAt         string                  `json:"last_at"`
	Count          int                     `json:"count"`
	Truncated      bool                    `json:"truncated"`
	// NewestMessageID is the owner-visible newest message of the whole
	// conversation; Newer counts messages after an inclusive anchor page.
	NewestMessageID int64 `json:"newest_message_id"`
	Newer           int   `json:"newer"`
}

// ThreadStateView is the resolver's assessment as the wire carries it. It
// contains no shared subject or body: only the state, its owner-facing
// actor (empty when the last move came from shared/delegate mail) and the
// visibility disclosure.
type ThreadStateView struct {
	State            threadintel.State      `json:"state"`
	Reason           threadintel.Reason     `json:"reason"`
	Actor            string                 `json:"actor"`
	LastAt           string                 `json:"last_at"`
	LastMessageID    int64                  `json:"last_message_id"`
	ReplyToMessageID int64                  `json:"reply_to_message_id"`
	Visibility       threadintel.Visibility `json:"visibility"`
	Confidence       float64                `json:"confidence"`
	Topic            string                 `json:"topic,omitempty"`
	Subtopic         string                 `json:"subtopic,omitempty"`
}

// ThreadMessageView is one owner-visible message with its bounded snippet.
type ThreadMessageView struct {
	ID             int64  `json:"id"`
	Subject        string `json:"subject"`
	SenderName     string `json:"sender_name"`
	SenderAddress  string `json:"sender_address"`
	ReceivedAt     string `json:"received_at"`
	Sent           bool   `json:"sent"`
	IsRead         bool   `json:"is_read"`
	HasAttachments bool   `json:"has_attachments"`
	Snippet        string `json:"snippet"`
}

// ThreadParticipantView is one external participant of the conversation.
type ThreadParticipantView struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Messages int    `json:"messages"`
	LastAt   string `json:"last_at"`
}

// ConversationsView is the thread-first mailbox page. Conversations and each
// row's participants are always arrays; Counts always carries every state
// key; Coverage always carries the three coverage counts.
type ConversationsView struct {
	GeneratedAt   string                   `json:"generated_at"`
	Conversations []ConversationView       `json:"conversations"`
	Total         int                      `json:"total"`
	Counts        map[string]int           `json:"counts"`
	NextBefore    string                   `json:"next_before"`
	HasMore       bool                     `json:"has_more"`
	Preview       bool                     `json:"preview"`
	Coverage      ConversationCoverageView `json:"coverage"`
}

// ConversationView is one digest row. Organization is omitted when the
// latest external participant does not resolve to one.
type ConversationView struct {
	ConversationID   string                        `json:"conversation_id"`
	Subject          string                        `json:"subject"`
	SenderName       string                        `json:"sender_name"`
	SenderAddress    string                        `json:"sender_address"`
	LastMessageID    int64                         `json:"last_message_id"`
	LastAt           string                        `json:"last_at"`
	FirstAt          string                        `json:"first_at"`
	Messages         int                           `json:"messages"`
	Sent             int                           `json:"sent"`
	Received         int                           `json:"received"`
	Unread           int                           `json:"unread"`
	LastSent         bool                          `json:"last_sent"`
	LastIsRead       bool                          `json:"last_is_read"`
	HasAttachments   bool                          `json:"has_attachments"`
	Snippet          string                        `json:"snippet"`
	DueDate          string                        `json:"due_date"`
	Participants     []ThreadParticipantView       `json:"participants"`
	ParticipantCount int                           `json:"participant_count"`
	Organization     *ConversationOrganizationView `json:"organization,omitempty"`
	State            ThreadStateView               `json:"state"`
	StateSource      string                        `json:"state_source"`
}

// ConversationOrganizationView is the external organization label.
type ConversationOrganizationView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Declared bool   `json:"declared"`
}

// ConversationCoverageView reports the owner-visible mail behind the digest.
type ConversationCoverageView struct {
	Messages      int `json:"messages"`
	Conversations int `json:"conversations"`
	Unthreaded    int `json:"unthreaded"`
}

// AskHistoryView is the persisted Q&A history rail, newest first. Each item
// is a projection; the full answer is GET /api/ask/history/{id}.
type AskHistoryView struct {
	Items []qa.HistoryRow `json:"items"`
}

// QuestionsView is the pre-gate question catalogue the labelling screen
// reads: the profile's identity and every typed question, in profile
// order. It carries no message, label value or triage answer.
type QuestionsView struct {
	Name        string            `json:"name"`
	Fingerprint string            `json:"fingerprint"`
	Questions   []engine.Question `json:"questions"`
}

// SettingView adds the effective value and layer to a schema setting.
type SettingView struct {
	*config.Setting
	Value          any             `json:"value"`
	Layer          config.Layer    `json:"layer"`
	RestartPending bool            `json:"restart_pending"`
	Learned        *config.Learned `json:"learned,omitempty"`
}

// SecretsView is the secret store's shape: names only, never values.
type SecretsView struct {
	Kind     string   `json:"kind"`
	ReadOnly bool     `json:"read_only"`
	Names    []string `json:"names"`
	Expected []string `json:"expected"`
}

// SidecarStatus is the managed-runtime screen.
type SidecarStatus struct {
	Builds   []sidecar.BuildInfo `json:"builds"`
	Runtimes []sidecar.Status    `json:"runtimes"`
}

// LogEntryView is one entry of a log page on the wire. HasAttrs is a
// projection fact the page handler sets only for attrs=off: it says whether
// the retained entry had attributes even though the page omits them, so the
// client can fetch that one entry later. It is not part of logx.Entry,
// which describes only what was stored.
type LogEntryView struct {
	logx.Entry
	HasAttrs bool `json:"has_attrs,omitempty"`
}

// LogEntryViews is the one copy from retained entries to the page's wire
// view. The page allocates its own entries; attribute maps stay shared
// (read-only) with the ring. The handler owns the attrs query projection.
func LogEntryViews(entries []logx.Entry) []LogEntryView {
	out := make([]LogEntryView, len(entries))
	for i, entry := range entries {
		out[i] = LogEntryView{Entry: entry}
	}
	return out
}

// LogSnapshot is a log read with its retained bounds.
type LogSnapshot struct {
	Entries   []LogEntryView `json:"entries"`
	Health    logx.Health    `json:"health"`
	OldestSeq uint64         `json:"oldest_seq"`
	LastSeq   uint64         `json:"last_seq"`
	Truncated bool           `json:"truncated"`
}

// ProviderInfo is one provider record as the UI sees it: the complete
// non-secret identity and limits stay in the provider file; the API never
// exposes a key value, only its secret name and whether it is present.
type ProviderInfo struct {
	ID           string      `json:"id"`
	Label        string      `json:"label,omitempty"`
	Protocol     string      `json:"protocol"`
	Kind         engine.Kind `json:"kind"`
	BaseURL      string      `json:"base_url"`
	Path         string      `json:"path"`
	Model        string      `json:"model"`
	APIKeySecret string      `json:"api_key_secret,omitempty"`
	Runtime      string      `json:"runtime,omitempty"`
	// The complete non-secret record, so the editor can round-trip a
	// record without resetting its limits, prices, retry or embedding
	// spec on save.
	ContextTokens        int                   `json:"context_tokens,omitempty"`
	CharsPerToken        float64               `json:"chars_per_token,omitempty"`
	ReserveTokensPerByte float64               `json:"reserve_tokens_per_byte,omitempty"`
	ReserveOutputTokens  int                   `json:"reserve_output_tokens,omitempty"`
	MaxQuestions         int                   `json:"max_questions,omitempty"`
	ProbabilityTolerance float64               `json:"probability_tolerance,omitempty"`
	ResponseFormat       string                `json:"response_format,omitempty"`
	ReasoningEffort      string                `json:"reasoning_effort,omitempty"`
	PriceInput           *float64              `json:"price_input_per_mtok"`
	PriceOutput          *float64              `json:"price_output_per_mtok"`
	Retry                engine.Retry          `json:"retry,omitzero"`
	Envelope             string                `json:"envelope,omitempty"`
	NoulFold             string                `json:"noul_fold,omitempty"`
	Embedding            *engine.EmbeddingSpec `json:"embedding,omitempty"`
	KeyPresent           bool                  `json:"key_present"`
	Status               engine.ProviderStatus `json:"status"`
}

// ProviderRoleInfo is one settings-owned provider selector with its state:
// empty, unknown record or wrong kind is an explicit unconfigured blocker;
// a configured record whose key is missing is configured but unavailable.
type ProviderRoleInfo struct {
	Setting    string      `json:"setting"`
	Label      string      `json:"label"`
	Kind       engine.Kind `json:"kind"`
	Provider   string      `json:"provider"`
	Configured bool        `json:"configured"`
	Available  bool        `json:"available"`
	Detail     string      `json:"detail,omitempty"`
}

// MessageListView is the gated all-mail page. Messages is always an array;
// Counts is present only when the request asked for it and then carries
// every mail role, zero-filled. NextBefore is the opaque next page cursor
// and is empty when HasMore is false.
//
// Rows carry metadata and a bounded plain-text snippet only: never
// recipients, a body, raw HTML or transport headers.
type MessageListView struct {
	Messages   []MessageListRow `json:"messages"`
	Total      int              `json:"total"`
	Counts     map[string]int   `json:"counts,omitempty"`
	NextBefore string           `json:"next_before"`
	HasMore    bool             `json:"has_more"`
}

// MessageListRow is one owner mail-list row: the fields the list renders and
// nothing else. The stored row also carries the sender's representing
// address and the store/folder labels; no list screen reads them, and they
// were 6 fields on every row of the largest list page.
type MessageListRow struct {
	RowID          int64           `json:"row_id"`
	Subject        string          `json:"subject"`
	SenderName     string          `json:"sender_name"`
	SenderAddress  string          `json:"sender_address"`
	Instant        string          `json:"instant"`
	ReceivedAt     string          `json:"received_at"`
	SentOn         string          `json:"sent_on"`
	Role           mail.FolderRole `json:"role"`
	IsRead         bool            `json:"is_read"`
	HasAttachments bool            `json:"has_attachments"`
	Snippet        string          `json:"snippet"`
}

// MessageWire is the message projection of the viewer: exactly the fields
// the message screen renders. The stored message also carries raw headers
// (2-6 KB per mail), the full recipient list, opaque connector ids and
// connector metadata no screen reads; serializing them made the detail the
// largest response of the app and, at the configured caps, several hundred
// KB. SenderOf reads the raw headers before this projection drops them, and
// the sender identity is carried by SenderView, not by a second copy here.
type MessageWire struct {
	ID                      string          `json:"id"`
	StoreName               string          `json:"store_name"`
	FolderPath              string          `json:"folder_path"`
	FolderRole              mail.FolderRole `json:"folder_role"`
	Subject                 string          `json:"subject"`
	SenderName              string          `json:"sender_name"`
	SenderAddress           string          `json:"sender_address"`
	SentRepresentingAddress string          `json:"sent_representing_address"`
	ReceivedAt              string          `json:"received_at"`
	SentOn                  string          `json:"sent_on"`
	IsRead                  bool            `json:"is_read"`
	HasAttachments          bool            `json:"has_attachments"`
	// Body is the canonical plain text. It is omitted once the sanitized
	// HTML fragment is present: the viewer renders the fragment and the
	// duplicate plain body only made the detail response larger.
	Body           string            `json:"body,omitempty"`
	BodyTruncated  bool              `json:"body_truncated"`
	Attachments    []mail.Attachment `json:"attachments"`
	ConversationID string            `json:"conversation_id"`
	Partial        bool              `json:"partial"`
}

// WireMessage projects a stored message into the viewer contract. It is the
// one owner of that projection.
func WireMessage(m *mail.Message) *MessageWire {
	if m == nil {
		return nil
	}
	attachments := nonNilSlice(m.Attachments)
	return &MessageWire{
		ID: m.ID, StoreName: m.StoreName, FolderPath: m.FolderPath, FolderRole: m.FolderRole,
		Subject: m.Subject, SenderName: m.SenderName, SenderAddress: m.SenderAddress,
		SentRepresentingAddress: m.SentRepresentingAddress,
		ReceivedAt:              m.ReceivedAt, SentOn: m.SentOn,
		IsRead: m.IsRead, HasAttachments: m.HasAttachments,
		Body: m.Body, BodyTruncated: m.BodyTruncated, Attachments: attachments,
		ConversationID: m.ConversationID, Partial: m.Partial,
	}
}

// MessageView is the gated message card. Message is the viewer's projection
// of the owner-scope message; the verdict is the scope decision, when any.
// Sender keeps From, Sender, on-behalf, Reply-To, Return-Path and the
// reported authentication separate from triage and from the stored message.
//
// Message.Body is the canonical plain text and stays byte-for-byte
// compatible. BodyHTML is a sanitized display fragment (internal/render),
// not the stored HTML: it is safe for the shell's sandboxed iframe and is
// the only HTML field the API exposes. BodyCSS is the compiled presentation
// sheet for that fragment; the shell links it as a blob stylesheet, so the
// API never emits a style attribute or a <style> element.
type MessageView struct {
	RowID             int64          `json:"row_id"`
	Revision          int64          `json:"revision"`
	StoreKind         mail.StoreKind `json:"store_kind,omitempty"`
	Verdict           scope.Verdict  `json:"verdict,omitempty"`
	Reason            scope.Reason   `json:"reason,omitempty"`
	Message           *MessageWire   `json:"message"`
	Sender            SenderView     `json:"sender"`
	BodyFormat        string         `json:"body_format"`
	BodyHTML          string         `json:"body_html"`
	BodyCSS           string         `json:"body_css,omitempty"`
	BodyHTMLTruncated bool           `json:"body_html_truncated"`
	BodyRemoteImages  int            `json:"body_remote_images"`
	BodyBlockedImages int            `json:"body_blocked_images"`
	BodyInlineImages  int            `json:"body_inline_images"`
	BodyMissingInline int            `json:"body_missing_inline"`
}

// ProjectBody drops the duplicate plain body when the sanitized HTML
// fragment is present. A plain message, or an HTML message whose fragment is
// empty, keeps its canonical text as the viewer's fallback. It is the one
// viewer-body policy, applied by the message handler.
func (v *MessageView) ProjectBody() {
	if v.Message != nil && v.BodyFormat == "html" && v.BodyHTML != "" {
		v.Message.Body = ""
	}
}

// validate rejects a Services value that would panic in a handler.
func (s Services) validate() error {
	missing := []struct {
		name string
		ok   bool
	}{
		{"bus", s.Bus != nil},
		{"status", s.Status != nil},
		{"learning", s.Learning != nil},
		{"jobs", s.Jobs != nil},
		{"mailboxes", s.Mailboxes != nil},
		{"runs", s.Runs != nil},
		{"triage", s.Triage != nil},
		{"search", s.Search != nil},
		{"settings", s.Settings != nil},
		{"secrets", s.Secrets != nil},
		{"providers", s.Providers != nil},
		{"sidecar", s.Sidecar != nil},
		{"logs", s.Logs != nil},
		{"labels", s.Labels != nil},
		{"hub", s.Hub != nil},
		{"messages", s.Messages != nil},
		{"threads", s.Threads != nil},
		{"conversations", s.Conversations != nil},
		{"commitments", s.Commitments != nil},
		{"brief", s.Brief != nil},
		{"priority", s.Priority != nil},
		{"agenda", s.Agenda != nil},
		{"ask", s.Ask != nil},
		{"companies", s.Companies != nil},
		{"shutdown", s.Shutdown != nil},
		{"boot", s.Boot != nil},
	}
	for _, m := range missing {
		if !m.ok {
			return fmt.Errorf("api: %s service is required", m.name)
		}
	}
	return nil
}

// ErrShutdown is returned by a shutdown request that already happened.
var ErrShutdown = errors.New("api: shutdown already requested")
