package qa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/world"
)

// Route is the explicit answer route of one question. It is stored with the
// turn and never inherited by the next one.
type Route string

// Answer routes. Auto keeps the historical behavior: a deterministic answer
// when one is exact, the configured provider otherwise. Local never calls a
// provider. Remote uses the one configured qa.provider; only the exact
// deterministic owners (an accepted plain count, a clarification, an empty
// result) answer without it.
const (
	RouteAuto   Route = "auto"
	RouteLocal  Route = "local"
	RouteRemote Route = "remote"
)

// Route errors.
var (
	// ErrInvalidRoute is a route outside auto, local or remote.
	ErrInvalidRoute = errors.New("qa: invalid route")
	// ErrRouteUnavailable is a remote route without a usable generator.
	ErrRouteUnavailable = errors.New("qa: route unavailable")
)

// RouteUnavailableError names the route that could not run. Reason is a
// short, non-secret class for the API params; the wrapped error carries the
// provider diagnostic to the server log.
type RouteUnavailableError struct {
	Route  Route
	Reason string
	Err    error
}

func (e *RouteUnavailableError) Error() string {
	if e == nil {
		return ErrRouteUnavailable.Error()
	}
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Route, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %v", e.Route, e.Reason, e.Err)
}

func (e *RouteUnavailableError) Unwrap() error { return ErrRouteUnavailable }

// ParseRoute reads the request route. Empty means Auto, so an older client
// keeps the behavior it already had. An unknown value is rejected at the
// trust boundary, never coerced to Auto.
func ParseRoute(raw string) (Route, error) {
	route := Route(strings.ToLower(strings.TrimSpace(raw)))
	if route == "" {
		return RouteAuto, nil
	}
	if !route.Valid() {
		return "", ErrInvalidRoute
	}
	return route, nil
}

// Valid reports whether the route is one of the three accepted values.
func (r Route) Valid() bool {
	return r == RouteAuto || r == RouteLocal || r == RouteRemote
}

// remoteReason classifies a generator-build failure without exposing a key
// or a provider body.
func remoteReason(err error) string {
	if err == nil {
		return ""
	}
	return errorClass(err)
}

// answerAuto is the historical computed-or-provider path. It is the one
// owner of the Auto branch, shared by the dispatcher and independent of the
// explicit routes.
func (s *Service) answerAuto(ctx context.Context, view AskView, question, language string, preview bool,
	intent, senderText string, turns []store.QAHistory, turnKey string) (AskView, error) {
	switch intent {
	case IntentSender:
		return s.askSender(ctx, view, senderText)
	case IntentSummaryToday:
		// Auto must not answer "today" from the mailbox-wide priority
		// counts. The owner-day window is the one fact the question asks.
		return s.askToday(ctx, view)
	case IntentFreeform:
		return s.askFreeform(ctx, view, question, language, preview, turns, turnKey)
	default:
		org, clarify, err := s.companyScope(ctx, question, language)
		if err != nil {
			return AskView{}, err
		}
		if clarify != nil {
			return clarifyView(view, clarify), nil
		}
		if org == nil {
			return s.askStructured(ctx, view, intent)
		}
		// A named organization scopes the question, even when a structured
		// keyword selected this branch. The profile answers only its own
		// dimensions; every other question goes to the planner with that
		// organization's mail. A mailbox-wide priority list would answer a
		// different question.
		view.Intent = IntentFreeform
		cached, gen, key, ok := s.cachedFreeform(view, question, language, preview, turnKey)
		if ok {
			return cached, nil
		}
		return s.answerFreeform(ctx, view, question, language, preview, org, nil, turns, turnKey, gen, key)
	}
}

// companyScope is the one organization-linking decision: one named
// organization scopes the question, two or more ask which one, and none
// leaves the question mailbox-wide. Auto, Local and Remote all call it.
func (s *Service) companyScope(ctx context.Context, question, language string) (*world.CompanyRow, *mailquery.Clarification, error) {
	orgs, err := s.companyCandidates(ctx, question)
	if err != nil {
		return nil, nil, err
	}
	if len(orgs) == 1 {
		org := orgs[0]
		return &org, nil, nil
	}
	return nil, companyClarification(question, language, orgs), nil
}

// answerLocal answers without any provider call. Every deterministic owner
// keeps its existing behavior (sender lookup, structured ranking, company
// profile, clarification); a free-form question that would need a written
// answer returns the retrieved sources with an explicit local notice. A
// recognized count cue never reaches this function: answerCount runs the
// scope decision for every route, and only a rejected count follows the
// Local freeform path.
func (s *Service) answerLocal(ctx context.Context, view AskView, question, language, intent, senderText string) (AskView, error) {
	view.Route = RouteLocal
	switch intent {
	case IntentSender:
		view, err := s.askSender(ctx, view, senderText)
		view.Route = RouteLocal
		return view, err
	case IntentSummaryToday:
		view, err := s.askToday(ctx, view)
		view.Route = RouteLocal
		return view, err
	case IntentFreeform:
		view, err := s.answerLocalFreeform(ctx, view, question, language)
		view.Route = RouteLocal
		return view, err
	default:
		return s.answerLocalStructured(ctx, view, question, language, intent)
	}
}

// answerCount is the one count owner shared by every route. A question with a
// recognized count cue first passes the providerless scope decision
// (providerlessCountIntent), which runs with or without a configured
// generator: an accepted plain count is answered from the priority totals or
// the topic census with no provider call, and a rejected count — a person,
// organization, date, act or any other unlisted scope — follows that route's
// freeform path. Without this owner, a count that parseIntent maps to a
// structured category would let Auto, Local and Remote answer a mailbox-wide
// total for a scoped question.
func (s *Service) answerCount(ctx context.Context, view AskView, route Route, question, language string, preview bool,
	turns []store.QAHistory, turnKey string, remoteGen Generator) (AskView, error) {
	countIntent, phrase, ok := s.providerlessCountIntent(ctx, question)
	switch {
	case ok && countIntent == IntentTopic:
		view.Intent = IntentTopic
		return s.askTopic(ctx, view, phrase)
	case ok:
		view.Intent = countIntent
		return s.askStructured(ctx, view, countIntent)
	}
	view.Intent = IntentFreeform
	switch route {
	case RouteLocal:
		return s.answerLocalFreeform(ctx, view, question, language)
	case RouteRemote:
		return s.answerRemote(ctx, view, question, language, IntentFreeform, "", turns, remoteGen)
	default:
		return s.askFreeform(ctx, view, question, language, preview, turns, turnKey)
	}
}

// providerlessCountIntent returns the local, generator-free owner of a plain
// count question: the priority category total or the catalogue topic count
// its wording names, with the strict mailbox-scope gate already passed. ok is
// false when the question keeps its freeform path: no count cue, no accepted
// owner, a named organization or person, or a failed scope read. Priority
// intents are tried before the topic table, so a topic can never shadow a
// reply, urgent, deadline, security, decision or workload count.
func (s *Service) providerlessCountIntent(ctx context.Context, question string) (string, subject.Phrase, bool) {
	if !aggregateCountRequest(mail.Fold(strings.TrimSpace(question))) {
		return "", subject.Phrase{}, false
	}
	if structured, ok := aggregateStructuredIntent(question); ok {
		if s.providerlessCountScopeAllowed(ctx, question) {
			return structured, subject.Phrase{}, true
		}
		return "", subject.Phrase{}, false
	}
	if phrase, _, ok := aggregateTopicIntent(question); ok && s.providerlessCountScopeAllowed(ctx, question) {
		return IntentTopic, phrase, true
	}
	return "", subject.Phrase{}, false
}

// answerLocalStructured keeps the company scope rule: one named organization
// scopes the answer, two or more ask which one, and a mailbox-wide structured
// intent uses the priority ranking. A specific fact about one organization is
// retrieved locally instead of being widened to the mailbox.
func (s *Service) answerLocalStructured(ctx context.Context, view AskView, question, language, intent string) (AskView, error) {
	org, clarify, err := s.companyScope(ctx, question, language)
	if err != nil {
		return AskView{}, err
	}
	if clarify != nil {
		return clarifyView(view, clarify), nil
	}
	if org != nil {
		if companyProfileQuestion(question, *org) {
			view, err = s.askCompany(ctx, view, *org, question, language)
		} else {
			view, err = s.localRetrieval(ctx, view, question, org)
		}
	} else {
		view, err = s.askStructured(ctx, view, intent)
	}
	view.Route = RouteLocal
	return view, err
}

// answerLocalFreeform resolves the same entity and organization scope as the
// ordinary free-form path, but never reaches the planner. An exact contact
// answer and a company profile are deterministic; everything else is local
// retrieval.
func (s *Service) answerLocalFreeform(ctx context.Context, view AskView, question, language string) (AskView, error) {
	org, clarify, err := s.companyScope(ctx, question, language)
	if err != nil {
		return AskView{}, err
	}
	if clarify != nil {
		return clarifyView(view, clarify), nil
	}
	if org != nil && companyProfileQuestion(question, *org) {
		return s.askCompany(ctx, view, *org, question, language)
	}
	entities := s.resolveQuestionEntities(ctx, question)
	if len(entities) > 1 && !pluralEntityQuestion(question) {
		if clarify := entityClarification(question, language, entities); clarify != nil {
			return clarifyView(view, clarify), nil
		}
	}
	if len(entities) == 1 {
		if contact, ok := s.contactAnswerFor(view, &entities[0], question); ok {
			return contact, nil
		}
	}
	return s.localRetrieval(ctx, view, question, org)
}

// localRetrieval returns the sources a local answer can offer without
// inventing prose. A warming or timed-out index keeps its own transient
// notice; an empty result is a no-match.
func (s *Service) localRetrieval(ctx context.Context, view AskView, question string, org *world.CompanyRow) (AskView, error) {
	msgs, _, _, err := s.plannerContext(ctx, question, org)
	if err != nil {
		if notice, reason, degraded := retrievalNotice(err); degraded {
			s.log.Warn("qa local retrieval degraded", "notice", notice, "reason", reason)
			return retrieval(view, notice, nil, "", ""), nil
		}
		return AskView{}, err
	}
	if len(msgs) == 0 {
		return noMatches(view), nil
	}
	return retrieval(view, NoticeLocalNoAnswer, msgs, "", ""), nil
}

// askToday answers a today question from the mailbox itself. It replaces the
// priority-wide counts an earlier summary used: "today" must count messages
// received today in the owner's zone, not the whole mailbox's attention set.
func (s *Service) askToday(ctx context.Context, view AskView) (AskView, error) {
	result, msgs, err := s.todayEvidence(ctx)
	if err != nil {
		return AskView{}, err
	}
	view.Mode, view.Intent, view.Answer, view.Notice = ModeStructured, IntentSummaryToday, "", ""
	view.Counts = map[string]int{"total": result.Total}
	view.Citations = make([]Citation, 0, len(msgs))
	for _, m := range msgs {
		view.Citations = append(view.Citations, Citation{MessageID: m.ID, Subject: m.Subject,
			SenderName: m.SenderName, ReceivedAt: m.ReceivedAt, Reason: "today"})
	}
	return view, nil
}

// todayEvidence reads the newest received messages of the owner's current
// day. Rows are the citation window; Total is the whole day.
func (s *Service) todayEvidence(ctx context.Context) (mailquery.Result, []contextMessage, error) {
	return s.todayContext(ctx, todayCitationLimit)
}

// todayContext reads the owner's current day as planner evidence. The
// structured answer uses a short citation window; freeform retrieval asks
// for the full planner budget. Both share todayPlan, so they cannot describe
// different days.
func (s *Service) todayContext(ctx context.Context, limit int) (mailquery.Result, []contextMessage, error) {
	resolved, loc, err := s.todayPlan(limit)
	if err != nil {
		return mailquery.Result{}, nil, err
	}
	result, err := s.store.ExecuteMailQuery(ctx, resolved, loc)
	if err != nil {
		return mailquery.Result{}, nil, err
	}
	msgs, err := s.contextForRows(ctx, result.Rows, "today")
	if err != nil {
		return mailquery.Result{}, nil, err
	}
	return result, msgs, nil
}

// todayPlan resolves the owner's current day once for every "today" read.
// The structured answer consumes the rows; freeform retrieval consumes the
// same half-open window, so the two can never describe different days.
func (s *Service) todayPlan(limit int) (mailquery.Plan, *time.Location, error) {
	plan := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Direction: mailquery.DirectionReceived, TimeRange: mailquery.TimeToday,
		Limit: limit, Order: mailquery.OrderDesc}.Normalize()
	loc := s.zone()
	resolved, _, err := plan.Resolve(s.clock.Now(), loc)
	if err != nil {
		return mailquery.Plan{}, loc, err
	}
	return resolved, loc, nil
}

// todayBounds returns the owner-day window used by freeform retrieval. It
// reuses todayPlan so the window has one owner.
func (s *Service) todayBounds() (time.Time, time.Time, error) {
	plan, _, err := s.todayPlan(todayCitationLimit)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	since, ok := mail.Parse(plan.Since)
	if !ok {
		return time.Time{}, time.Time{}, fmt.Errorf("qa: today window since %q is not a stored instant", plan.Since)
	}
	until, ok := mail.Parse(plan.Until)
	if !ok {
		return time.Time{}, time.Time{}, fmt.Errorf("qa: today window until %q is not a stored instant", plan.Until)
	}
	return since, until, nil
}

// todayCitationLimit bounds the visible sources of a today answer. The count
// still reports the full day.
const todayCitationLimit = 5

// contextForRows turns typed list rows into numbered prompt evidence. Only
// stored owner-safe summaries are read; a missing row is absent, never a
// fabricated message.
func (s *Service) contextForRows(ctx context.Context, rows []mailquery.Row, reason string) ([]contextMessage, error) {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.MessageID > 0 {
			ids = append(ids, row.MessageID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	sums, err := s.store.SummariesOwner(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]contextMessage, 0, len(rows))
	for _, row := range rows {
		sum, ok := sums[row.MessageID]
		if !ok {
			continue
		}
		out = append(out, contextMessage{ID: row.MessageID, Subject: sum.Subject,
			SenderName: sum.SenderName, SenderAddress: sum.SenderAddress, ReceivedAt: sum.Instant,
			Sent: sum.Role == mail.RoleSent, Body: sum.Body, Reason: reason})
	}
	return out, nil
}

// answerRemote writes every answer with the one configured provider. The
// route is resolved before this call, so a missing generator is a typed
// unavailability error, never a quiet fallback to a structured answer.
func (s *Service) answerRemote(ctx context.Context, view AskView, question, language, intent, senderText string,
	turns []store.QAHistory, gen Generator) (AskView, error) {
	view.Route = RouteRemote
	view.Provider, view.Model = gen.Name(), gen.Model()
	var (
		msgs    []contextMessage
		threads []threadEvidence
		facts   *organizationFacts
		counts  map[string]int
	)
	switch intent {
	case IntentSender:
		evidence, err := s.senderEvidence(ctx, senderText)
		if err != nil {
			return AskView{}, err
		}
		if evidence.notice != "" {
			return retrieval(view, evidence.notice, nil, view.Provider, view.Model), nil
		}
		if !evidence.known {
			return retrieval(view, NoticeSenderUnknown, nil, view.Provider, view.Model), nil
		}
		if len(evidence.rows) == 0 {
			return noMatches(view), nil
		}
		if msgs, err = s.contextForRows(ctx, evidence.rows, "sender"); err != nil {
			return AskView{}, err
		}
		counts = map[string]int{"sender": len(evidence.rows)}
	case IntentSummaryToday:
		result, today, err := s.todayEvidence(ctx)
		if err != nil {
			return AskView{}, err
		}
		if result.Total == 0 {
			return noMatches(view), nil
		}
		msgs = today
		counts = map[string]int{"today": result.Total}
	default:
		if intent == IntentFreeform {
			org, clarify, err := s.companyScope(ctx, question, language)
			if err != nil {
				return AskView{}, err
			}
			if clarify != nil {
				return clarifyView(view, clarify), nil
			}
			entities := s.resolveQuestionEntities(ctx, question)
			if len(entities) > 1 && !pluralEntityQuestion(question) {
				if clarify := entityClarification(question, language, entities); clarify != nil {
					return clarifyView(view, clarify), nil
				}
			}
			msgs, threads, facts, err = s.plannerContext(ctx, question, org)
			if err != nil {
				if notice, reason, degraded := retrievalNotice(err); degraded {
					s.log.Warn("qa remote retrieval degraded", "notice", notice, "reason", reason)
					return retrieval(view, notice, nil, view.Provider, view.Model), nil
				}
				return AskView{}, err
			}
		} else {
			var err error
			counts, err = s.priorityCounts(ctx)
			if err != nil {
				return AskView{}, err
			}
			items, _, _, err := s.prio.Rank(ctx, intentFilter(intent), 5)
			if err != nil {
				return AskView{}, err
			}
			rows := rankRows(items)
			if msgs, err = s.contextForRows(ctx, rows, string(intentFilter(intent))); err != nil {
				return AskView{}, err
			}
			view.Counts = counts
		}
	}
	if len(msgs) == 0 && facts == nil {
		return noMatches(view), nil
	}
	owner, err := s.store.OwnerIdentity(ctx)
	if err != nil {
		s.log.Warn("qa owner identity unavailable", "reason", errorClass(err))
		owner = store.OwnerIdentity{}
	}
	system, user := remotePrompt(question, language, intent, counts, facts, threads, turns, msgs, owner,
		s.clock.Now(), s.zoneName())
	callCtx, cancel := s.generationContext(ctx)
	defer cancel()
	genRes, err := gen.Generate(callCtx, system, user)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return AskView{}, ctx.Err()
		}
		s.log.Warn("qa remote answer failed", "notice", NoticeGenerationFailed,
			"provider", view.Provider, "model", view.Model, "reason", errorClass(err))
		return retrieval(view, NoticeGenerationFailed, msgs, view.Provider, view.Model), nil
	}
	text, unwrapped := unwrapAnswerEnvelope(genRes.Content)
	var answer string
	var citations []Citation
	if unwrapped {
		answer, citations = groundAnswer(text, msgs, nil)
	}
	if strings.TrimSpace(answer) == "" || len(citations) == 0 {
		s.log.Warn("qa remote answer not grounded", "notice", NoticeNoGroundedAnswer,
			"provider", view.Provider, "model", view.Model)
		return retrieval(view, NoticeNoGroundedAnswer, msgs, view.Provider, view.Model), nil
	}
	view.Mode, view.Answer, view.Citations = ModeGenerated, answer, citations
	if counts != nil {
		view.Counts = counts
	}
	return s.finalizeGeneratedAnswer(view, language), nil
}

// rankRows is the prompt projection of the ranked priority items: the same
// message ids, subjects, senders and instants the structured citations show.
func rankRows(items []priority.Item) []mailquery.Row {
	out := make([]mailquery.Row, 0, len(items))
	for _, it := range items {
		out = append(out, mailquery.Row{MessageID: it.MessageID, Subject: it.Subject,
			SenderName: it.SenderName, Instant: it.ReceivedAt})
	}
	return out
}

// remotePrompt is the one prose contract of the Remote route. It carries the
// computed facts separately from the numbered messages so the model can
// narrate them without recomputing a count.
func remotePrompt(question, language, intent string, counts map[string]int, facts *organizationFacts,
	threads []threadEvidence, turns []store.QAHistory, msgs []contextMessage,
	owner store.OwnerIdentity, now time.Time, zone string) (system, user string) {
	languageTag := languageName(language)
	system = `You are Mailsense's mail answer writer. Answer the owner's question about their mailbox.
` + ownerPromptLine(owner) + `
Rules:
- Answer in ` + languageTag + `.
- Use only the Facts block and the numbered messages. Cite every factual claim with [n].
- The counts in the Facts block are authoritative. Never recompute, round or contradict them.
- Never invent a subject, sender, date or number.
- Answer in at most 120 words, as plain text. Do not use a markdown table.
- Write naturally and plainly, in short sentences and sentence case. Do not use em dashes or double hyphens.
- Return only the answer text. Never wrap it in JSON, quotes or a code fence.
- If the evidence does not answer the question, say so and cite the closest message.
`
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(oneLine(question))
	b.WriteString("\nLanguage: ")
	b.WriteString(languageTag)
	b.WriteString("\nToday: ")
	b.WriteString(now.Format("2006-01-02"))
	b.WriteString("\nOwner time zone: ")
	b.WriteString(zone)
	b.WriteString("\nRoute: remote (the owner explicitly asked for a written model answer)\n")
	b.WriteString("\nFacts:\n")
	switch intent {
	case IntentSummaryToday:
		fmt.Fprintf(&b, "- Messages received today: %d\n", counts["today"])
		if len(msgs) > 0 {
			fmt.Fprintf(&b, "- The newest %d are cited below; the count covers the whole day.\n", len(msgs))
		}
	case IntentSender:
		fmt.Fprintf(&b, "- Messages matching the sender: %d\n", counts["sender"])
	default:
		if len(counts) > 0 {
			b.WriteString("- Priority counts for the whole mailbox:\n")
			for _, key := range []string{"total", "reply", "urgent", "security", "decision", "deadline", "workload"} {
				if n, ok := counts[key]; ok {
					fmt.Fprintf(&b, "  - %s: %d\n", key, n)
				}
			}
		}
	}
	if facts != nil {
		writeOrganizationFacts(&b, facts)
	}
	if len(threads) > 0 {
		b.WriteString("\nThread states (deterministic; newest first):\n")
		for _, t := range threads {
			fmt.Fprintf(&b, "- %q: %s", oneLine(t.Subject), t.State)
			if t.OwnerLastAt != "" {
				fmt.Fprintf(&b, "; last move %s", t.OwnerLastAt)
			}
			if t.Visibility != "" {
				fmt.Fprintf(&b, "; evidence %s", t.Visibility)
			}
			b.WriteString("\n")
		}
	}
	if block := conversationBlock(turns); block != "" {
		b.WriteString("\n")
		b.WriteString(block)
	}
	b.WriteString("\nMessages (numbered; may be empty):\n")
	writeContextMessages(&b, msgs)
	return system, b.String()
}
