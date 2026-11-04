// Package feedback is what the owner teaches and what the app learns from
// it: labels (stored in the store's labels table, changes published),
// few-shot example selection and label-derived suspicious priors (both
// causal: only labels made before a message, on other messages of the
// same sender or domain), evaluation against labels and weak reply labels
// (scored on the model's own value: no leak), threshold and escalation
// calibration, and the correlations and insights computed as plain data
// for the UI. Pure except the Labeler. Only manual labels teach or score:
// Teaching and Reference (sources.go) are the one source policy, and the
// judge's production answers stay audit-only.
package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

var log = logx.For("feedback")

// Label is one user label, valid under the current profile.
type Label struct {
	MessageID int64               `json:"message_id"`
	Question  string              `json:"question"`
	Type      engine.QuestionType `json:"type"`
	Value     taxonomy.Label      `json:"value"`
	Source    string              `json:"source"`
	CreatedAt string              `json:"created_at"`
	Sender    string              `json:"sender"` // normalized sender of the labelled message
	Subject   string              `json:"subject"`
	// Created is when the label was made, At when the labelled message
	// happened (zero when unknown): a label only informs mail after both.
	Created time.Time `json:"-"`
	At      time.Time `json:"-"`
}

// Positive reads a noul label as yes/no with the question's cutoff.
func (l *Label) Positive(p *taxonomy.Profile) bool { return l.Value.Number >= p.Cutoff(l.Question) }

// Before reports whether the label existed, about a message that had
// happened, before t.
func (l *Label) Before(t time.Time) bool {
	return !l.Created.IsZero() && l.Created.Before(t) && (l.At.IsZero() || l.At.Before(t))
}

// LabelChange is published when labels are set or removed. A batch of
// labels publishes one event (a full subscriber buffer drops events, and a
// card or batch would otherwise lose the retrain trigger): Count is how
// many labels it covers, and MessageID and Question are set when every
// label of the batch shares them (one label, one message's card, or one
// question over several messages).
type LabelChange struct {
	MessageID int64  `json:"message_id,omitempty"`
	Question  string `json:"question,omitempty"`
	Action    string `json:"action"` // "set" or "delete"
	Count     int    `json:"count,omitempty"`
}

// TopicLabel carries every label change.
var TopicLabel = events.NewTopic[LabelChange]("feedback.label")

// MaxLabelBatch is the most labels one PutBatch call accepts: a page of a
// labelling card, not an unbounded corpus import.
const MaxLabelBatch = 100

// BatchSizeProblem is the one refusal text of an oversized label batch: the
// API's request bound and the labeler's domain guard share it, so the two
// can never say different things.
func BatchSizeProblem(size int) string {
	return fmt.Sprintf("a label batch holds at most %d items, got %d", MaxLabelBatch, size)
}

// PutItem is one label of a batch.
type PutItem struct {
	MessageID int64           `json:"message_id"`
	Question  string          `json:"question"`
	Value     json.RawMessage `json:"value"`
}

// StoredLabel is one label a batch wrote: identifiers only, never a value.
type StoredLabel struct {
	MessageID int64  `json:"message_id"`
	Question  string `json:"question"`
	Source    string `json:"source"`
}

// ValidationError is the label batch's typed refusal: the batch itself is
// empty or too large, or — the request being well formed — one or more items
// name an unknown question, an invalid value, a duplicate, or a message the
// owner cannot see. The API maps item problems to one 422 problem per item;
// the typed value keeps every service-side refusal a client error rather
// than an internal one.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "feedback: labels not stored: " + strings.Join(e.Problems, "; ")
}

// PutResult is what a batch stored.
type PutResult struct {
	Stored []StoredLabel `json:"stored"`
}

// Labeler sets and removes labels (the UI's label actions).
type Labeler struct {
	st  *store.Store
	bus *events.Bus
}

// NewLabeler returns a labeler; bus may be nil.
func NewLabeler(st *store.Store, bus *events.Bus) *Labeler { return &Labeler{st: st, bus: bus} }

// Put validates a label against the profile and stores it (one PutBatch
// item: the one validation and storage path).
func (lb *Labeler) Put(ctx context.Context, p *taxonomy.Profile, messageID int64, question string, raw any) error {
	value, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("feedback: label value: %w", err)
	}
	_, err = lb.PutBatch(ctx, p, []PutItem{{MessageID: messageID, Question: question, Value: value}})
	return err
}

// PutMessage stores one message's answers to several questions in one
// transaction and publishes one change event (the UI's message card).
func (lb *Labeler) PutMessage(ctx context.Context, p *taxonomy.Profile, messageID int64, values map[string]json.RawMessage) (PutResult, error) {
	if len(values) == 0 {
		return PutResult{}, fmt.Errorf("feedback: message %d has no label values", messageID)
	}
	items := make([]PutItem, 0, len(values))
	for _, question := range slices.Sorted(maps.Keys(values)) {
		items = append(items, PutItem{MessageID: messageID, Question: question, Value: values[question]})
	}
	return lb.PutBatch(ctx, p, items)
}

// PutBatch validates every item against the profile and the store, then
// stores all of them or none in one transaction. Every problem is reported
// at once; source is always manual (the judge has its own writer). A
// message must exist and be visible.
func (lb *Labeler) PutBatch(ctx context.Context, p *taxonomy.Profile, items []PutItem) (PutResult, error) {
	return lb.put(ctx, p, items, store.LabelManual, "")
}

// PutJudge stores one message's judge answers in one transaction (one
// batch, one change event). values are the judge's parsed labels; the
// judge's prompt/taxonomy digest is the label's provenance. A judge
// answer refused by the model (nil in the judge's result) is never passed
// here: a refusal is no label.
func (lb *Labeler) PutJudge(ctx context.Context, p *taxonomy.Profile, messageID int64, prompt string, values map[string]taxonomy.Label) (PutResult, error) {
	return lb.PutJudgeSource(ctx, p, messageID, prompt, store.LabelJudge, values)
}

// PutJudgeSource is PutJudge with an explicit source: the judge reference
// (store.LabelJudge) or the judge's production answer (store.LabelJudgeAnswer)
// used by the certified fallback route. Both carry the prompt digest; the
// source is what keeps the reference and the answer disjoint.
func (lb *Labeler) PutJudgeSource(ctx context.Context, p *taxonomy.Profile, messageID int64, prompt, source string, values map[string]taxonomy.Label) (PutResult, error) {
	if len(values) == 0 {
		return PutResult{}, fmt.Errorf("feedback: message %d has no judge labels", messageID)
	}
	if p == nil {
		return PutResult{}, fmt.Errorf("feedback: a profile is required")
	}
	if source != store.LabelJudge && source != store.LabelJudgeAnswer {
		return PutResult{}, fmt.Errorf("feedback: %q is not a judge source", source)
	}
	items := make([]PutItem, 0, len(values))
	for _, question := range slices.Sorted(maps.Keys(values)) {
		q, ok := p.Question(question)
		if !ok {
			return PutResult{}, fmt.Errorf("feedback: judge label for unknown question %q", question)
		}
		items = append(items, PutItem{MessageID: messageID, Question: question, Value: json.RawMessage(values[question].JSON(q.Type))})
	}
	return lb.put(ctx, p, items, source, prompt)
}

// put is the one validation, storage and event path of manual and judge
// labels; a judge batch must carry its prompt digest and a manual batch
// none (a judge label is never mistaken for the owner's answer).
func (lb *Labeler) put(ctx context.Context, p *taxonomy.Profile, items []PutItem, source, prompt string) (PutResult, error) {
	if len(items) == 0 {
		return PutResult{}, &ValidationError{Problems: []string{"a label batch cannot be empty"}}
	}
	if len(items) > MaxLabelBatch {
		return PutResult{}, &ValidationError{Problems: []string{BatchSizeProblem(len(items))}}
	}
	if p == nil {
		return PutResult{}, fmt.Errorf("feedback: a profile is required")
	}
	switch {
	case (source == store.LabelJudge || source == store.LabelJudgeAnswer) && strings.TrimSpace(prompt) == "":
		return PutResult{}, fmt.Errorf("feedback: a judge label batch needs its prompt digest")
	case source != store.LabelJudge && source != store.LabelJudgeAnswer && prompt != "":
		return PutResult{}, fmt.Errorf("feedback: only judge labels carry a prompt digest")
	}
	var problems []string
	seen := map[string]bool{}
	writes := make([]store.LabelWrite, 0, len(items))
	for i, item := range items {
		where := fmt.Sprintf("item %d", i+1)
		if item.MessageID <= 0 {
			problems = append(problems, where+": message_id must be positive")
		}
		q, known := p.Question(item.Question)
		switch {
		case item.Question == "":
			problems = append(problems, where+": question is required")
		case !known:
			problems = append(problems, fmt.Sprintf("%s: unknown question %q", where, item.Question))
		}
		key := fmt.Sprintf("%d\x00%s", item.MessageID, item.Question)
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%s: duplicate message %d and question %q", where, item.MessageID, item.Question))
		}
		seen[key] = true
		if len(bytes.TrimSpace(item.Value)) == 0 {
			problems = append(problems, where+": value is required")
			continue
		}
		raw, err := jsonx.DecodeOne(item.Value)
		if err != nil {
			problems = append(problems, where+": "+err.Error())
			continue
		}
		if !known {
			continue
		}
		label, err := taxonomy.ParseLabel(q, raw)
		if err != nil {
			problems = append(problems, where+": "+err.Error())
			continue
		}
		writes = append(writes, store.LabelWrite{MessageID: item.MessageID, Question: item.Question,
			Value: label.JSON(q.Type), Source: source, Prompt: prompt})
	}
	ids := make([]int64, 0, len(items))
	seenIDs := map[int64]bool{}
	for _, item := range items {
		if item.MessageID > 0 && !seenIDs[item.MessageID] {
			seenIDs[item.MessageID] = true
			ids = append(ids, item.MessageID)
		}
	}
	if len(ids) > 0 {
		found, err := lb.st.MessageIDs(ctx, store.MessageFilter{RowIDs: ids})
		if err != nil {
			return PutResult{}, err
		}
		have := make(map[int64]bool, len(found))
		for _, id := range found {
			have[id] = true
		}
		for _, id := range ids {
			if !have[id] {
				problems = append(problems, fmt.Sprintf("message %d does not exist or is not visible", id))
			}
		}
	}
	if len(problems) > 0 {
		return PutResult{}, &ValidationError{Problems: problems}
	}
	if err := lb.st.PutLabels(ctx, writes); err != nil {
		return PutResult{}, err
	}
	stored := make([]StoredLabel, len(writes))
	for i, w := range writes {
		stored[i] = StoredLabel{MessageID: w.MessageID, Question: w.Question, Source: w.Source}
	}
	lb.publishBatch(stored)
	return PutResult{Stored: stored}, nil
}

// Delete removes the owner's label of a message's question.
func (lb *Labeler) Delete(ctx context.Context, messageID int64, question string) error {
	if err := lb.st.DeleteLabel(ctx, messageID, question, store.LabelManual); err != nil {
		return err
	}
	lb.changed(messageID, question, "delete")
	return nil
}

func (lb *Labeler) changed(messageID int64, question, action string) {
	lb.publish(LabelChange{MessageID: messageID, Question: question, Action: action, Count: 1})
}

// publishBatch publishes one event for a committed batch; it names the
// message and question when the batch has one of them (a card or a bulk
// answer of one question).
func (lb *Labeler) publishBatch(stored []StoredLabel) {
	change := LabelChange{Action: "set", Count: len(stored)}
	if len(stored) == 0 {
		lb.publish(change)
		return
	}
	message, question := stored[0].MessageID, stored[0].Question
	sameMessage, sameQuestion := true, true
	for _, s := range stored[1:] {
		sameMessage = sameMessage && s.MessageID == message
		sameQuestion = sameQuestion && s.Question == question
	}
	if sameMessage {
		change.MessageID = message
	}
	if sameQuestion {
		change.Question = question
	}
	lb.publish(change)
}

// publish is the one label event and log path (ids, questions, counts:
// never a value or a message body).
func (lb *Labeler) publish(change LabelChange) {
	log.Info("label changed", "message", change.MessageID, "question_key", change.Question, "action", change.Action, "count", change.Count)
	if lb.bus != nil {
		events.Publish(lb.bus, TopicLabel, change)
	}
}

// Load reads every label of visible mail usable under the profile, every
// source included: the teaching and reference views are Teaching and
// Reference, applied by each consumer, because the quality report also
// needs the per-source counts. Labels of an unpicked store never reach the
// learner (store.ListLabels applies the one visibility predicate); labels of
// questions the profile no longer has, or whose value its question no
// longer accepts, are skipped and counted (never silently mixed in).
func Load(ctx context.Context, st *store.Store, p *taxonomy.Profile) ([]Label, int, error) {
	rows, err := st.ListLabels(ctx)
	if err != nil {
		return nil, 0, err
	}
	out, skipped := FromRows(rows, p)
	if skipped > 0 {
		log.Warn("labels skipped under the current profile", "skipped", skipped, "kept", len(out))
	}
	return out, skipped, nil
}

// FromRows converts stored labels.
func FromRows(rows []store.LabelRow, p *taxonomy.Profile) ([]Label, int) {
	var out []Label
	skipped := 0
	for _, r := range rows {
		q, ok := p.Question(r.Question)
		if !ok {
			skipped++
			continue
		}
		v, err := taxonomy.DecodeLabel(q, r.Value)
		if err != nil {
			skipped++
			continue
		}
		l := Label{MessageID: r.MessageID, Question: r.Question, Type: q.Type, Value: v, Source: r.Source,
			CreatedAt: r.CreatedAt, Sender: mail.NormalizeAddress(r.SenderAddress), Subject: r.Subject}
		l.Created, _ = mail.Parse(r.CreatedAt)
		l.At, _ = mail.Instant(r.Role, r.ReceivedAt, r.SentOn)
		out = append(out, l)
	}
	return out, skipped
}
