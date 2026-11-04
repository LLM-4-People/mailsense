package layer1

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// EmbedderSource builds the configured embedder (embed.Source).
type EmbedderSource interface {
	Embedder() (engine.Embedder, error)
}

// StoreInputs reads a run's inputs from the store, the evidence engine
// and the embedder's vector space.
type StoreInputs struct {
	Config   *config.Config
	Store    *store.Store
	Evidence *evidence.Engine
	Embedder EmbedderSource // nil: no embedder
	vectors  vector.Set     // the in-memory copy of the space, refreshed per run

	// members is the member-id population of one mail generation and folder
	// role set. A label write moves Generation but not MailGeneration, so the
	// queue rebuild after a label click reuses this scan.
	memberMu  sync.Mutex
	memberKey string
	members   []int64
}

// memberFilter is the one population of layer 1 and the label queue:
// visible owner mail of triage.folder_roles.
func (s *StoreInputs) memberFilter() (store.MessageFilter, error) {
	var roles []mail.FolderRole
	for _, r := range s.Config.Strings(triage.SettingFolderRoles) {
		roles = append(roles, mail.FolderRole(r))
	}
	return store.MessageFilter{Roles: roles, OwnerOnly: true}, nil
}

// Members are the visible owner mail of triage.folder_roles, grouped by
// conversation (the causal view's threads as they stand now: a fold
// never splits a conversation).
func (s *StoreInputs) Members(ctx context.Context) ([]Member, error) {
	set, err := threads.SettingsFrom(s.Config)
	if err != nil {
		return nil, err
	}
	set.Roles = threads.CausalRoles()
	a, err := threads.Load(ctx, s.Store, set)
	if err != nil {
		return nil, err
	}
	filter, err := s.memberFilter()
	if err != nil {
		return nil, err
	}
	var out []Member
	err = s.Store.EachMessage(ctx, filter, func(r *store.MessageRow) error {
		m := Member{ID: r.RowID, Revision: r.Revision, Group: "m" + strconv.FormatInt(r.RowID, 10)}
		if t, ok := a.ThreadOf(r.RowID); ok {
			m.Group = "t" + strconv.FormatInt(t.ID, 10)
		}
		m.Instant, _ = r.Message.Instant()
		m.Subject = r.Message.Subject
		m.Body = mail.Head(mail.Strip(r.Message.Body), bodyHeadRunes)
		m.SenderAddress = r.Message.SenderAddress
		m.HasAttachments = r.Message.HasAttachments
		m.RecipientCount = len(r.Message.Recipients)
		m.IsReply = strings.TrimSpace(r.Message.InReplyTo) != ""
		m.Depth = strings.Count(r.Message.References, "<")
		if m.Depth == 0 && m.IsReply {
			m.Depth = 1
		}
		m.Hour = m.Instant.Hour()
		m.ListUnsubscribe = strings.Contains(strings.ToLower(r.Message.Headers), "list-unsubscribe")
		m.FromOwner = r.Reason == scope.ReasonSentByOwner
		// The thread as it stood at this message's own instant: a later
		// message bridging two conversations never changes who spoke here
		// before. The final Group stays the fold identity (a fold never
		// splits a conversation) and is not a history input.
		if _, earlier, ok := a.EarlierMembers(r.RowID); ok {
			fromOwner := make([]bool, 0, len(earlier))
			for _, prior := range earlier {
				fromOwner = append(fromOwner, prior.Role == mail.RoleSent)
			}
			addThreadHistory(&m, fromOwner)
		}
		if n := len(r.Message.Recipients); n > 0 {
			m.Recipients = make([]string, 0, n)
			for _, rcpt := range r.Message.Recipients {
				address := strings.ToLower(strings.TrimSpace(rcpt.Address))
				if address != "" {
					m.Recipients = append(m.Recipients, address)
				}
			}
		}
		out = append(out, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	addHistory(out)
	return out, nil
}

// MemberIDs lists the same population as Members without building the
// thread graph or decoding the messages: the label count, the label queue
// and the label requirement only need the ids.
func (s *StoreInputs) MemberIDs(ctx context.Context) ([]int64, error) {
	filter, err := s.memberFilter()
	if err != nil {
		return nil, err
	}
	roles := make([]string, len(filter.Roles))
	for i, r := range filter.Roles {
		roles[i] = string(r)
	}
	key := strconv.FormatUint(s.Store.MailGeneration(), 10) + "|" + strings.Join(roles, ",")
	s.memberMu.Lock()
	if s.members != nil && s.memberKey == key {
		ids := s.members
		s.memberMu.Unlock()
		// The population is immutable between generations; callers read it.
		return ids, nil
	}
	s.memberMu.Unlock()
	ids, err := s.Store.MessageIDs(ctx, filter)
	if err != nil {
		return nil, err
	}
	s.memberMu.Lock()
	s.members, s.memberKey = ids, key
	s.memberMu.Unlock()
	return ids, nil
}

// Vectors is the current space's copy (refreshed incrementally).
func (s *StoreInputs) Vectors(ctx context.Context) (string, *vector.Set, error) {
	if s.Embedder == nil {
		return "", nil, nil
	}
	e, err := s.Embedder.Embedder()
	if err != nil {
		return "", nil, err
	}
	if err := s.vectors.Refresh(ctx, s.Store, e.Space(), e.Dimensions()); err != nil {
		return "", nil, err
	}
	return e.Space(), &s.vectors, nil
}

// Cells are every member's causal evidence cells in the current layout.
func (s *StoreInputs) Cells(ctx context.Context) (Layout, map[int64][]int32, error) {
	if s.Evidence == nil {
		return Layout{}, nil, fmt.Errorf("layer1: %s is on without an evidence engine", SettingUseEvidence)
	}
	l := s.Evidence.FeatureLayout()
	out := map[int64][]int32{}
	err := s.Evidence.EachRecord(ctx, func(id int64, r evidence.Record) error {
		out[id] = l.Indices(r)
		return nil
	})
	return Layout{ID: l.ID, Cells: len(l.Names)}, out, err
}

// Labels are every stored label of visible mail.
func (s *StoreInputs) Labels(ctx context.Context) ([]store.LabelRow, error) {
	return s.Store.ListLabels(ctx)
}

// Decisions streams a decider key's stored answers.
func (s *StoreInputs) Decisions(ctx context.Context, key store.TriageKey, fn func(int64, map[string]engine.Answer) error) error {
	return s.Store.EachDecision(ctx, key, func(d store.Decision) error {
		var a map[string]engine.Answer
		if err := json.Unmarshal([]byte(d.Answers), &a); err != nil {
			return fmt.Errorf("layer1: decision of message %d (%s): %w", d.MessageID, key.Decider, err)
		}
		return fn(d.MessageID, a)
	})
}

// LoadAnswers is every stored answer of a decider key by message (small
// keys only: tests and the dev harness's comparison).
func LoadAnswers(ctx context.Context, in Inputs, key store.TriageKey) (map[int64]map[string]engine.Answer, error) {
	out := map[int64]map[string]engine.Answer{}
	err := in.Decisions(ctx, key, func(id int64, a map[string]engine.Answer) error {
		out[id] = a
		return nil
	})
	return out, err
}

// Rules measures the evidence rules on the evaluation labels
// (evidence.rules.z) and lists the rules firing on each message.
func (s *StoreInputs) Rules(ctx context.Context, eval map[int64]map[string]json.RawMessage) ([]RuleInfo, map[int64][]int, error) {
	if s.Evidence == nil {
		return nil, nil, nil
	}
	set := s.Evidence.Rules()
	fires := map[int64][]int{}
	labelled := map[int64]evidence.Record{}
	err := s.Evidence.EachRecord(ctx, func(id int64, r evidence.Record) error {
		for i := range set.Rules {
			if set.Rules[i].Fires(r) {
				fires[id] = append(fires[id], i)
			}
		}
		if eval[id] != nil {
			labelled[id] = r
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	snap := s.Config.Snapshot()
	stats := set.Measure(eval, labelled, snap.Float(evidence.SettingRulesZ), snap.Float(evidence.SettingRulesMinPrecision))
	out := make([]RuleInfo, len(set.Rules))
	for i := range set.Rules {
		r := &set.Rules[i]
		out[i] = RuleInfo{ID: r.ID, Question: r.Question, Answer: r.Parsed(), Stat: stats[i]}
	}
	return out, fires, nil
}
