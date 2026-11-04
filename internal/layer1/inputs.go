package layer1

import (
	"context"
	"encoding/json"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// Inputs is everything a layer-1 run reads. The store implements it
// (StoreInputs); the dev harness wraps it (gold dev and test messages
// never get a target): dependency injection, never a test flag.
type Inputs interface {
	// Members are the messages layer 1 answers: the visible owner mail of
	// triage.folder_roles, each with its conversation (the folds' groups,
	// the kNN's exclusion) and its instant.
	Members(ctx context.Context) ([]Member, error)
	// MemberIDs is the same population as Members without the thread
	// graph or the message rows: counts and queues that only need ids.
	MemberIDs(ctx context.Context) ([]int64, error)
	// Vectors are the stored vectors of the current space ("" and nil
	// when there is no embedder).
	Vectors(ctx context.Context) (space string, set *vector.Set, err error)
	// Cells are the members' causal evidence feature cells in one layout.
	Cells(ctx context.Context) (Layout, map[int64][]int32, error)
	// Labels are every stored label.
	Labels(ctx context.Context) ([]store.LabelRow, error)
	// Decisions streams the stored answers of a decider key, one message
	// at a time (a run never holds a whole mailbox's decoded answers).
	Decisions(ctx context.Context, key store.TriageKey, fn func(id int64, answers map[string]engine.Answer) error) error
	// Rules measures the layer-0 rules on evaluation labels (message →
	// question → value) and lists the rules firing on each member.
	Rules(ctx context.Context, eval map[int64]map[string]json.RawMessage) ([]RuleInfo, map[int64][]int, error)
}

// Member is one message layer 1 answers.
type Member struct {
	ID       int64
	Revision int64  // the message's revision (answers are saved at it)
	Group    string // its conversation as threaded now (folds never split one)
	Instant  time.Time
	// The lexical, sender and metadata features of the design's second
	// block. They are read from the message, never copied into a label.
	Subject         string
	Body            string
	SenderAddress   string
	HasAttachments  bool
	RecipientCount  int
	IsReply         bool
	Depth           int
	Hour            int
	ListUnsubscribe bool

	// The causal history block: what the mailbox already knew about this
	// sender, domain and conversation before this message. It is computed
	// from the visible population in time order, never from a label, and
	// it is the signal a small head needs for relationship and topic.
	FromOwner            bool
	SenderMessagesBefore int
	SenderFirstDays      int
	DomainMessagesBefore int
	DomainFirstDays      int
	ThreadMessagesBefore int

	// Recipients are the message's addressees (lowercased): the owner's own
	// outgoing mail names the people the owner wrote to, which is the only
	// causal evidence of a two-way relationship.
	Recipients []string
	// Reciprocity, recency and thread role, all computed before this
	// message: whether the owner ever wrote to this sender (and how often),
	// how long the sender was quiet, how much of the sender's mail arrived
	// in the last 30 days, and who spoke last in this conversation.
	OwnerWroteCountBefore     int
	SenderDaysSinceLast       int
	SenderMessagesLast30      int
	ThreadOwnerMessagesBefore int
	ThreadLastFromOwner       bool
}

// Layout names the evidence feature cells.
type Layout struct {
	ID    string
	Cells int
}

// RuleInfo is one rule with its measurement on the evaluation labels.
type RuleInfo struct {
	ID       string
	Question string
	Answer   evidence.RuleAnswer
	Stat     evidence.RuleStat
}
