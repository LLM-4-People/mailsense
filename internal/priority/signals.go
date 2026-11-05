package priority

import (
	"math"
	"time"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// rowSignals is what one row's stored answers produce. The reply pass
// computes it once and the scoring pass reuses it, so no message's route
// answers are decoded or interpreted twice. The reply obligation and the
// entry facts are derived here, once, from the thresholded signals; the
// action classifier's view (actionSignals) is derived at the point of use
// instead of being stored, because it is a pure function of sig and the
// build retains one of these records per owner message.
type rowSignals struct {
	sig        Signals
	bulk       bool
	wantsReply bool
	question   bool
	cta        bool
	preview    bool
}

// Bands is this record's levels, read by the classifier's one band rule
// (action.Bands) so the ranking and the action cannot disagree about what is
// urgent, deadline-bound or work.
func (s Signals) Bands() action.Bands {
	return action.Bands{Urgency: s.Urgency, Deadline: s.Deadline, Effort: s.Effort, RequestCount: s.RequestCount}
}

// neededRoles is the one list of profile roles the priority ranking reads.
// signals() reads every role here, including RoleTopic for the subject path,
// and derives the subject from the values it already extracted. The AST guard
// TestNeededQuestionsCoverTheRankingRoles fails if signals stops reading a
// role this list names or starts reading one it does not. The store never
// imports taxonomy: it receives the resulting question keys as data and
// decodes only those.
var neededRoles = []taxonomy.Role{
	taxonomy.RoleCategory, taxonomy.RoleTopic, taxonomy.RoleAsksFor,
	taxonomy.RoleStance, taxonomy.RoleRelationship, taxonomy.RoleDecision,
	taxonomy.RoleReply, taxonomy.RoleCTA, taxonomy.RoleUrgency,
	taxonomy.RoleSuspicious, taxonomy.RoleDeadline, taxonomy.RoleEffort,
	taxonomy.RoleRequests, taxonomy.RoleQuestion, taxonomy.RoleInfo,
}

// neededQuestions returns the profile questions the priority read decodes:
// the role questions above, in role order. A role the profile does not keep
// is skipped. This is the one owner of "what the ranking reads": replySignals
// passes these keys to the store, and answersForInto filters the merged
// answers to the same keys.
func neededQuestions(p *taxonomy.Profile) []store.QuestionKey {
	if p == nil {
		return nil
	}
	keys := make([]store.QuestionKey, 0, len(neededRoles))
	for _, role := range neededRoles {
		q, ok := p.RoleQuestion(role)
		if !ok {
			continue
		}
		keys = append(keys, store.QuestionKey{Name: q.Name, Type: string(q.Type)})
	}
	return keys
}

// signalsFor merges the stored route answers with the threshold-free
// preview at the one trust boundary both passes use. It computes the
// ranking's question selection for this one call; the build uses
// signalsForKeysInto with the selection and scratch map it already owns.
func signalsFor(p *taxonomy.Profile, routeAnswers map[string]route.Answer, previewAnswers map[string]engine.Answer) (rowSignals, error) {
	return signalsForKeys(p, neededQuestions(p), routeAnswers, previewAnswers)
}

// signalsForKeys is signalsFor with the caller's precomputed question
// selection, so a whole-mailbox build resolves the role list once instead
// of once per row.
func signalsForKeys(p *taxonomy.Profile, keys []store.QuestionKey, routeAnswers map[string]route.Answer, previewAnswers map[string]engine.Answer) (rowSignals, error) {
	return signalsForKeysInto(p, keys, routeAnswers, previewAnswers, make(map[string]engine.Answer, len(keys)))
}

// signalsForKeysInto is signalsForKeys with a caller-owned scratch answer map.
// A whole-mailbox build keeps one map and clears it per row, so deriving a
// row's signals does not allocate the merge map. The map is scratch after the
// call: rowSignals holds only plain values.
func signalsForKeysInto(p *taxonomy.Profile, keys []store.QuestionKey, routeAnswers map[string]route.Answer, previewAnswers map[string]engine.Answer, answers map[string]engine.Answer) (rowSignals, error) {
	previewUsed, err := answersForInto(p, keys, routeAnswers, previewAnswers, answers)
	if err != nil {
		return rowSignals{}, err
	}
	return rowSignalsFor(p, answers, previewUsed), nil
}

// rowSignalsFor derives every fact the two passes read from one merged answer
// map. bulk is the action classifier's automated-bulk predicate (which
// overrides on suspicion), and wantsReply reconciles the reply answer with the
// dominant ask and the bulk veto. Computing them here is the one place these
// derivations run per row; the classifier's full view is derived where it is
// needed by actionSignals.
func rowSignalsFor(p *taxonomy.Profile, answers map[string]engine.Answer, previewUsed bool) rowSignals {
	sig := signals(p, answers)
	bulk := action.Bulk(actionSignals(sig))
	return rowSignals{
		sig: sig, bulk: bulk,
		wantsReply: wantsResponse(sig, bulk),
		question:   sig.IsQuestion == 1,
		cta:        sig.CallToAction == 1,
		preview:    previewUsed,
	}
}

// scoreRowSignals ranks one message from its precomputed signals. at and
// dated are the caller's one parse of the stored receive instant.
func scoreRowSignals(in Input, rs rowSignals, at time.Time, dated bool, withSnippet bool) (Item, bool) {
	sig := rs.sig
	act := action.Classify(action.Input{MessageID: in.Message.MessageID, Signals: actionSignals(sig), Thread: in.Thread,
		StoredDue: in.Message.StoredDue})
	// The reply filter claims an owner obligation, so it follows the action
	// classification: an answered thread, an automated message or a concrete
	// payment is not "to reply". The score still counts the classified reply
	// signal (below), because a security alert that also asks an answer is
	// more important than one that does not.
	replyOpen := act.Primary == action.Reply && act.Waiting == action.WaitingOwner
	replySignal := rs.wantsReply
	switch in.Thread.State {
	case threadintel.AwaitingOwner:
		if in.Thread.ReplyToMessageID != 0 {
			replySignal = in.Thread.ReplyToMessageID == in.Message.MessageID
		}
	case threadintel.AwaitingOther, threadintel.AnsweredByOwner, threadintel.AnsweredByOther, threadintel.NoAction:
		replySignal = false
	}
	reasons, score, include := scoreSignals(rs, in.Now, at, dated, replySignal)
	if !include {
		return Item{}, false
	}
	switch in.Thread.State {
	case threadintel.AwaitingOwner:
		reasons = append(reasons, "thread_waiting")
	case threadintel.AwaitingOther:
		reasons = append(reasons, "thread_waiting_other")
	case threadintel.AnsweredByOwner, threadintel.AnsweredByOther:
		reasons = append(reasons, "thread_answered")
	case threadintel.Unknown:
		reasons = append(reasons, "thread_unknown")
	}
	item := Item{
		MessageID: in.Message.MessageID, ConversationID: in.Message.ConversationID,
		Subject: in.Message.Subject, SenderName: in.Message.SenderName,
		SenderAddress: in.Message.SenderAddress, ReceivedAt: in.Message.ReceivedAt, FolderRole: in.Message.FolderRole,
		HasAttachments: in.Message.HasAttachments, Score: score,
		Reasons: reasons, Preview: rs.preview, Signals: sig, Action: act,
		ThreadState: string(in.Thread.State), ThreadReason: string(in.Thread.Reason),
		ThreadActor: in.Thread.OwnerActorName, ThreadLastAt: in.Thread.LastAt, ThreadVisibility: string(in.Thread.Visibility),
		ThreadReplyTo: in.Thread.ReplyToMessageID, ReplyOpen: replyOpen,
	}
	if withSnippet {
		item.Snippet = Snippet(in.Message.Body, snippetRunes)
	}
	return item, true
}

// replyOwed is the one reconciliation of the reply answer with the
// dominant ask. A message the classifier itself says asks for nothing is
// informational: it cannot create a reply obligation, even when the reply
// answer disagrees. Missing asks_for does not override the reply answer.
// is_question is deliberately undirected and call_to_action is an action,
// so neither can open a reply here; the thread resolver reads this result.
func replyOwed(sig Signals) bool {
	return sig.ReplyNeeded == 1 && threadintel.ClassifyAsk(sig.AsksFor) != threadintel.AskFYI
}

// wantsResponse is the directed answer request the thread resolver reads.
// It is reply_needed only: is_question deliberately ignores who must answer,
// so it can never prove the owner owes one. A call to action is not a reply
// either: "verify your account" is an action, and the action classifier owns
// it. bulk is that classifier's predicate for sig, derived once by
// rowSignalsFor; a suspicious message is never bulk mail.
func wantsResponse(sig Signals, bulk bool) bool {
	return replyOwed(sig) && !bulk
}

// actionSignals maps the ranking's thresholded view onto the action
// classifier's input. It is the one translation between the two packages.
func actionSignals(sig Signals) action.Signals {
	return action.Signals{
		Urgency: sig.Urgency, ReplyNeeded: sig.ReplyNeeded, Suspicious: sig.Suspicious,
		DecisionNeeded: sig.DecisionNeeded, Deadline: sig.Deadline, Effort: sig.Effort,
		RequestCount: sig.RequestCount, IsQuestion: sig.IsQuestion, CallToAction: sig.CallToAction,
		InfoSufficient: sig.InfoSufficiency, Category: sig.Category, Topic: sig.Subject.Topic,
		Subtopic: sig.Subject.Subtopic, AsksFor: sig.AsksFor, Stance: sig.Stance,
		Relationship: sig.Relationship,
	}
}

// answersFor merges the stored route answers with the threshold-free
// preview, at the one trust boundary both scoring and thread-signal reads
// use. It allocates the merge map for one call; a whole-mailbox build uses
// answersForInto with one scratch map instead.
func answersFor(p *taxonomy.Profile, keys []store.QuestionKey, routeAnswers map[string]route.Answer, preview map[string]engine.Answer) (map[string]engine.Answer, bool, error) {
	answers := make(map[string]engine.Answer, len(keys))
	previewUsed, err := answersForInto(p, keys, routeAnswers, preview, answers)
	if err != nil {
		return nil, false, err
	}
	return answers, previewUsed, nil
}

// answersForInto is answersFor filling a caller-owned map. Only the ranking's
// needed questions enter the merge: the store decoded exactly those keys, and
// a preview fill of any other question is neither decoded nor counted. The
// returned preview flag therefore means "the ranking's signals used preview
// data", not "some answer anywhere was filled". A malformed preview of a
// needed question is an error, never a silent zero. The map is cleared first,
// so a build can reuse it across rows.
func answersForInto(p *taxonomy.Profile, keys []store.QuestionKey, routeAnswers map[string]route.Answer, preview map[string]engine.Answer, answers map[string]engine.Answer) (bool, error) {
	clear(answers)
	for _, key := range keys {
		if a, ok := routeAnswers[key.Name]; ok {
			answers[key.Name] = a.Answer
		}
	}
	previewUsed := false
	if len(preview) > 0 {
		changed, err := triage.ApplyPreview(p, answers, preview)
		if err != nil {
			return false, err
		}
		previewUsed = changed
	}
	return previewUsed, nil
}

// signals extracts the thresholded values. An abstained, refused, missing
// or type-mismatched answer is absent (zero), never a zero-valued answer.
// The subject is built from these same values, so the ranking's topic path
// obeys the signals' cutoff: subject.FromAnswers accepts an out-of-range Noul
// as yes (a.Value >= cutoff) while answerOf rejects it, and the ranking
// follows the thresholded behavior.
func signals(p *taxonomy.Profile, answers map[string]engine.Answer) Signals {
	q := p.QuestionName
	category := choice(p, answers, q(taxonomy.RoleCategory))
	topic := choice(p, answers, q(taxonomy.RoleTopic))
	asksFor := choice(p, answers, q(taxonomy.RoleAsksFor))
	stance := choice(p, answers, q(taxonomy.RoleStance))
	relationship := choice(p, answers, q(taxonomy.RoleRelationship))
	decision, reply, cta := yes(p, answers, q(taxonomy.RoleDecision)), yes(p, answers, q(taxonomy.RoleReply)), yes(p, answers, q(taxonomy.RoleCTA))
	return Signals{
		Urgency:         level(p, answers, q(taxonomy.RoleUrgency)),
		ReplyNeeded:     reply,
		Suspicious:      yes(p, answers, q(taxonomy.RoleSuspicious)),
		DecisionNeeded:  decision,
		Deadline:        level(p, answers, q(taxonomy.RoleDeadline)),
		Effort:          level(p, answers, q(taxonomy.RoleEffort)),
		RequestCount:    level(p, answers, q(taxonomy.RoleRequests)),
		IsQuestion:      yes(p, answers, q(taxonomy.RoleQuestion)),
		CallToAction:    cta,
		InfoSufficiency: yes(p, answers, q(taxonomy.RoleInfo)),
		Category:        category,
		AsksFor:         asksFor,
		Stance:          stance,
		Relationship:    relationship,
		Subject: subject.Classify(subject.Input{
			Category: category, Topic: topic, AsksFor: asksFor, Stance: stance,
			Relationship: relationship,
			Decision:     decision == 1, Reply: reply == 1, CTA: cta == 1,
		}),
	}
}

// answerOf returns a usable answer for name (typed, not refused, not
// abstained, same type as the profile question).
func answerOf(p *taxonomy.Profile, answers map[string]engine.Answer, name string) (engine.Answer, *engine.Question, bool) {
	q, ok := p.Question(name)
	if !ok {
		return engine.Answer{}, nil, false
	}
	a, ok := answers[name]
	if !ok || !a.Answered() || a.Type != q.Type {
		return engine.Answer{}, nil, false
	}
	if math.IsNaN(a.Value) || math.IsInf(a.Value, 0) {
		return engine.Answer{}, nil, false
	}
	switch q.Type {
	case engine.Noul:
		if a.Value < 0 || a.Value > 1 {
			return engine.Answer{}, nil, false
		}
	case engine.Score:
		if a.Value < 0 || a.Value > float64(q.Top()) {
			return engine.Answer{}, nil, false
		}
	case engine.Choice:
		if !q.HasOption(a.Choice) {
			return engine.Answer{}, nil, false
		}
	}
	return a, q, true
}

func yes(p *taxonomy.Profile, answers map[string]engine.Answer, name string) int {
	a, _, ok := answerOf(p, answers, name)
	if !ok || a.Value < p.Cutoff(name) {
		return 0
	}
	return 1
}

func level(p *taxonomy.Profile, answers map[string]engine.Answer, name string) int {
	a, q, ok := answerOf(p, answers, name)
	if !ok {
		return 0
	}
	// A score's level is the class selective.Decide chose, the same class
	// Mail and Triage display. The expected level (round(Value)) is a
	// different fact and can name another level.
	if q.Type == engine.Score {
		if class, ok := selective.Decided(q, a, p.Cutoff(name)); ok {
			return max(0, min(class, q.Top()))
		}
	}
	n := int(math.Round(a.Value))
	return max(0, min(n, q.Top()))
}

func choice(p *taxonomy.Profile, answers map[string]engine.Answer, name string) string {
	a, q, ok := answerOf(p, answers, name)
	if !ok || !q.HasOption(a.Choice) {
		return ""
	}
	return a.Choice
}
