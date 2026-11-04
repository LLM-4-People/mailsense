package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/world"
)

// askStructured answers a common question from the priority ranking: no
// provider call, no invented prose (the UI composes the localized text).
func (s *Service) askStructured(ctx context.Context, view AskView, intent string) (AskView, error) {
	filter := intentFilter(intent)
	counts, err := s.priorityCounts(ctx)
	if err != nil {
		return AskView{}, err
	}
	items, _, _, err := s.prio.Rank(ctx, filter, 5)
	if err != nil {
		return AskView{}, err
	}
	view.Mode, view.Intent, view.Answer = ModeStructured, intent, ""
	view.Counts = counts
	view.Citations = citationsFor(items, filter)
	return view, nil
}

// askSender answers a sender question with an exact sender lookup. The
// question names a person or address; the answer cites only messages whose
// sender identity matches it, never a body or recipient mention of the name.
// It stays structured: no generated summary is invented.
func (s *Service) askSender(ctx context.Context, view AskView, text string) (AskView, error) {
	evidence, err := s.senderEvidence(ctx, text)
	if err != nil {
		return AskView{}, err
	}
	if evidence.notice != "" {
		return retrieval(view, evidence.notice, nil, "", ""), nil
	}
	if !evidence.known {
		return retrieval(view, NoticeSenderUnknown, nil, "", ""), nil
	}
	view.Mode, view.Intent, view.Answer = ModeStructured, IntentSender, ""
	view.Counts = map[string]int{}
	view.Citations = make([]Citation, 0, len(evidence.rows))
	for _, row := range evidence.rows {
		view.Citations = append(view.Citations, Citation{MessageID: row.MessageID, Subject: row.Subject,
			SenderName: row.SenderName, ReceivedAt: row.Instant, Reason: "sender"})
	}
	return view, nil
}

// senderEvidence is the one exact-sender read shared by the structured and
// remote routes. known is false only when the mailbox cannot resolve the
// named sender; a resolved sender with no mail returns known and no rows.
// notice names a transient retrieval failure instead of pretending it was an
// empty sender.
type senderEvidence struct {
	known  bool
	rows   []mailquery.Row
	notice string
}

func (s *Service) senderEvidence(ctx context.Context, text string) (senderEvidence, error) {
	addresses, err := s.senderAddresses(ctx, text)
	if err != nil {
		return senderEvidence{}, err
	}
	if len(addresses) == 0 {
		return senderEvidence{}, nil
	}
	retrievalCtx, cancel := s.retrievalContext(ctx)
	defer cancel()
	loc := s.zone()
	rows := make([]mailquery.Row, 0, senderEvidenceLimit)
	seen := make(map[int64]bool, senderEvidenceLimit)
	for _, address := range addresses {
		plan := mailquery.Plan{
			Kind: mailquery.KindMessages, Action: mailquery.ActionList,
			Direction: mailquery.DirectionReceived, ContactAddresses: []string{address},
			Limit: senderEvidenceLimit,
		}.Normalize()
		res, err := s.store.ExecuteMailQuery(retrievalCtx, plan, loc)
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() == nil {
				return senderEvidence{}, err
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(retrievalCtx.Err(), context.DeadlineExceeded) {
				s.log.Warn("qa sender lookup degraded", "notice", NoticeRetrievalTimeout, "reason", "retrieval_timeout")
				return senderEvidence{known: true, notice: NoticeRetrievalTimeout}, nil
			}
			return senderEvidence{}, err
		}
		for _, row := range res.Rows {
			if row.MessageID == 0 || seen[row.MessageID] {
				continue
			}
			seen[row.MessageID] = true
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Instant > rows[j].Instant })
	if len(rows) > senderEvidenceLimit {
		rows = rows[:senderEvidenceLimit]
	}
	return senderEvidence{known: true, rows: rows}, nil
}

// senderEvidenceLimit bounds one sender answer, as the ranked intents do.
const senderEvidenceLimit = 5

// maxSenderAddresses bounds the addresses one sender question resolves to.
// A common first name can belong to many people; the newest and busiest few
// are evidence, the rest is a clarification the owner did not ask for.
const maxSenderAddresses = 4

// senderAddresses resolves the text after a sender prefix to exact
// addresses. An address is used as-is; a name is matched against the people
// the mailbox already knows, and only candidates whose name or address
// contains every folded term are accepted.
func (s *Service) senderAddresses(ctx context.Context, text string) ([]string, error) {
	folded := strings.TrimSpace(mail.Fold(text))
	if folded == "" {
		return nil, nil
	}
	if strings.Contains(folded, "@") {
		return []string{folded}, nil
	}
	terms := strings.Fields(folded)
	if len(terms) == 0 {
		return nil, nil
	}
	rows, err := s.store.PersonCandidates(ctx, terms, 50)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		address  string
		messages int
		last     string
	}
	byAddress := make(map[string]*candidate, len(rows))
	for _, row := range rows {
		address := mail.NormalizeAddress(row.Address)
		if address == "" {
			continue
		}
		name := mail.Fold(row.Name)
		local, domain := localPart(address), mail.AddressDomain(address)
		matches := true
		for _, term := range terms {
			if !strings.Contains(name, term) && !strings.Contains(local, term) && !strings.Contains(domain, term) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		c := byAddress[address]
		if c == nil {
			c = &candidate{address: address}
			byAddress[address] = c
		}
		c.messages += row.Messages
		if row.LastAt > c.last {
			c.last = row.LastAt
		}
	}
	out := make([]*candidate, 0, len(byAddress))
	for _, c := range byAddress {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].messages != out[j].messages {
			return out[i].messages > out[j].messages
		}
		if out[i].last != out[j].last {
			return out[i].last > out[j].last
		}
		return out[i].address < out[j].address
	})
	if len(out) > maxSenderAddresses {
		out = out[:maxSenderAddresses]
	}
	addresses := make([]string, len(out))
	for i, c := range out {
		addresses[i] = c.address
	}
	return addresses, nil
}

// Company shortcut bounds. The visible organization book is read at its own
// maximum so every known organization is a linking candidate, and the
// profile window is the Companies screen's default 12 months and 30 days.
const (
	companyListLimit     = world.MaxCompanyLimit
	companyProfileMonths = world.DefaultCompanyMonths
	companyProfileDays   = world.DefaultCompanyEventDays
	maxCompanyCitations  = 8
)

// companyCandidates returns the organization(s) the question names, ordered
// by evidence. It is entity linking over the organization book (labels and
// domains), not a phrase table: a folded, token-boundary match is an entity
// mention. A clarification answer repeats the chosen organization's label and
// domain, so a strictly higher evidence score selects it and lets the
// one-click option resolve. A book failure is an error, never a silent
// widening to the whole mailbox.
func (s *Service) companyCandidates(ctx context.Context, question string) ([]world.CompanyRow, error) {
	profiles := s.companyProfiles()
	if profiles == nil {
		return nil, nil
	}
	res, err := profiles.List(ctx, world.ListQuery{Limit: companyListLimit})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.log.Warn("qa company book unavailable", "reason", errorClass(err))
		return nil, err
	}
	matches := matchCompanies(question, res.Companies)
	if len(matches) > 1 && matches[0].Evidence > matches[1].Evidence {
		// The chosen clarification option carries strictly more evidence
		// than the other names. This is the same evidence rule the free-form
		// resolver uses.
		top := matches[0].Evidence
		kept := matches[:0]
		for _, match := range matches {
			if match.Evidence == top {
				kept = append(kept, match)
			}
		}
		matches = kept
	}
	orgs := make([]world.CompanyRow, 0, len(matches))
	for _, match := range matches {
		orgs = append(orgs, match.Org)
	}
	return orgs, nil
}

// compactWords folds and splits text into letter/digit-only words. Splitting
// on punctuation keeps "clinique-exemple.test" as one word and "Clinique Exemple" as two.
func compactWords(s string) []string {
	folded := mail.Fold(strings.TrimSpace(s))
	fields := strings.FieldsFunc(folded, func(r rune) bool { return !mail.IsWordRune(r) })
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		if token := mail.CompactFold(field); token != "" {
			words = append(words, token)
		}
	}
	return words
}

// containsCompactPhrase reports whether the words contain target as one
// whole word or as a contiguous run of whole words. The run length is
// bounded by the target's length, so a long question stays linear.
func containsCompactPhrase(words []string, target string) bool {
	if target == "" {
		return false
	}
	for i := 0; i < len(words); i++ {
		joined := ""
		for j := i; j < len(words); j++ {
			joined += words[j]
			if joined == target {
				return true
			}
			if len(joined) >= len(target) {
				break
			}
		}
	}
	return false
}

// askCompany answers one known organization from its cached deep profile:
// no search, no planner and no model call. It is used only when the
// question is about the organization itself (companyProfileQuestion), never
// for a specific fact that merely mentions it. Every number and name comes
// from the profile view; a profile failure is an explicit notice, never a
// silent fallback to unrelated mail.
func (s *Service) askCompany(ctx context.Context, view AskView, org world.CompanyRow, question, language string) (AskView, error) {
	profiles := s.companyProfiles()
	view.Mode, view.Intent = ModeComputed, IntentCompany
	view.Provider, view.Model, view.Notice = "", "", ""
	view.Counts, view.Citations = map[string]int{}, []Citation{}
	if profiles == nil {
		view.Notice = NoticeCompanyUnavailable
		return view, nil
	}
	profile, err := profiles.Profile(ctx, org.Key, companyProfileMonths, companyProfileDays)
	if err != nil {
		if ctx.Err() != nil {
			return AskView{}, ctx.Err()
		}
		s.log.Warn("qa company profile unavailable", "reason", errorClass(err))
		view.Notice = NoticeCompanyUnavailable
		return view, nil
	}
	view.Answer = companyAnswer(language, profile, org, s.zone(), s.clock.Now())
	view.Counts = companyCounts(profile)
	if profile.Messages > 0 {
		view.Citations = companyCitations(profile)
	}
	return view, nil
}

// companyAnswer composes the short localized answer from profile fields only.
// The label is the profile's, with the linked row as a fallback; no company
// key, state enum or model text ever reaches it.
func companyAnswer(language string, profile world.ProfileView, org world.CompanyRow, loc *time.Location, now time.Time) string {
	label := companyLabel(profile, org)
	if profile.Messages == 0 {
		if isFrench(language) {
			if label == "" {
				return "Fiche du répertoire, sans message échangé."
			}
			return label + " est une fiche du répertoire, sans message échangé."
		}
		if label == "" {
			return "Directory entry with no mail."
		}
		return label + " is a directory entry with no mail."
	}

	parts := make([]string, 0, 5)
	if isFrench(language) {
		base := fmt.Sprintf("%s, %s, %s.", countItems(profile.Messages, "message échangé", "messages échangés"),
			countItems(profile.PeopleContacted, "personne", "personnes"),
			countItems(profile.Threads, "conversation", "conversations"))
		if label != "" {
			base = "Avec " + label + " : " + base
		}
		parts = append(parts, base)
		if open := companyOpenFR(profile.ThreadStates); open != "" {
			parts = append(parts, open)
		}
		if date := companyDate(language, profile.LastAt, loc); date != "" {
			parts = append(parts, "Dernier contact le "+date+".")
		}
		if overdue, upcoming := companyDeadlineCounts(profile.Deadlines, now); overdue+upcoming > 0 {
			parts = append(parts, companyDeadlinesFR(overdue, upcoming))
		}
		if n := len(profile.Events); n > 0 {
			parts = append(parts, countItems(n, "rendez-vous à venir", "rendez-vous à venir")+".")
		}
	} else {
		base := fmt.Sprintf("%s, %s, %s.", countItems(profile.Messages, "message exchanged", "messages exchanged"),
			countItems(profile.PeopleContacted, "person", "people"),
			countItems(profile.Threads, "conversation", "conversations"))
		if label != "" {
			base = "With " + label + ": " + base
		}
		parts = append(parts, base)
		if open := companyOpenEN(profile.ThreadStates); open != "" {
			parts = append(parts, open)
		}
		if date := companyDate(language, profile.LastAt, loc); date != "" {
			parts = append(parts, "Last contact on "+date+".")
		}
		if overdue, upcoming := companyDeadlineCounts(profile.Deadlines, now); overdue+upcoming > 0 {
			parts = append(parts, companyDeadlinesEN(overdue, upcoming))
		}
		if n := len(profile.Events); n > 0 {
			parts = append(parts, countItems(n, "upcoming event", "upcoming events")+".")
		}
	}
	return strings.Join(parts, " ")
}

// companyDeadlineCounts splits the profile's deadline window into what is
// already late and what is still ahead. The profile reads 30 days back for
// deadlines, so narrating every row as "upcoming" would misstate overdue
// commitments.
func companyDeadlineCounts(deadlines []world.Deadline, now time.Time) (overdue, upcoming int) {
	for _, d := range deadlines {
		if !d.Due.IsZero() && d.Due.Before(now) {
			overdue++
			continue
		}
		upcoming++
	}
	return overdue, upcoming
}

func companyDeadlinesFR(overdue, upcoming int) string {
	late := countItems(overdue, "échéance en retard", "échéances en retard")
	ahead := countItems(upcoming, "échéance à venir", "échéances à venir")
	switch {
	case overdue > 0 && upcoming > 0:
		return late + ", " + ahead + "."
	case overdue > 0:
		return late + "."
	default:
		return ahead + "."
	}
}

func companyDeadlinesEN(overdue, upcoming int) string {
	late := countItems(overdue, "overdue commitment", "overdue commitments")
	ahead := countItems(upcoming, "upcoming commitment", "upcoming commitments")
	switch {
	case overdue > 0 && upcoming > 0:
		return late + ", " + ahead + "."
	case overdue > 0:
		return late + "."
	default:
		return ahead + "."
	}
}

// companyLabel is the display name only: profile label, linked row label,
// then a domain from either view. The company key is never displayed.
func companyLabel(profile world.ProfileView, org world.CompanyRow) string {
	for _, candidate := range []string{profile.Label, org.Label} {
		if label := strings.TrimSpace(candidate); label != "" {
			return label
		}
	}
	for _, domains := range [][]string{profile.Domains, org.Domains} {
		for _, domain := range domains {
			if label := strings.TrimSpace(domain); label != "" {
				return label
			}
		}
	}
	return ""
}

// companyOpenFR describes the two open states in the profile's own counts.
// A zero state is omitted rather than printed as a sentence about nothing.
func companyOpenFR(states map[string]int) string {
	owner, other := states[string(threadintel.AwaitingOwner)], states[string(threadintel.AwaitingOther)]
	switch {
	case owner > 0 && other > 0:
		return waitingFR(owner) + " votre réponse, " + waitingTailFR(other) + " la leur."
	case owner > 0:
		return waitingFR(owner) + " votre réponse."
	case other > 0:
		return waitingFR(other) + " leur réponse."
	}
	return ""
}

func companyOpenEN(states map[string]int) string {
	owner, other := states[string(threadintel.AwaitingOwner)], states[string(threadintel.AwaitingOther)]
	switch {
	case owner > 0 && other > 0:
		return waitingEN(owner) + " waiting on your reply, " + waitingTailEN(other) + " waiting on theirs."
	case owner > 0:
		return waitingEN(owner) + " waiting on your reply."
	case other > 0:
		return waitingEN(other) + " waiting on their reply."
	}
	return ""
}

func waitingFR(n int) string {
	if n == 1 {
		return "1 conversation attend"
	}
	return fmt.Sprintf("%d conversations attendent", n)
}

func waitingTailFR(n int) string {
	if n == 1 {
		return "1 attend"
	}
	return fmt.Sprintf("%d attendent", n)
}

func waitingEN(n int) string {
	if n == 1 {
		return "1 conversation is"
	}
	return fmt.Sprintf("%d conversations are", n)
}

func waitingTailEN(n int) string {
	if n == 1 {
		return "1 is"
	}
	return fmt.Sprintf("%d are", n)
}

func countItems(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// companyDate is the profile's last-contact day in the owner's zone. An
// unparseable instant is omitted instead of being printed raw or guessed.
func companyDate(language, instant string, loc *time.Location) string {
	at, ok := parseInstant(instant, loc)
	if !ok {
		return ""
	}
	if isFrench(language) {
		return at.Format("02/01/2006")
	}
	return at.Format("2006-01-02")
}

// companyDay is companyDate without the language-specific order, for the one
// compact profile line the planner reads.
func companyDay(instant string, loc *time.Location) string {
	at, ok := parseInstant(instant, loc)
	if !ok {
		return ""
	}
	return at.Format("2006-01-02")
}

// parseInstant reads a stored mail instant in the owner's zone. An empty or
// unparseable value is not a fact and is omitted by every caller.
func parseInstant(instant string, loc *time.Location) (time.Time, bool) {
	instant = strings.TrimSpace(instant)
	if instant == "" {
		return time.Time{}, false
	}
	at, ok := mail.Parse(instant)
	if !ok {
		parsed, err := time.Parse(time.RFC3339, instant)
		if err != nil {
			return time.Time{}, false
		}
		at = parsed
	}
	if loc != nil {
		at = at.In(loc)
	}
	return at, true
}

// companyCounts are the five numbers the answer and the screen share. The
// two open states come from the profile's thread-state counts, never from a
// second derivation of who owes a reply.
func companyCounts(profile world.ProfileView) map[string]int {
	return map[string]int{
		"messages":       profile.Messages,
		"people":         profile.PeopleContacted,
		"threads":        profile.Threads,
		"awaiting_owner": profile.ThreadStates[string(threadintel.AwaitingOwner)],
		"awaiting_other": profile.ThreadStates[string(threadintel.AwaitingOther)],
	}
}

// companyCitations cites the profile's recent messages first, then the open
// conversations. Message IDs are deduplicated and capped; a missing id or
// metadata field is never invented.
func companyCitations(profile world.ProfileView) []Citation {
	out := make([]Citation, 0, maxCompanyCitations)
	seen := make(map[int64]bool, maxCompanyCitations)
	add := func(id int64, subject, sender, instant, reason string) {
		if id <= 0 || seen[id] || len(out) >= maxCompanyCitations {
			return
		}
		seen[id] = true
		out = append(out, Citation{MessageID: id, Subject: subject, SenderName: sender, ReceivedAt: instant, Reason: reason})
	}
	for _, m := range profile.Recent {
		add(m.MessageID, m.Subject, m.SenderName, m.Instant, "recent")
	}
	for _, thread := range profile.OpenThreads {
		add(thread.MessageID, thread.Subject, "", thread.LastAt, "")
	}
	return out
}

// generatorAvailable reports whether a Q&A generator is configured. It is a
// local catalog lookup: an empty selector, an unknown record or a missing key
// is unavailable without a provider call. The provider-less count fallback
// uses it to stay off the computed path when a generator can still answer.
func (s *Service) generatorAvailable() bool {
	if s.source == nil {
		return false
	}
	gen, err := s.source.Generator()
	return err == nil && gen != nil
}

// providerlessCountScopeAllowed reports whether a plain provider-less count
// owns the whole mailbox scope: no organization and no person is named, and
// both optional reads succeeded. A company-book or person-candidate failure
// returns false, so the question keeps the existing freeform behavior instead
// of guessing a mailbox-wide total.
func (s *Service) providerlessCountScopeAllowed(ctx context.Context, question string) bool {
	orgs, err := s.companyCandidates(ctx, question)
	if err != nil {
		s.log.Warn("qa providerless count scope deferred", "reason", errorClass(err))
		return false
	}
	if len(orgs) > 0 {
		return false
	}
	person, err := s.questionNamesPerson(ctx, question)
	if err != nil {
		s.log.Warn("qa providerless count scope deferred", "reason", errorClass(err))
		return false
	}
	return !person
}

// questionNamesPerson reports a person the question names, or the resolver
// error. A failed person read is not "no person": the caller must keep the
// question freeform so a person-scoped count cannot answer a mailbox total.
// The plain-count wording resolves with the count cues removed: a cue word is
// not a name, so "Amount of deadlines" cannot match a sender named Paramount
// and "Count of deadlines" cannot match Accounting.
func (s *Service) questionNamesPerson(ctx context.Context, question string) (bool, error) {
	entities, err := s.resolveEntityMentionsChecked(ctx, aggregateCountEntityMentions(question), question, true)
	if err != nil {
		return false, err
	}
	for _, entity := range entities {
		if entity.Kind == entityPerson {
			return true, nil
		}
	}
	return false, nil
}

// askFreeform resolves the person or organization the question names before
// the planner runs, then answers through the shared freeform path. The answer
// cache is checked first, so a repeated question never reads the entity book;
// a clarification is never cached. The prior turns and turnKey belong to the
// thread this question continues; both are empty for a brand-new one.
func (s *Service) askFreeform(ctx context.Context, view AskView, question, language string, preview bool,
	turns []store.QAHistory, turnKey string) (AskView, error) {
	cached, gen, key, ok := s.cachedFreeform(view, question, language, preview, turnKey)
	if ok {
		return cached, nil
	}
	var entities []resolvedEntity
	// A recognized count that was forced onto the freeform path resolves
	// through the same cue-filtered mentions as the strict provider-less
	// scope gate. A cue word is not a name: "amount" must not select a sender
	// named Paramount when the question also names Bob. Non-count freeform
	// questions keep the ordinary resolver.
	if aggregateCountRequest(mail.Fold(strings.TrimSpace(question))) {
		// A count that named an entity must not fall back to a mailbox-wide
		// count when the entity read fails: that answer would be about a
		// different set. The strict resolver is the one scope owner.
		var err error
		entities, err = s.resolveEntityMentionsChecked(ctx, aggregateCountEntityMentions(question), question, true)
		if err != nil {
			// A count that named an entity must not fall back to a mailbox
			// total when the entity read fails. The question keeps its
			// freeform intent and returns a retrieval notice; no provider is
			// called with a scope nobody resolved.
			s.log.Warn("qa aggregate count scope unresolved", "notice", NoticeUnavailable, "reason", errorClass(err))
			return retrieval(view, NoticeUnavailable, nil, "", ""), nil
		}
	} else {
		entities = s.resolveQuestionEntities(ctx, question)
	}
	// A plural or aggregate question asks for the whole set ("clients",
	// "fournisseurs", "toutes les personnes"). It must never be reduced to a
	// single-person clarification: the planner sees it with no entity scope
	// and can return a contact/company ranking. Only a genuinely singular
	// question with several exact matches asks which one.
	if len(entities) > 1 && !pluralEntityQuestion(question) {
		if clarify := entityClarification(question, language, entities); clarify != nil {
			return clarifyView(view, clarify), nil
		}
	}
	var entity *resolvedEntity
	if len(entities) == 1 {
		entity = &entities[0]
	}
	if entity != nil && isEntitySummaryQuestion(question, *entity) {
		summary, err := s.answerEntitySummary(ctx, view, question, language, *entity)
		if err != nil {
			return AskView{}, err
		}
		if cacheableAnswer(summary) {
			s.storeAnswer(key, summary, gen, preview, s.clock.Now())
		}
		return summary, nil
	}
	if contact, ok := s.contactAnswerFor(view, entity, question); ok {
		return contact, nil
	}
	var scope *world.CompanyRow
	if entity != nil && entity.Kind == entityCompany {
		org := entity.Org
		scope = &org
	}
	return s.answerFreeform(ctx, view, question, language, preview, scope, entity, turns, turnKey, gen, key)
}

// clarifyView is the one-click ambiguity response. It carries no provider
// identity and no computed result; the options are complete follow-up
// questions that the existing UI renders as buttons.
func clarifyView(view AskView, clarify *mailquery.Clarification) AskView {
	view.Mode, view.Intent, view.Answer = ModeClarify, "clarify", clarify.Question
	view.Clarify = clarify
	view.Counts = map[string]int{}
	view.Citations = []Citation{}
	view.Provider, view.Model, view.Notice = "", "", ""
	return view
}

// companyClarification offers one complete follow-up question per named
// organization. A structured question that names two companies would
// otherwise answer mailbox-wide counts with both names silently dropped.
func companyClarification(question, language string, orgs []world.CompanyRow) *mailquery.Clarification {
	options := make([]string, 0, len(orgs))
	seen := map[string]bool{}
	for _, org := range orgs {
		label := strings.TrimSpace(org.Label)
		if label == "" {
			label = strings.TrimSpace(org.Key)
		}
		if domain := strings.TrimSpace(firstString(org.Domains)); domain != "" && !strings.Contains(label, domain) {
			label += " (" + domain + ")"
		}
		option := entityFollowUp(question, label)
		if option == "" || seen[option] {
			continue
		}
		seen[option] = true
		options = append(options, option)
	}
	if len(options) < 2 {
		return nil
	}
	prompt := "Which organization did you mean?"
	if isFrench(language) {
		prompt = "Quelle organisation ?"
	}
	return &mailquery.Clarification{Question: prompt, Options: options}
}

// firstString is the first non-empty value, used for the one domain the
// choice label shows.
func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// cachedFreeform reports a cached freeform answer, with the fresh ask's
// identity and timing reset. It returns the generation and cache key so a
// miss can use them without reading the generation twice. A continuation's
// turnKey scopes both the lookup and the stored answer, so a cached answer
// from one thread is never served to another; a brand-new thread has no
// prior context and uses the context-free single-turn key.
func (s *Service) cachedFreeform(view AskView, question, language string, preview bool,
	turnKey string) (AskView, uint64, string, bool) {
	gen := s.store.Generation()
	// A route re-score rewrites the certified answers an answer cites
	// without touching mail or labels, so it is its own cache dimension.
	routeGen := s.store.RouteGeneration()
	now := s.clock.Now().In(s.zone())
	key := answerCacheKey(s.plannerIdentity(), language, question, gen, routeGen, preview, turnKey,
		now.Format("2006-01-02"), s.zoneName())
	cached, ok := s.cachedAnswer(key)
	if ok {
		// The answer is reused, the ask is not: SaveQA below persists a
		// fresh history row with this call's id and timing. The cached
		// intent is kept because a computed answer may have resolved to
		// a typed kind (or the company shortcut) on its first ask. The
		// conversation id is this turn's, never the cached thread's.
		cached.Question = question
		cached.Preview = preview
		cached.ConversationID = view.ConversationID
		cached.ID, cached.CreatedAt, cached.TookMS = 0, "", 0
	}
	return cached, gen, key, ok
}

// answerFreeform is the freeform path with an already linked organization and
// resolved person: the profile shortcut only for a question about the
// organization itself, otherwise the planner with that organization as the
// retrieval scope and the person as an exact address scope.
func (s *Service) answerFreeform(ctx context.Context, view AskView, question, language string, preview bool,
	org *world.CompanyRow, entity *resolvedEntity, turns []store.QAHistory, turnKey string, gen uint64, key string) (AskView, error) {
	var err error
	switch {
	case org != nil && companyProfileQuestion(question, *org):
		// The question asks about the organization itself; its profile
		// answers it deterministically and instantly.
		view, err = s.askCompany(ctx, view, *org, question, language)
	case org != nil:
		// The organization is only the scope of a specific question.
		// Retrieve its mail and let the planner answer from it.
		view, err = s.askPlanned(ctx, view, question, language, org, entity, turns, turnKey)
	default:
		view, err = s.askPlanned(ctx, view, question, language, nil, entity, turns, turnKey)
	}
	if err == nil && cacheableAnswer(view) {
		s.storeAnswer(key, view, gen, preview, s.clock.Now())
	}
	return view, err
}

// askPlanned is the general free-form path. It checks the local plan cache
// first, retrieves local context only on a miss, and asks the configured
// generator for one strict JSON decision: execute a typed plan, answer from
// the numbered messages, or ask one clarifying question. A plan is executed
// locally; the model never computes the count, name or date it returns.
// org, when set, is the organization the question names: retrieval is
// scoped to its mail so a specific fact is answered from that relationship,
// never from the organization's generic profile. turns are the prior turns
// of the same conversation, oldest first, and turnKey scopes both caches to
// this thread turn.
func (s *Service) askPlanned(ctx context.Context, view AskView, question, language string,
	org *world.CompanyRow, entity *resolvedEntity, turns []store.QAHistory, turnKey string) (AskView, error) {
	key := planKey(s.plannerIdentity(), language, question, turnKey)

	// The persisted plan cache is the cheapest path: a hit executes the
	// stored plan without searching the mailbox or calling the planner. A
	// resolved person is re-applied to the cached plan, so an old substring
	// contact is rewritten to exact addresses. A plan that matches nothing is
	// not a final answer: the question may name a person or a fact a stale
	// text filter never matched, so fall through to fresh retrieval instead
	// of freezing a wrong no-match.
	if plan, ok, err := s.store.GetQAPlan(ctx, key); err != nil {
		s.log.Warn("qa plan cache unreadable", "reason", errorClass(err))
	} else if ok {
		plan = applyResolvedEntity(plan, entity, question)
		if !computedPlanAllowed(question, plan) {
			// Do not replay a cached computed plan as the answer to a
			// content or summary question. Fall through to fresh retrieval
			// and let the planner answer from the numbered mail.
			s.log.Warn("qa cached content plan rejected", "kind", string(plan.Kind), "action", string(plan.Action))
		} else {
			cached, err := s.executePlan(ctx, view, key, language, plan)
			if err != nil {
				return AskView{}, err
			}
			if !emptyPlanResult(cached) {
				return cached, nil
			}
		}
	}

	msgs, threads, facts, err := s.plannerContext(ctx, question, org)
	if notice, reason, degraded := retrievalNotice(err); degraded {
		// Retrieval could not search the whole mailbox. Never report this as
		// an empty mailbox or a provider problem. The answer is not cached,
		// so the next ask retries, and a transient notice is not written to
		// history: it is not an answer and must not replay after the index is
		// healthy.
		s.log.Warn("qa retrieval degraded", "notice", notice, "reason", reason)
		return retrieval(view, notice, nil, "", ""), nil
	}
	if err != nil {
		return AskView{}, err
	}

	if s.source == nil {
		if len(msgs) == 0 {
			return noMatches(view), nil
		}
		return retrieval(view, NoticeUnavailable, msgs, "", ""), nil
	}
	gen, err := s.source.Generator()
	if err != nil {
		s.log.Warn("qa planner unavailable", "notice", NoticeUnavailable, "reason", errorClass(err))
		if len(msgs) == 0 {
			return noMatches(view), nil
		}
		return retrieval(view, NoticeUnavailable, msgs, "", ""), nil
	}
	view.Provider, view.Model = gen.Name(), gen.Model()
	owner, err := s.store.OwnerIdentity(ctx)
	if err != nil {
		// The owner line sharpens person questions; a failed identity read
		// must not discard the mail evidence or invent a name.
		s.log.Warn("qa owner identity unavailable", "reason", errorClass(err))
		owner = store.OwnerIdentity{}
	}
	system, user := plannerPrompt(question, language, msgs, threads, facts, org, owner, entity, turns, s.clock.Now(), s.zoneName())
	callCtx, cancel := s.generationContext(ctx)
	defer cancel()
	genRes, err := gen.Generate(callCtx, system, user)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return AskView{}, ctx.Err()
		}
		s.log.Warn("qa planner call failed", "notice", NoticeGenerationFailed, "provider", view.Provider, "model", view.Model, "reason", errorClass(err))
		return retrieval(view, NoticeGenerationFailed, msgs, view.Provider, view.Model), nil
	}
	resp, ok := parsePlanResponse(genRes.Content, len(msgs))
	if !ok {
		// A plain answer is still usable only when it is grounded in the
		// numbered messages; JSON that failed the contract is never shown
		// as prose.
		raw := strings.TrimSpace(genRes.Content)
		unfenced := trimJSONFence(raw)
		if !looksLikeJSON(unfenced) {
			// A prose fallback is accepted only when it cites the
			// numbered evidence itself. The whole context is not a citation
			// list: a markerless sentence must not claim every message as
			// its source.
			answer, citations := groundAnswer(unfenced, msgs, nil)
			if answer != "" && len(citations) > 0 {
				view.Mode, view.Intent = ModeGenerated, IntentFreeform
				view.Answer, view.Citations = answer, citations
				return s.finalizeGeneratedAnswer(view, language), nil
			}
			s.log.Warn("qa answer not grounded", "notice", NoticeNoGroundedAnswer, "provider", view.Provider, "model", view.Model)
			return retrieval(view, NoticeNoGroundedAnswer, msgs, view.Provider, view.Model), nil
		}
		s.log.Warn("qa planner contract failed", "notice", NoticeGenerationFailed, "provider", view.Provider, "model", view.Model)
		return retrieval(view, NoticeGenerationFailed, msgs, view.Provider, view.Model), nil
	}
	switch resp.Route {
	case "plan":
		plan := applyResolvedEntity(*resp.Plan, entity, question)
		if !computedPlanAllowed(question, plan) {
			// "What did X say?", "Who approved the invoice?" or a summary
			// request asks for a fact or a narrative inside the mail. A
			// computed plan is a different answer, even when it cites the
			// right messages, so fall back to the retrieved sources.
			s.log.Warn("qa content plan rejected", "kind", string(plan.Kind), "action", string(plan.Action),
				"notice", NoticeNoGroundedAnswer, "provider", view.Provider, "model", view.Model)
			if len(msgs) == 0 {
				return noMatches(view), nil
			}
			return retrieval(view, NoticeNoGroundedAnswer, msgs, view.Provider, view.Model), nil
		}
		return s.executePlan(ctx, view, key, language, plan)
	case "answer":
		answer, citations := groundAnswer(strings.TrimSpace(resp.Answer), msgs, resp.Used)
		if strings.TrimSpace(answer) == "" || len(citations) == 0 {
			if len(msgs) == 0 {
				return noMatches(view), nil
			}
			s.log.Warn("qa answer not grounded", "notice", NoticeNoGroundedAnswer, "provider", view.Provider, "model", view.Model)
			return retrieval(view, NoticeNoGroundedAnswer, msgs, view.Provider, view.Model), nil
		}
		view.Mode, view.Intent = ModeGenerated, IntentFreeform
		view.Answer, view.Citations = answer, citations
		return s.finalizeGeneratedAnswer(view, language), nil
	case "clarify":
		view.Mode, view.Intent, view.Answer = ModeClarify, "clarify", resp.Question
		view.Clarify = &mailquery.Clarification{Question: resp.Question, Options: resp.Options}
		return view, nil
	default:
		s.log.Warn("qa planner contract failed", "notice", NoticeGenerationFailed, "provider", view.Provider, "model", view.Model)
		return retrieval(view, NoticeGenerationFailed, msgs, view.Provider, view.Model), nil
	}
}

// generationContext is the one provider-call budget and timeout wrapper. The
// planner and the deterministic summary share it, so a new call path cannot
// bypass qa.call_timeout or qa.budget.*.
func (s *Service) generationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	guard := engine.NewBudgetGuard(engine.Budget{MaxCalls: s.cfg.Int("qa.budget.max_calls"),
		MaxUSD: s.cfg.Float("qa.budget.max_usd"), AllowUnpriced: false})
	return context.WithTimeout(engine.WithReserver(ctx, guard),
		time.Duration(s.cfg.Int("qa.call_timeout"))*time.Second)
}

// retrievalContext is the one local-retrieval budget for every question that
// waits for a complete answer: the freeform planner path and the sender path
// share qa.retrieval_timeout, so neither can race the search box's interactive
// budget or hold a worker without a bound.
func (s *Service) retrievalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.cfg.Duration("qa.retrieval_timeout"))
}

// plannerContext retrieves owner messages for a content answer. A question
// that names an organization is answered from that organization's own
// conversations first: its mail is the scope of the fact being asked, and
// the thread's first and latest message carry the task and its state. The
// organization's profile facts travel beside those messages. Zero search
// hits stay zero: an analytical plan needs no excerpts, and unrelated
// priority items are never substituted for a question the mailbox did not
// match. With one or two hits, priority's best items top up the context. A
// question that names a person also merges the newest messages from every
// address writing under that display name, so the person's other
// organizations are evidence instead of a missing address.
func (s *Service) plannerContext(ctx context.Context, question string, org *world.CompanyRow) ([]contextMessage, []threadEvidence, *organizationFacts, error) {
	if org != nil {
		msgs, threads, facts, err := s.companyEvidence(ctx, question, *org)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(msgs) > 0 {
			return msgs, threads, facts, nil
		}
	}
	limit := s.cfg.Int("qa.max_messages")
	todayScoped := questionMentionsToday(question)
	filter := store.SearchFilter{OwnerOnly: true}
	if todayScoped {
		since, until, err := s.todayBounds()
		if err != nil {
			return nil, nil, nil, err
		}
		filter.Since, filter.Until = since, until
	}
	query := search.Query{Text: question, Filter: filter, Limit: limit, Wait: true}
	// A written answer is allowed to take seconds. Retrieval is complete
	// within this bound: it waits for an index that is catching up and for
	// the semantic ranking instead of racing the search box's 150 ms budget.
	retrievalCtx, cancel := s.retrievalContext(ctx)
	defer cancel()
	res, err := s.search.Search(retrievalCtx, query)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(retrievalCtx.Err(), context.DeadlineExceeded) {
			return nil, nil, nil, errSearchTimeout
		}
		return nil, nil, nil, err
	}
	var todayMsgs []contextMessage
	if todayScoped {
		_, todayMsgs, err = s.todayContext(ctx, limit)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if len(res.Hits) == 0 && len(todayMsgs) == 0 {
		if problem := retrievalProblemOf(res.Semantic); problem != nil {
			// Either the semantic leg ran against an empty/partial vector
			// set while the mailbox has eligible mail, or it missed this
			// request's budget. In both cases the mailbox has not been
			// searched with every index yet: this is not a no-match answer.
			return nil, nil, nil, problem
		}
		return nil, nil, nil, nil
	}
	// A question that names a person also needs that person's other
	// addresses: their signatures and domains are the evidence for the
	// organizations the question asks about. A failed leg costs nothing
	// more than those extra messages. A today question already has the day's
	// messages; expanding a person beyond that day would reintroduce the old
	// evidence the date scope excludes.
	var personMsgs []contextMessage
	if !todayScoped {
		personMsgs, err = s.personEvidence(ctx, question, res.Hits, personEvidenceBudget(limit))
		if err != nil {
			s.log.Warn("qa person evidence unavailable", "reason", errorClass(err))
			personMsgs = nil
		}
	}
	// One ranking read covers both uses: its best items top up a thin
	// retrieval, and its thresholded signals annotate every numbered
	// message the ranking already knows. A failed read costs only those
	// signals and the top-up, never the retrieval itself.
	top := s.priorityTop(ctx)
	signals := signalsByMessage(top)
	var byID map[int64]priority.Item
	if len(res.Hits) < 3 && !todayScoped {
		byID = make(map[int64]priority.Item, len(top))
		for _, it := range top {
			byID[it.MessageID] = it
		}
	}
	excerpts := make(map[int64]string, len(res.Hits))
	for _, h := range res.Hits {
		if h.Snippet != "" {
			excerpts[h.RowID] = h.Snippet
		}
	}
	ordered := make([]int64, 0, len(personMsgs)+len(res.Hits)+3)
	personByID := make(map[int64]contextMessage, len(personMsgs))
	seen := map[int64]bool{}
	for _, m := range personMsgs {
		if m.ID <= 0 || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		personByID[m.ID] = m
		ordered = append(ordered, m.ID)
	}
	for _, h := range res.Hits {
		if !seen[h.RowID] {
			seen[h.RowID] = true
			ordered = append(ordered, h.RowID)
		}
	}
	todayByID := make(map[int64]contextMessage, len(todayMsgs))
	for _, m := range todayMsgs {
		if m.ID <= 0 || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		todayByID[m.ID] = m
		ordered = append(ordered, m.ID)
	}
	if len(res.Hits) < 3 && !todayScoped {
		added := 0
		for _, it := range top {
			if added == 3 || len(ordered) >= limit {
				break
			}
			if !seen[it.MessageID] {
				seen[it.MessageID] = true
				ordered = append(ordered, it.MessageID)
				added++
			}
		}
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	// The person leg and the day read already have each message's owner
	// summary; only the remaining ids need a summary read.
	rest := ordered
	if len(personByID) > 0 || len(todayByID) > 0 {
		rest = make([]int64, 0, len(ordered))
		for _, id := range ordered {
			if _, ok := personByID[id]; ok {
				continue
			}
			if _, ok := todayByID[id]; ok {
				continue
			}
			rest = append(rest, id)
		}
	}
	sums, err := s.store.SummariesOwner(ctx, rest)
	if err != nil {
		return nil, nil, nil, err
	}
	// One context read for the numbered ids: the message, recipient, label,
	// conversation and read/importance facts come from the same stored
	// bundle, so a question about any of them reads one owner boundary
	// instead of a second table.
	details, err := s.store.ContextDetails(ctx, ordered)
	if err != nil {
		// The stored dimensions sharpen a recipient, learned-label or thread
		// question; a failed read must not discard the summaries the answer
		// still has. A missing detail also never fabricates "unread" or
		// "low" for a message whose stored value was not read.
		s.log.Warn("qa message context unavailable", "reason", errorClass(err))
		details = nil
	}
	msgs := make([]contextMessage, 0, len(ordered))
	for _, id := range ordered {
		detail, hasDetail := details[id]
		if m, ok := todayByID[id]; ok {
			msgs = append(msgs, m)
			continue
		}
		if m, ok := personByID[id]; ok {
			if hasDetail {
				if len(detail.Recipients) > 0 {
					m.Recipients = detail.Recipients
				}
				if detail.ConversationID != "" {
					m.ConversationID = detail.ConversationID
				}
				if len(detail.Labels) > 0 {
					m.Labels = detail.Labels
				}
				m.IsRead = detail.IsRead
				m.Importance = detail.Importance
				m.ContextKnown = true
			}
			msgs = append(msgs, m)
			continue
		}
		sum, ok := sums[id]
		if !ok {
			continue
		}
		reason := "retrieved"
		if it, ok := byID[id]; ok {
			reason = reasonFor(it, priority.FilterAll)
			if reason == "" {
				reason = "priority"
			}
		}
		msg := newContextMessage(sum, signals[id], reason)
		msg.Excerpt = excerpts[id]
		withContextDetail(&msg, detail, hasDetail)
		msgs = append(msgs, msg)
	}
	return msgs, s.threadEvidenceFor(ctx, msgs), nil, nil
}

// priorityTop reads the cached ranking once. A failed read is a logged loss
// of the top-up and signal dimensions, never a reason to discard stored mail
// evidence the caller already has.
func (s *Service) priorityTop(ctx context.Context) []priority.Item {
	items, err := s.prio.Top(ctx, priority.MaxCached)
	if err != nil {
		s.log.Warn("qa priority signals unavailable", "reason", errorClass(err))
		return nil
	}
	return items
}

// signalMap keys each ranked item's thresholded signals by message id. Only
// a message the ranking actually carries gets an entry: no signal is
// invented for the rest.
func (s *Service) signalMap(ctx context.Context) map[int64]*priority.Signals {
	return signalsByMessage(s.priorityTop(ctx))
}

func signalsByMessage(items []priority.Item) map[int64]*priority.Signals {
	if len(items) == 0 {
		return nil
	}
	out := make(map[int64]*priority.Signals, len(items))
	for i := range items {
		if items[i].MessageID <= 0 {
			continue
		}
		signals := items[i].Signals
		out[items[i].MessageID] = &signals
	}
	return out
}

// normalizeLatestPlan closes the one ambiguous shape the executor owns: a
// single-message list for a named contact with direction "both". Without an
// explicit direction the newest match can be the owner's own reply, which is
// never "the latest message from" that person. The planner contract asks for
// "received"; a plan that still says "both" is corrected here, before it is
// cached, so the wrong answer cannot be replayed. A question about a message
// the owner sent carries direction "sent" and is untouched.
func normalizeLatestPlan(plan mailquery.Plan) mailquery.Plan {
	if plan.Kind == mailquery.KindMessages && plan.Action == mailquery.ActionList &&
		(plan.Contact != "" || len(plan.ContactAddresses) > 0) && plan.Limit == 1 &&
		plan.Direction == mailquery.DirectionBoth {
		plan.Direction = mailquery.DirectionReceived
	}
	return plan
}

// executePlan resolves the relative window in the owner's zone, runs the
// local query and caches the relative plan. The returned view carries the
// plan and its typed rows, never model-authored numbers.
func (s *Service) executePlan(ctx context.Context, view AskView, key, language string, plan mailquery.Plan) (AskView, error) {
	if plan.Kind == mailquery.KindAgenda {
		return s.executeAgenda(ctx, view, key, language, plan)
	}
	// A people/company topic ranking is retrieval-plus-grouping, not a SQL
	// text filter. It is never written to the plan cache: the retrieved hit
	// set is not reproducible from the plan alone. A sent-only request falls
	// through to the lexical executor, which owns recipient aggregation.
	if topicRankingPlan(plan) && plan.Direction != mailquery.DirectionSent {
		return s.executeTopicRanking(ctx, view, key, language, plan)
	}
	plan = normalizeLatestPlan(plan)
	loc := s.zone()
	resolved, _, err := plan.Resolve(s.clock.Now(), loc)
	if err != nil {
		return AskView{}, err
	}
	result, err := s.store.ExecuteMailQuery(ctx, resolved, loc)
	if err != nil {
		return AskView{}, err
	}
	view.Mode, view.Intent, view.Answer, view.Notice = ModeComputed, string(plan.Kind), "", ""
	view.Plan, view.Result = &plan, &result
	view.Citations = []Citation{}
	if plan.Kind == mailquery.KindMessages && plan.Action == mailquery.ActionList {
		for _, row := range result.Rows {
			view.Citations = append(view.Citations, Citation{MessageID: row.MessageID, Subject: row.Subject,
				SenderName: row.SenderName, ReceivedAt: row.Instant, Reason: "match"})
		}
	}
	view.Counts = map[string]int{}
	switch plan.Kind {
	case mailquery.KindContacts:
		view.Counts["contacts"] = len(result.Rows)
		view.Counts["messages"] = result.Total
	case mailquery.KindCompanies:
		view.Counts["companies"] = len(result.Rows)
		view.Counts["messages"] = result.Total
	default:
		view.Counts["total"] = result.Total
	}
	if plan.Kind == mailquery.KindMessages && plan.Action == mailquery.ActionList && result.Total == 0 {
		// A zero-result list is not evidence. Leave the cache untouched so a
		// later ask re-plans with fresh retrieval.
	} else if err := s.store.SaveQAPlan(ctx, key, language, plan); err != nil {
		s.log.Warn("qa plan not cached", "reason", errorClass(err))
	}
	return view, nil
}

// executeAgenda runs the one plan whose evidence spans the world model:
// calendar occurrences, threads where the owner still owes a reply, and
// due commitments. The numbers come from the agenda service, never from
// the model.
func (s *Service) executeAgenda(ctx context.Context, view AskView, key, language string, plan mailquery.Plan) (AskView, error) {
	if s.agenda == nil {
		return AskView{}, errors.New("qa: the agenda service is not configured")
	}
	horizon := s.agenda.DefaultHorizon()
	if plan.HorizonHours > 0 {
		horizon = time.Duration(plan.HorizonHours * float64(time.Hour))
	}
	res, err := s.agenda.Build(ctx, horizon)
	if err != nil {
		return AskView{}, err
	}
	if plan.Limit > 0 && len(res.Items) > plan.Limit {
		res.Items = res.Items[:plan.Limit]
		res.Truncated = true
	}
	view.Mode, view.Intent = ModeComputed, string(mailquery.KindAgenda)
	view.Plan, view.Agenda = &plan, &res
	view.Answer = agendaAnswer(language, res)
	view.Counts = map[string]int{"events": res.Events, "obligations": res.Obligations, "deadlines": res.Deadlines}
	view.Citations = []Citation{}
	for _, it := range res.Items {
		if it.MessageID > 0 {
			view.Citations = append(view.Citations, Citation{MessageID: it.MessageID, Reason: "agenda"})
		}
	}
	if err := s.store.SaveQAPlan(ctx, key, language, plan); err != nil {
		s.log.Warn("qa plan not cached", "reason", errorClass(err))
	}
	return view, nil
}

// agendaAnswer is the short server-side summary; the screen renders the
// items themselves. It is composed from counts, never from model text.
func agendaAnswer(language string, res agenda.Result) string {
	h := res.HorizonHours
	if isFrench(language) {
		return fmt.Sprintf("%d rendez-vous, %d réponses à donner, %d échéances dans les %g prochaines heures.",
			res.Events, res.Obligations, res.Deadlines, h)
	}
	return fmt.Sprintf("%d events, %d replies owed, %d commitments due in the next %g hours.",
		res.Events, res.Obligations, res.Deadlines, h)
}

// PlanResponse is the one strict JSON object the planner returns.
type PlanResponse struct {
	Route    string          `json:"route"`
	Plan     *mailquery.Plan `json:"plan,omitempty"`
	Answer   string          `json:"answer,omitempty"`
	Used     []int           `json:"used,omitempty"`
	Question string          `json:"question,omitempty"`
	Options  []string        `json:"options,omitempty"`
}

// parsePlanResponse validates a planner decision at the trust boundary.
func parsePlanResponse(content string, n int) (PlanResponse, bool) {
	var out PlanResponse
	if err := jsonx.DecodeStrict([]byte(trimJSONFence(content)), &out); err != nil {
		return PlanResponse{}, false
	}
	out.Route = strings.ToLower(strings.TrimSpace(out.Route))
	switch out.Route {
	case "plan":
		if out.Plan == nil {
			return PlanResponse{}, false
		}
		plan := out.Plan.Normalize()
		if err := plan.Validate(); err != nil {
			return PlanResponse{}, false
		}
		out.Plan = &plan
	case "answer":
		out.Answer = strings.TrimSpace(out.Answer)
		out.Used = validIndices(out.Used, n)
		if out.Answer == "" {
			return PlanResponse{}, false
		}
	case "clarify":
		out.Question = strings.TrimSpace(out.Question)
		out.Options = cleanOptions(out.Options)
		if out.Question == "" || len(out.Options) < 2 {
			return PlanResponse{}, false
		}
	default:
		return PlanResponse{}, false
	}
	return out, true
}

// looksLikeJSON separates a malformed contract from a plain-text answer. A
// complete JSON object or array is the contract shape, and a provider
// envelope cut off mid-object is still JSON-shaped so its truncation is
// reported rather than displayed. Prose that merely begins with a bracket is
// not JSON: "[1] The invoice was approved." is a correctly cited answer.
func looksLikeJSON(content string) bool {
	raw := strings.TrimSpace(content)
	if raw == "" {
		return false
	}
	switch raw[0] {
	case '{':
		// An object opens a quoted key (or closes at once). "{see note}" is
		// prose that happens to start with a brace.
		rest := strings.TrimSpace(raw[1:])
		return strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "}")
	case '[':
		if json.Valid([]byte(raw)) {
			return true
		}
		return !citationMarker(raw)
	}
	return false
}

// citationMarker reports a leading [n] followed by prose (or nothing): the
// one citation form the Q&A prompts ask for. It is what separates
// "[1] The invoice was approved." from a truncated JSON array.
func citationMarker(raw string) bool {
	i := 1
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 1 || i >= len(raw) || raw[i] != ']' {
		return false
	}
	return i+1 == len(raw) || raw[i+1] == ' ' || raw[i+1] == '\t' || raw[i+1] == '\n'
}

// answerEnvelope is the structured shape a provider may emit despite the
// plain-text contract. Only Answer is prose; Used and Citations are provider
// hints and are ignored: grounding against the numbered messages remains the
// one citation owner.
type answerEnvelope struct {
	Answer    string `json:"answer"`
	Used      []int  `json:"used"`
	Citations []int  `json:"citations"`
}

// unwrapAnswerEnvelope returns the prose of a generated answer. Plain text is
// returned unchanged; a JSON envelope is unwrapped to its Answer field. A
// JSON-shaped string without a usable Answer is rejected, never displayed as
// prose. It is the one parser for both a fresh generation and a legacy stored
// row, so an old raw envelope reads as the answer it contains.
func unwrapAnswerEnvelope(content string) (string, bool) {
	trimmed := trimJSONFence(strings.TrimSpace(content))
	if !looksLikeJSON(trimmed) {
		return trimmed, true
	}
	var env answerEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return "", false
	}
	answer := strings.TrimSpace(env.Answer)
	if answer == "" {
		return "", false
	}
	return answer, true
}

// trimJSONFence tolerates a provider that wraps the one object in a
// markdown code fence despite the instruction; it never extracts prose.
func trimJSONFence(content string) string {
	raw := strings.TrimSpace(content)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	raw = strings.TrimPrefix(raw, "```")
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	return strings.TrimSpace(raw)
}

func validIndices(in []int, n int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, idx := range in {
		if idx < 1 || idx > n || seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, idx)
	}
	slices.Sort(out)
	return out
}

func cleanOptions(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, min(len(in), 6))
	for _, opt := range in {
		opt = strings.Join(strings.Fields(opt), " ")
		if opt == "" || len(opt) > 300 || seen[opt] {
			continue
		}
		seen[opt] = true
		out = append(out, opt)
		if len(out) == 6 {
			break
		}
	}
	return out
}

func citationsFromContext(msgs []contextMessage, used []int) []Citation {
	out := make([]Citation, 0, len(used))
	seen := make(map[int64]bool, len(used))
	for _, idx := range used {
		if idx < 1 || idx > len(msgs) {
			continue
		}
		m := msgs[idx-1]
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, Citation{MessageID: m.ID, Subject: m.Subject, SenderName: m.SenderName,
			ReceivedAt: m.ReceivedAt, Reason: m.Reason})
	}
	return out
}

// answerMarker matches the citation markers the planner writes in an answer:
// one index, or a comma/semicolon list the model groups into one bracket. The
// UI numbers the citation list 1..N and chips only a single-index marker, so a
// grouped marker must be renumbered and split here or the answer keeps a dead
// "[1, 2]" and the grouped sources never reach the list. A range ("[1-3]") is
// not a marker the prompts ask for and stays prose.
var answerMarker = regexp.MustCompile(`\[(\d+(?:\s*[,;]\s*\d+)*)\]`)

// markerIndices splits one marker's inner text into its indices.
func markerIndices(inner string) []int {
	parts := strings.FieldsFunc(inner, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// groundAnswer makes the written answer and its citation list one numbering
// space. The model cites prompt indices; the UI renders citation positions.
// Every valid marker becomes its first-appearance position, a cited message
// the model forgot in "used" is added as a source, and an invalid marker is
// removed rather than left as a dead chip. With no valid marker, the
// model's declared sources are kept but the prompt-index markers are
// stripped, so no source number can point at nothing.
func groundAnswer(answer string, msgs []contextMessage, used []int) (string, []Citation) {
	matches := answerMarker.FindAllStringSubmatchIndex(answer, -1)
	if len(matches) == 0 {
		return answer, citationsFromContext(msgs, used)
	}
	order := make([]int, 0, len(matches))
	position := make(map[int]int, len(matches))
	valid := func(n int) bool { return n >= 1 && n <= len(msgs) }
	// Every marker is a citation claim. An answer that cites evidence
	// outside the numbered context is not partially grounded: the claim and
	// its marker cannot be separated, so the whole answer is refused.
	for _, match := range matches {
		indices := markerIndices(answer[match[2]:match[3]])
		if len(indices) == 0 {
			return "", nil
		}
		for _, n := range indices {
			if !valid(n) {
				return "", nil
			}
		}
	}
	for _, match := range matches {
		for _, n := range markerIndices(answer[match[2]:match[3]]) {
			if _, ok := position[n]; ok {
				continue
			}
			position[n] = len(order) + 1
			order = append(order, n)
		}
	}
	var b strings.Builder
	last := 0
	for _, match := range matches {
		b.WriteString(answer[last:match[0]])
		// One marker per index, in the bracket's own order, so the UI can
		// chip each source; a repeated index inside one bracket is dropped.
		seen := map[int]bool{}
		for _, n := range markerIndices(answer[match[2]:match[3]]) {
			if seen[n] {
				continue
			}
			seen[n] = true
			b.WriteString("[" + strconv.Itoa(position[n]) + "]")
		}
		last = match[1]
	}
	b.WriteString(answer[last:])
	if len(order) == 0 {
		// Markers were present but none named a message in the context.
		// The declared sources are not a substitute: the answer itself
		// claimed citations that do not exist, so it is not grounded.
		return b.String(), nil
	}
	return b.String(), citationsFromContext(msgs, order)
}

// retrievalProblemOf names why a zero-hit search could not have searched the
// whole mailbox. Warming is a vector index that is genuinely incomplete for
// eligible mail; a timeout is a warm index that missed one request's budget
// (the query was slow, the vector copy was being prepared, the filter set was
// being listed). A timeout must never be reported as a warming index: that
// is the lie that kept showing "the search index is preparing" on a fully
// indexed mailbox. An unavailable embedder with a complete index is neither:
// the full-text leg answered, so a zero-hit question is a genuine no-match.
var (
	errSearchWarming = errors.New("qa: semantic index is warming")
	errSearchTimeout = errors.New("qa: retrieval timed out")
)

// retrievalProblemOf classifies a semantic leg as the one sentinel the notice
// mapping owns: nil when the leg answered, a timeout when it missed this
// request's budget, warming when the vector copy is partial. A timeout wins
// over a partial copy: a slow request must never be reported as a warming
// index.
func retrievalProblemOf(sem search.Semantic) error {
	if sem.TimedOut {
		return errSearchTimeout
	}
	if sem.Eligible > 0 && sem.Covered < sem.Eligible {
		return errSearchWarming
	}
	return nil
}

// threadStates reads the authoritative conversation truth for the requested
// ids. The priority ranking is the same source the agenda and the company
// screen show, so a question about a thread cannot disagree with them. Only
// conversations outside the ranking fall back to the store's structural
// resolver, and that fallback carries no classifier signal: its result is a
// structural state, never a claim that the owner owes a reply.
func (s *Service) threadStates(ctx context.Context, ids []string) (map[string]threadintel.Assessment, error) {
	out := make(map[string]threadintel.Assessment, len(ids))
	if s.prio != nil {
		resolved, err := s.prio.ThreadStates(ctx)
		if err != nil {
			s.log.Warn("qa priority thread states unavailable; using the store resolver", "reason", errorClass(err))
		} else {
			for _, id := range ids {
				if state, ok := resolved[id]; ok {
					out[id] = state
				}
			}
		}
	}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		resolved, err := s.store.ThreadStates(ctx, missing, nil)
		if err != nil {
			return nil, err
		}
		for id, state := range resolved {
			out[id] = state
		}
	}
	return out, nil
}

// noMatches is the explicit no-source answer: nothing in visible owner mail
// grounds the question, so no priority item is substituted, no provider call
// is made and there is nothing to cite.
func noMatches(view AskView) AskView {
	return retrieval(view, NoticeNoMatches, nil, "", "")
}

// retrieval is the explicit no-written-answer mode: the retrieval sources
// are still cited and nothing is invented. The caller passes the notice that
// names the cause; NoticeUnavailable is reserved for a missing generator,
// never for a failed call or a contract failure.
func retrieval(view AskView, notice string, msgs []contextMessage, provider, model string) AskView {
	view.Mode, view.Answer, view.Notice = ModeRetrieval, "", notice
	view.Provider, view.Model = provider, model
	view.Citations = make([]Citation, 0, len(msgs))
	for _, m := range msgs {
		view.Citations = append(view.Citations, Citation{MessageID: m.ID, Subject: m.Subject, SenderName: m.SenderName,
			ReceivedAt: m.ReceivedAt, Reason: m.Reason})
	}
	return view
}

// priorityCounts reads every filter's candidate total (one cached ranking).
func (s *Service) priorityCounts(ctx context.Context) (map[string]int, error) {
	out := make(map[string]int, len(priority.Filters))
	for _, f := range priority.Filters {
		_, total, _, err := s.prio.Rank(ctx, f, 1)
		if err != nil {
			return nil, err
		}
		key := string(f)
		if f == priority.FilterAll {
			key = "total"
		}
		out[key] = total
	}
	return out, nil
}

func citationsFor(items []priority.Item, filter priority.Filter) []Citation {
	out := make([]Citation, 0, len(items))
	for _, it := range items {
		out = append(out, Citation{MessageID: it.MessageID, Subject: it.Subject, SenderName: it.SenderName,
			ReceivedAt: it.ReceivedAt, Reason: reasonFor(it, filter)})
	}
	return out
}

// reasonFor picks the citation reason that matches the requested filter,
// falling back to the item's first reason.
func reasonFor(it priority.Item, filter priority.Filter) string {
	var preference []string
	switch filter {
	case priority.FilterReply:
		preference = []string{"reply", "question", "action"}
	case priority.FilterUrgent:
		preference = []string{"urgent", "deadline"}
	case priority.FilterSecurity:
		preference = []string{"security"}
	case priority.FilterDecision:
		preference = []string{"decision"}
	case priority.FilterDeadline:
		preference = []string{"deadline"}
	case priority.FilterWorkload:
		preference = []string{"workload"}
	}
	for _, want := range preference {
		if slices.Contains(it.Reasons, want) {
			return want
		}
	}
	if len(it.Reasons) > 0 {
		return it.Reasons[0]
	}
	return ""
}

func errorClass(err error) string {
	switch {
	case errors.Is(err, engine.ErrBudget):
		return "budget"
	case errors.Is(err, engine.ErrNoKey), errors.Is(err, engine.ErrUnavailable), errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return "failed"
}

// plannerPrompt describes the typed plan and the two honest alternatives.
// It contains no per-install phrase list: the schema and examples teach the
// decision space, and the model generalizes to new wording.
func plannerPrompt(question, language string, msgs []contextMessage, threads []threadEvidence,
	facts *organizationFacts, org *world.CompanyRow, owner store.OwnerIdentity, entity *resolvedEntity,
	turns []store.QAHistory, now time.Time, zone string) (system, user string) {
	system = `You are Mailsense's mail query planner. You answer questions about the owner's mailbox.
` + ownerPromptLine(owner) + `
Choose exactly one route and return one JSON object, with no markdown:
{"route":"plan","plan":{...}}      for a count, ranking, grouping, list or time aggregation over mail metadata
{"route":"answer","answer":"...","used":[1,2]} for the content or meaning of the numbered messages
{"route":"clarify","question":"...","options":["complete question", "..."]} when the request is genuinely ambiguous

Plan schema:
- kind: "contacts" | "companies" | "messages" | "time" | "agenda"
- action: "rank" for contacts and companies, "count" or "list" for messages, "list" for time
- direction: "both" | "sent" | "received" (default "both" for contacts, companies and messages, "received" for sender questions)
- metric: "messages" | "threads" | "exchanges" (contacts and companies only; "exchanges" keeps only an organization or a contact with at least one sent and one received message)
- group_by: "day" | "week" | "month" | "weekday" (time only)
- time_range: "all" | "today" | "yesterday" | "last_7_days" | "last_30_days" | "this_month" | "last_month" | "this_year" | "last_year"
- since, until: optional YYYY-MM-DD dates; until is inclusive. Use them for an arbitrary span such as "dans les 3 derniers mois".
- contact: optional name or address to restrict to one person; for companies, a company name or a domain
- contact_addresses: optional exact normalized addresses (at most 20) that replace contact; use only the addresses in the Resolved entity block
- text: optional words to match in subject or body
- unread: true, false, or null
- has_attachments: true, false, or null
` + mailquery.PromptSchema() + `
- order: "desc" or "asc"

Rules:
- A question about mail metadata — how many, how often, a ranking, a list, a busy day, a period, a sender leaderboard — returns a plan. Never state a count or a ranking that is not in the numbered messages.
- A question about what a message says, who it is about, who said it, or who a task is for is a content question: answer it from the numbered messages and cite them, even when it starts with who, what or when. A person's name that appears in a message is an answer, not a count. Never state a name or a date that is not in the numbered messages.
- A yes/no question about a reply, an answer, or whether someone is still waiting ("did X reply", "has X answered", "est-ce que X a répondu", "X m'a-t-il répondu") is a content question: return route "answer", cite the owner's last sent message and the later message from the other side. If no message comes after the owner's last sent message, answer no and cite the last message you checked. Never return a message list or a count for a yes/no question.
- A question about who received, was sent, or was copied on a message is answered from the To/Cc lines of the numbered messages, never from who replied later. Cite the sent message and name every recipient it lists, including Cc and Bcc.
- A question about a person — which organizations they work for, their role, their other company — is a content question. The numbered messages may come from several addresses of the same person; names, signatures, domains and addresses are the evidence. Answer from them and cite them. When the question asks for "the other company" relative to one organization, exclude that one and name the other one(s) the messages show.
- A question about the latest or last message from someone asks for the newest message that person sent: use kind "messages", action "list", that contact, direction "received", order "desc", limit 1. Answer it in prose from the numbered messages when that newest received message is among them; otherwise return the plan. Use direction "sent" only when the question asks for the message the owner sent. Never call an older retrieved message, or the owner's own reply, the latest message from that person.
- A summarization request ("summarize", "résume", "synthèse", "récap", "summary") must return route "answer" with a written summary, never a "messages" plan and never a list or count of matching messages. If the request names one person or organization and the numbered messages are one conversation, summarize what was said, the decisions, the open items and the next action, citing [n]. Never list message subjects as the answer.
- The Resolved entity block is authoritative. For a person, use only its exact addresses (kind "messages" with contact_addresses) or the exact address, never the first-name substring. Its company/domain is identity evidence, not a separate person. For a company, use its label or one listed domain as the contact.
- When a singular question names an ambiguous entity, return route "clarify" with one complete option per candidate; the backend normally catches this before you are called. Every option must include the candidate's address or domain so clicking it resolves exactly. A plural role noun ("clients", "contacts", "fournisseurs", "personnes") is a set: never clarify it and never ask which one; return the list or ranking instead.
- Learned labels are machine evidence, not human truth: they are marked manual or judge. Use a label when the question asks for that dimension, cite the message, and prefer manual over judge when both exist. Never invent a label the message does not carry.
- A question about attachments, unread state, the sender, the recipients, the organizations a person belongs to, or a message's learned category/topic/urgency/sentiment/stance is answered from the corresponding field on the numbered messages, citing them. A question about the latest message or a thread's state uses the Thread states block and the Received dates; never infer either from the numbered order.
- Unread, importance, attachments, thread visibility, message counts, organization facts and learned labels are stored evidence. Use them when the question asks for that dimension, cite the messages, and say "partial" when the thread visibility is partial.
- The Owner line names the mailbox owner. A message marked "sent by the owner" is the owner's own mail: never describe the owner as a third party, never count the owner as a contact, and never treat the owner's signature as evidence about another person.
- Every numbered message states its direction (sent by the owner, or received). A reply is a received message after the owner's sent message in the same thread. The thread states below are resolved from every visible message, including shared mail; use them as the authority on who moved last, and use the message direction to confirm it. The numbered messages are evidence order, not chronological: the organization's task statement can come before older replies, so compare Received when the order matters.
- "compagnie", "entreprise", "organisation", "company", "organization" with a ranking or a count means kind "companies", never "contacts": it ranks external organizations, not people. "personnes", "destinataires", "contacts", "people" means kind "contacts". A question about senders ("expéditeurs", "who emails me") means contacts with direction "received".
- A request for a list or ranking of several people or organizations filtered by a subject, idea or event ("clients who mentioned AI", "fournisseurs qui ont parlé de X", "which contacts discussed the merger") returns kind "contacts" (or "companies" when it says organizations) with action "rank", direction "received" for who wrote to the owner, and "text" set to the short topic keywords, keeping the owner's acronyms and adding the obvious expansion (for example "IA intelligence artificielle"). Set limit 50 for contacts and 100 for companies when the request says all or list; use N for a top-N request. Never return a clarify for a plural request, and never put a person in "contact": "text" is the topic.
- "discuté", "échangé", "talked", "corresponded", "with whom" means direction "both" and metric "exchanges" (two-way) for contacts and companies.
- "combien", "how many" means kind "messages", action "count".
- "quand", "when", "par mois", "by month", "busiest day" means kind "time".
- "what should I do next", "que dois-je faire", "next 3 hours", "dans les 3 prochaines heures", "who is waiting on me", "qui attend ma réponse", "what is due before my next meeting" means kind "agenda" with the requested horizon. The agenda already knows which threads still owe the owner a reply, including when a colleague answered instead.
- Never answer "who is waiting on me" from message-level reply flags: use the agenda plan.
- For a content question, answer only from the numbered messages, in the requested language, in at most 120 words, and cite every claim with [n]. When the answer lists three or more distinct items, put each item on its own short line after one lead sentence; no markdown table. If the messages do not contain the answer, say so; never guess.
- The Conversation so far block is the same chat. Resolve follow-ups ("et pour l'autre ?", "and the second one?") against it, but ground every factual claim in the numbered messages and cite them. A clarification answer is a new turn, not a new mailbox question.
- Ask for clarification only when the direction, time range or entity genuinely changes the answer. Options must be complete questions the owner can click.
- The owner's own addresses and organizations are excluded by the executor; do not add them as contacts.
- The current date and the owner's zone are given below; use them for relative ranges.

Examples:
Q: donne-moi les 15 destinataires avec qui j'ai le plus discuté
A: {"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"both","metric":"exchanges","time_range":"all","limit":15,"order":"desc"}}

Q: avec quelle compagnie ai-je le plus discuté dans les 3 derniers mois ?
A: {"route":"plan","plan":{"kind":"companies","action":"rank","direction":"both","metric":"exchanges","since":"` + now.AddDate(0, -3, 0).Format("2006-01-02") + `","until":"` + now.Format("2006-01-02") + `","limit":15,"order":"desc"}}

Q: who emails me the most?
A: {"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"received","metric":"messages","time_range":"all","limit":15,"order":"desc"}}

Q: donne-moi la liste de clients qui ont déjà fait mention de l'IA, n'importe laquelle dans n'importe quel contexte
A: {"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"received","metric":"messages","text":"IA intelligence artificielle","time_range":"all","limit":50,"order":"desc"}}

Q: how many emails did I send last month?
A: {"route":"plan","plan":{"kind":"messages","action":"count","direction":"sent","time_range":"last_month","limit":10,"order":"desc"}}

Q: which month was busiest?
A: {"route":"plan","plan":{"kind":"time","action":"list","direction":"both","group_by":"month","time_range":"all","limit":12,"order":"desc"}}

Q: what should I do in the next 3 hours?
A: {"route":"plan","plan":{"kind":"agenda","action":"list","horizon_hours":3,"limit":20,"order":"asc"}}

Q: qui attend ma réponse ?
A: {"route":"plan","plan":{"kind":"agenda","action":"list","horizon_hours":24,"limit":20,"order":"asc"}}

Q: qui est le plus important ?
A: {"route":"clarify","question":"Voulez-vous dire les personnes ou les organisations ?","options":["Avec quelles personnes ai-je le plus échangé ?","Avec quelles compagnies ai-je le plus échangé ?"]}
`
	var b strings.Builder
	if len(turns) > 0 {
		b.WriteString("Current question: ")
	} else {
		b.WriteString("Question: ")
	}
	b.WriteString(oneLine(question))
	b.WriteString("\nLanguage: ")
	b.WriteString(languageName(language))
	b.WriteString("\nToday: ")
	b.WriteString(now.Format("2006-01-02"))
	b.WriteString("\nOwner time zone: ")
	b.WriteString(zone)
	b.WriteString("\n")
	if org != nil {
		b.WriteString("Organization scope: ")
		b.WriteString(oneLine(org.Label))
		if domains := cleanDomains(org.Domains); len(domains) > 0 {
			b.WriteString(" (")
			b.WriteString(strings.Join(domains, ", "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	writeResolvedEntity(&b, entity)
	writeOrganizationFacts(&b, facts)
	if len(threads) > 0 {
		index := make(map[int64]int, len(msgs))
		for i, m := range msgs {
			index[m.ID] = i + 1
		}
		b.WriteString("\nThread states (deterministic; newest first):\n")
		for _, t := range threads {
			fmt.Fprintf(&b, "- %q: %s", oneLine(t.Subject), t.State)
			switch {
			case t.OwnerActorName != "" && t.OwnerActorAddr != "":
				fmt.Fprintf(&b, ", last message from %s <%s>", oneLine(t.OwnerActorName), oneLine(t.OwnerActorAddr))
			case t.OwnerActorName != "":
				fmt.Fprintf(&b, ", last message from %s", oneLine(t.OwnerActorName))
			case t.OwnerActorAddr != "":
				fmt.Fprintf(&b, ", last message from <%s>", oneLine(t.OwnerActorAddr))
			}
			if t.OwnerLastAt != "" && (t.OwnerActorName != "" || t.OwnerActorAddr != "") {
				fmt.Fprintf(&b, " on %s", t.OwnerLastAt)
			}
			if n := index[t.OwnerMessageID]; n > 0 {
				fmt.Fprintf(&b, "; last message is [%d]", n)
			}
			if t.Messages > 0 {
				fmt.Fprintf(&b, "; %s", countItems(t.Messages, "message", "messages"))
			}
			if len(t.Participants) > 0 {
				fmt.Fprintf(&b, "; participants %s", strings.Join(t.Participants, ", "))
			}
			if t.FirstAt != "" {
				fmt.Fprintf(&b, "; first %s", t.FirstAt)
			}
			if t.Visibility != "" {
				fmt.Fprintf(&b, "; visibility %s", t.Visibility)
			}
			if t.Reason != "" {
				fmt.Fprintf(&b, "; reason %s", t.Reason)
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

// writeContextMessages renders the numbered evidence block shared by the
// planner and the deterministic summary prompt. Every line is stored mail
// fields only; no prompt derives a fact of its own.
func writeContextMessages(b *strings.Builder, msgs []contextMessage) {
	if len(msgs) == 0 {
		b.WriteString("(none)\n")
	}
	for i, m := range msgs {
		direction := "received"
		if m.Sent {
			direction = "sent by the owner"
		}
		fmt.Fprintf(b, "[%d]\nSubject: %s\nDirection: %s\nFrom: %s <%s>\nReceived: %s\n",
			i+1, oneLine(m.Subject), direction, oneLine(m.SenderName), oneLine(m.SenderAddress), m.ReceivedAt)
		if m.ContextKnown {
			if !m.IsRead {
				b.WriteString("Unread: yes\n")
			}
			switch m.Importance {
			case 2:
				b.WriteString("Importance: high\n")
			case 0:
				b.WriteString("Importance: low\n")
			}
		}
		if m.HasAttachments {
			b.WriteString("Attachments: yes\n")
		}
		if m.Person != "" {
			fmt.Fprintf(b, "Person: %s\n", oneLine(m.Person))
		}
		if line := recipientsLine(m.Recipients); line != "" {
			fmt.Fprintf(b, "To/Cc: %s\n", line)
		}
		if line := labelLine(m.Labels); line != "" {
			fmt.Fprintf(b, "Learned: %s\n", line)
		}
		if m.Signals != nil {
			s := m.Signals
			fmt.Fprintf(b, "Priority signals: urgency=%d, reply_needed=%d, suspicious=%d, decision_needed=%d, "+
				"deadline=%d, effort=%d, request_count=%d, is_question=%d, call_to_action=%d, "+
				"info_sufficient=%d, category=%s, topic=%s, subtopic=%s, asks_for=%s, stance=%s, relationship=%s\n",
				s.Urgency, s.ReplyNeeded, s.Suspicious, s.DecisionNeeded, s.Deadline, s.Effort,
				s.RequestCount, s.IsQuestion, s.CallToAction, s.InfoSufficiency, oneLine(s.Category),
				oneLine(s.Subject.Topic), oneLine(s.Subject.Subtopic), oneLine(s.AsksFor),
				oneLine(s.Stance), oneLine(s.Relationship))
		}
		body := priority.Snippet(m.Body, 600)
		fmt.Fprintf(b, "Body: %s\n", body)
		if excerpt := priority.Snippet(m.Excerpt, 600); excerpt != "" && !strings.Contains(body, excerpt) {
			fmt.Fprintf(b, "Relevant excerpt: %s\n", excerpt)
		}
		b.WriteString("\n")
	}
}

// conversationBlock renders the prior turns of one conversation as compact
// chat context. It is data, not evidence: the system rule still requires the
// answer to be grounded in and cite the numbered messages. Only the newest
// MaxConversationTurns turns are considered, every question and answer is
// flattened and capped at MaxConversationTurnRunes, and empty lines are
// dropped. The caller has already scoped the turns to one conversation id.
func conversationBlock(turns []store.QAHistory) string {
	if len(turns) > MaxConversationTurns {
		turns = turns[len(turns)-MaxConversationTurns:]
	}
	var b strings.Builder
	for _, turn := range turns {
		question := priority.Snippet(turn.Question, MaxConversationTurnRunes)
		answer := ""
		if turn.Clarify != nil {
			questionText := priority.Snippet(turn.Clarify.Question, MaxConversationTurnRunes)
			if questionText != "" {
				answer = "Assistant asked: " + questionText
				if options := cleanOptions(turn.Clarify.Options); len(options) > 0 {
					answer += " | Options: " + strings.Join(options, " | ")
				}
			}
		}
		if answer == "" {
			if text := priority.Snippet(turn.Answer, MaxConversationTurnRunes); text != "" {
				answer = "Assistant: " + text
			}
		}
		if question == "" && answer == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if question != "" {
			b.WriteString("Owner: " + question + "\n")
		}
		if answer != "" {
			b.WriteString(answer + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Conversation so far (oldest first):\n" + b.String()
}

// ownerPromptLine names the mailbox owner from stored identity, never from a
// configured or hard-coded name. With no detected name it still tells the
// model to trust the per-message direction.
func ownerPromptLine(owner store.OwnerIdentity) string {
	names := make([]string, 0, len(owner.Names))
	seenName := map[string]bool{}
	for _, name := range owner.Names {
		name = oneLine(name)
		if name == "" || seenName[strings.ToLower(name)] {
			continue
		}
		seenName[strings.ToLower(name)] = true
		names = append(names, name)
	}
	addresses := make([]string, 0, len(owner.Addresses))
	seenAddress := map[string]bool{}
	for _, address := range owner.Addresses {
		address = oneLine(address)
		if address == "" || seenAddress[strings.ToLower(address)] {
			continue
		}
		seenAddress[strings.ToLower(address)] = true
		addresses = append(addresses, address)
	}
	switch {
	case len(names) > 0 && len(addresses) > 0:
		return "Owner: " + strings.Join(names, ", ") + " <" + strings.Join(addresses, ", ") + ">"
	case len(names) > 0:
		return "Owner: " + strings.Join(names, ", ")
	case len(addresses) > 0:
		return "Owner: <" + strings.Join(addresses, ", ") + ">"
	default:
		return "Owner: not identified; trust the per-message direction."
	}
}

// writeOrganizationFacts renders the owner-safe profile metadata the company
// path already fetched: volume, thread states, the top people and the next
// events and commitments. Every line is one fact group; an empty group is
// omitted. The profile is metadata only, so no body or shared/delegate
// content can appear here.
func writeOrganizationFacts(b *strings.Builder, facts *organizationFacts) {
	if facts == nil {
		return
	}
	lines := make([]string, 0, 4)
	var header []string
	if label := oneLine(facts.Label); label != "" {
		header = append(header, label)
	}
	var totals []string
	if facts.Messages > 0 {
		totals = append(totals, fmt.Sprintf("messages=%d (sent=%d, received=%d)",
			facts.Messages, facts.Sent, facts.Received))
	}
	if facts.PeopleTotal > 0 {
		totals = append(totals, fmt.Sprintf("people=%d", facts.PeopleTotal))
	}
	if facts.Threads > 0 {
		totals = append(totals, fmt.Sprintf("threads=%d", facts.Threads))
	}
	if facts.FirstAt != "" {
		totals = append(totals, "first="+facts.FirstAt)
	}
	if facts.LastAt != "" {
		totals = append(totals, "last="+facts.LastAt)
	}
	if len(totals) > 0 {
		header = append(header, strings.Join(totals, ", "))
	}
	if len(header) > 0 {
		lines = append(lines, "Organization: "+strings.Join(header, "; "))
	}

	if len(facts.States) > 0 || facts.OpenThreads > 0 {
		states := make([]string, 0, len(facts.States))
		any := false
		for _, st := range facts.States {
			states = append(states, fmt.Sprintf("%s=%d", st.State, st.Count))
			if st.Count > 0 {
				any = true
			}
		}
		if any || facts.OpenThreads > 0 {
			line := fmt.Sprintf("Open threads: %d", facts.OpenThreads)
			if len(states) > 0 {
				line += "; " + strings.Join(states, ", ")
			}
			lines = append(lines, line)
		}
	}

	if len(facts.People) > 0 {
		people := make([]string, 0, len(facts.People))
		for _, person := range facts.People {
			name := oneLine(person.Name)
			address := oneLine(person.Address)
			label := name
			switch {
			case name != "" && address != "":
				label = name + " <" + address + ">"
			case address != "":
				label = "<" + address + ">"
			}
			if label == "" {
				continue
			}
			people = append(people, fmt.Sprintf("%s (%d)", label, person.Messages))
		}
		if len(people) > 0 {
			lines = append(lines, "People: "+strings.Join(people, ", "))
		}
	}

	var agenda []string
	if len(facts.Events) > 0 {
		if entries := organizationEntriesLine(facts.Events); entries != "" {
			agenda = append(agenda, "Events: "+entries)
		}
	}
	if len(facts.Deadlines) > 0 {
		if entries := organizationEntriesLine(facts.Deadlines); entries != "" {
			agenda = append(agenda, "Deadlines: "+entries)
		}
	}
	if len(agenda) > 0 {
		lines = append(lines, strings.Join(agenda, "; "))
	}

	if len(lines) == 0 {
		return
	}
	b.WriteString("\nOrganization facts:\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// organizationEntriesLine renders events or deadlines as "subject (when)".
// An entry with neither a subject nor a time is dropped rather than printed
// as an empty pair.
func organizationEntriesLine(entries []organizationEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		subject := oneLine(entry.Subject)
		switch {
		case subject != "" && entry.At != "":
			parts = append(parts, subject+" ("+entry.At+")")
		case subject != "":
			parts = append(parts, subject)
		case entry.At != "":
			parts = append(parts, entry.At)
		}
	}
	return strings.Join(parts, ", ")
}

func languageName(language string) string {
	if !isFrench(language) {
		return "English"
	}
	return "French"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// recipientsLine renders one message's addressees for the planner, grouped
// by kind in stored order. Every name is paired with its address; an empty
// group is omitted. It is display only: the store's recipient table remains
// the one owner of the fact.
func recipientsLine(recipients []mail.Recipient) string {
	var to, cc, bcc []string
	for _, r := range recipients {
		name := oneLine(r.Name)
		address := oneLine(r.Address)
		label := name
		switch {
		case name != "" && address != "":
			label = name + " <" + address + ">"
		case address != "":
			label = "<" + address + ">"
		}
		if label == "" {
			continue
		}
		switch r.Kind {
		case mail.To:
			to = append(to, label)
		case mail.Cc:
			cc = append(cc, label)
		case mail.Bcc:
			bcc = append(bcc, label)
		}
	}
	parts := make([]string, 0, 3)
	if len(to) > 0 {
		parts = append(parts, "To "+strings.Join(to, ", "))
	}
	if len(cc) > 0 {
		parts = append(parts, "Cc "+strings.Join(cc, ", "))
	}
	if len(bcc) > 0 {
		parts = append(parts, "Bcc "+strings.Join(bcc, ", "))
	}
	return strings.Join(parts, " | ")
}

// labelLine renders one message's learned labels as a single compact line,
// sorted by question. Values are the store's raw JSON: a string is shown
// unquoted, a number or boolean as its JSON token, and a null is omitted (the
// question has no answer). The store keeps one source per question with
// manual shadowing judge, so the source printed here is the one fact owner.
func labelLine(labels map[string]store.LabelFact) string {
	if len(labels) == 0 {
		return ""
	}
	questions := make([]string, 0, len(labels))
	for question := range labels {
		questions = append(questions, question)
	}
	slices.Sort(questions)
	parts := make([]string, 0, len(questions))
	for _, question := range questions {
		fact := labels[question]
		value, ok := labelValue(fact.Value)
		if !ok {
			continue
		}
		parts = append(parts, fact.Source+" "+oneLine(question)+"="+value)
	}
	return strings.Join(parts, "; ")
}

// labelValue decodes one raw label value for display. It returns false for
// null and malformed JSON, which have nothing to cite. jsonx.DecodeOne rejects
// trailing data, so a value followed by another document is not read as the
// first one.
func labelValue(raw string) (string, bool) {
	value, err := jsonx.DecodeOne([]byte(raw))
	if err != nil {
		return "", false
	}
	switch v := value.(type) {
	case nil:
		return "", false
	case string:
		return oneLine(v), true
	case bool:
		return fmt.Sprint(v), true
	case json.Number:
		return v.String(), true
	default:
		// A taxonomy stores a choice or a number; another JSON shape still
		// renders as one compact token instead of leaking its raw lines.
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return oneLine(string(encoded)), true
	}
}

// cleanDomains is the display form of an organization's domains: trimmed,
// non-empty and deduplicated in their existing order. It never invents a
// domain the organization row does not declare.
func cleanDomains(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, domain := range in {
		domain = strings.TrimSpace(domain)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
	}
	return out
}
