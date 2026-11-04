package qa

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// A summary request is not a list request. When exactly one person or
// organization is resolved, the newest owner-visible conversation is read
// deterministically and the configured generator writes the summary from its
// numbered messages. No plan is executed and no count is shown as the answer.

const summaryThreadMessageLimit = 20

// summaryVerbList is the one list of summary verbs; the matcher and the
// entity stopwords both derive from it, so a new spelling cannot be taught to
// one and missed by the other.
var summaryVerbList = []string{
	"resume", "resumes", "resumer", "resumez", "resumons",
	"summarize", "summarise", "summarized", "summarised", "summary",
	"synthese", "synthetise", "synthetiser", "synthetises",
	"recap", "recapitule", "recapituler", "recapitulation",
}

// summaryIntentWords is the summary-intent vocabulary of a plain "today"
// question: the one verb list plus the summary nouns that are not verbs. A new
// spelling is therefore a summary request for both the topic path and the
// today path, instead of reaching one and missing the other.
var summaryIntentWords = slices.Concat(summaryVerbList, []string{"bilan", "overview", "brief"})

var (
	summaryVerbs = wordSet(strings.Join(summaryVerbList, " "))
	threadWords  = wordSet(strings.Join([]string{
		"echange", "echanges", "exchange", "exchanges",
		"conversation", "conversations", "thread", "threads",
		"discussion", "discussions", "fil", "fils",
	}, " "))
	contactWords = wordSet(strings.Join([]string{
		"courriel", "courriels", "email", "emails", "e-mail", "mail", "mails",
		"adresse", "adresses", "coordonnees", "contact", "contacter", "joindre",
		"reach", "address", "addresses", "contactinfo",
	}, " "))
	// contactDetailWords is the narrower set that asks for an address. A
	// company question may use "contact" as a noun ("do you have a contact
	// at Acme?"); that is a profile question, not an address lookup.
	contactDetailWords = wordSet(strings.Join([]string{
		"courriel", "courriels", "email", "emails", "e-mail", "mail", "mails",
		"adresse", "adresses", "coordonnees", "contactinfo", "address", "addresses",
	}, " "))
)

// summaryRequest reports one of the summary verbs in any language Mailsense
// accepts. It gates the plan rejection rule for every question, with or
// without a resolved entity.
func summaryRequest(question string) bool {
	return containsAny(compactWords(question), summaryVerbs)
}

// isEntitySummaryQuestion is the deterministic summary path: a summary verb
// plus an exchange/conversation/thread noun. An entity has already been
// resolved to exactly one person or organization.
func isEntitySummaryQuestion(question string, entity resolvedEntity) bool {
	if entity.Kind != entityPerson && entity.Kind != entityCompany {
		return false
	}
	words := compactWords(question)
	// "who is X" asks who the person or organization is: the resolved
	// entity's own conversation is the answer, not a mailbox-wide search.
	// The question must contain nothing but the who frame and the entity's
	// own name, so "who sent the invoice from X" keeps the search.
	if containsAny(words, whoWords) && whoIsOnly(entity, words) {
		return true
	}
	return containsAny(words, summaryVerbs) && containsAny(words, threadWords)
}

// whoIsFrame is the closed set of words a "who is X" question may use beside
// the entity's own identity. Anything else is a content question that the
// entity's profile cannot answer.
var whoIsFrame = wordSet("who whom whose qui que quel quelle quels quelles est is sont are " +
	"le la les l un une des de du the a an of person personne gens someone")

// whoIsOnly reports whether every word of the question is either the who
// frame or part of the entity's own name, addresses or domains.
func whoIsOnly(entity resolvedEntity, words []string) bool {
	identity := map[string]bool{}
	for _, token := range compactWords(entity.Label + " " + strings.Join(entity.Addresses, " ") +
		" " + strings.Join(entity.Domains, " ")) {
		identity[token] = true
	}
	for _, word := range words {
		if whoIsFrame[word] || identity[word] {
			continue
		}
		return false
	}
	return true
}

// isContactQuestion reports a request for the contact details of an already
// resolved person. It is deliberately narrow: a summary request that happens
// to contain "courriel" is handled by isEntitySummaryQuestion first.
func isContactQuestion(question string) bool {
	return containsAny(compactWords(question), contactWords)
}

// contactAnswerFor routes a contact-details question to the deterministic
// address-book answer. A person answers with its exact addresses; a company
// answers with its domain and the shared mailboxes on it. It is a small
// wrapper so the freeform dispatcher stays one path.
func (s *Service) contactAnswerFor(view AskView, entity *resolvedEntity, question string) (AskView, bool) {
	if entity == nil || !isContactQuestion(question) {
		return AskView{}, false
	}
	if entity.Kind == entityCompany && !containsAny(compactWords(question), contactDetailWords) {
		return AskView{}, false
	}
	if entity.Kind != entityPerson && entity.Kind != entityCompany {
		return AskView{}, false
	}
	return contactAnswer(view, *entity), true
}

// contactAnswer is the deterministic address-book answer: the exact known
// addresses and the declared company, with no provider call and no invented
// prose. A company has no single address, so its domain and shared mailboxes
// are the answer.
func contactAnswer(view AskView, entity resolvedEntity) AskView {
	lines := make([]string, 0, len(entity.Addresses)+len(entity.Contacts)+3)
	if entity.Label != "" {
		lines = append(lines, entity.Label)
	}
	for _, address := range entity.Addresses {
		if address != "" {
			lines = appendUniqueString(lines, address)
		}
	}
	for _, contact := range entity.Contacts {
		if contact != "" {
			lines = appendUniqueString(lines, contact)
		}
	}
	if entity.Kind == entityCompany {
		for _, domain := range entity.Domains {
			if domain != "" {
				lines = appendUniqueString(lines, domain)
			}
		}
	}
	if entity.Company != "" && entity.Company != entity.Label {
		lines = append(lines, entity.Company)
	}
	if entity.Messages > 0 {
		lines = append(lines, fmt.Sprintf("%d messages", entity.Messages))
	}
	view.Mode, view.Intent, view.Answer = ModeComputed, "contact", strings.Join(lines, "\n")
	view.Clarify = nil
	view.Counts = map[string]int{}
	view.Citations = []Citation{}
	view.Provider, view.Model, view.Notice = "", "", ""
	return view
}

// answerEntitySummary finds the newest conversation of the resolved entity,
// loads its bounded owner-safe page and summarizes it. A missing conversation
// is an explicit no-match; a missing generator keeps the exact-thread
// citations; every generation fallback uses the existing notice matrix.
func (s *Service) answerEntitySummary(ctx context.Context, view AskView, question, language string, entity resolvedEntity) (AskView, error) {
	conversationID, err := s.entityConversation(ctx, entity)
	if err != nil {
		if ctx.Err() != nil {
			return AskView{}, ctx.Err()
		}
		if entity.Kind == entityCompany {
			s.log.Warn("qa summary company profile unavailable", "notice", NoticeCompanyUnavailable, "reason", errorClass(err))
			return retrieval(view, NoticeCompanyUnavailable, nil, "", ""), nil
		}
		return AskView{}, err
	}
	if conversationID == "" {
		return noMatches(view), nil
	}
	msgs, err := s.summaryThreadMessages(ctx, conversationID)
	if err != nil {
		if ctx.Err() != nil {
			return AskView{}, ctx.Err()
		}
		return AskView{}, err
	}
	if len(msgs) == 0 {
		return noMatches(view), nil
	}
	return s.generateEntitySummary(ctx, view, question, language, entity, msgs)
}

// entityConversation is the newest owner-visible conversation of the resolved
// entity. A person is scoped by exact addresses; a company uses its profile's
// newest recent conversation rather than mixing every company mail.
func (s *Service) entityConversation(ctx context.Context, entity resolvedEntity) (string, error) {
	switch entity.Kind {
	case entityPerson:
		conversationID, _, err := s.store.LatestThreadForAddresses(ctx, entity.Addresses)
		return conversationID, err
	case entityCompany:
		profiles := s.companyProfiles()
		if profiles == nil {
			return "", nil
		}
		profile, err := profiles.Profile(ctx, entity.Key, companyProfileMonths, companyProfileDays)
		if err != nil {
			return "", err
		}
		recent := recentConversations(profile.Recent, 1)
		if len(recent) == 0 {
			return "", nil
		}
		return recent[0], nil
	default:
		return "", nil
	}
}

// summaryThreadMessages loads the newest bounded page of one conversation and
// the owner-safe details and bodies of its messages. ThreadMessages already
// applies the owner content boundary; SummariesOwner and ContextDetails repeat
// it for the body, recipients, labels and thread facts.
func (s *Service) summaryThreadMessages(ctx context.Context, conversationID string) ([]contextMessage, error) {
	page, err := s.store.ThreadMessages(ctx, conversationID, summaryThreadMessageLimit, 0)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]int64, 0, len(page.Messages))
	for _, message := range page.Messages {
		if message.ID > 0 {
			ids = append(ids, message.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// A failed detail read only costs the stored dimensions; the numbered
	// messages the summary still has stay usable (owner: contextMessages).
	return s.contextMessages(ctx, ids, "thread", "qa summary context unavailable", nil, nil)
}

// generateEntitySummary calls the configured generator with the dedicated
// summary prompt, the ordinary call budget/timeout and the ordinary grounding
// and notices. A missing or unusable provider leaves the exact-thread sources
// cited; it never falls back to a message list.
func (s *Service) generateEntitySummary(ctx context.Context, view AskView, question, language string,
	entity resolvedEntity, msgs []contextMessage) (AskView, error) {
	if s.source == nil {
		return retrieval(view, NoticeUnavailable, msgs, "", ""), nil
	}
	gen, err := s.source.Generator()
	if err != nil || gen == nil {
		s.log.Warn("qa summary generator unavailable", "notice", NoticeUnavailable, "reason", errorClass(err))
		return retrieval(view, NoticeUnavailable, msgs, "", ""), nil
	}
	view.Provider, view.Model = gen.Name(), gen.Model()
	owner, err := s.store.OwnerIdentity(ctx)
	if err != nil {
		s.log.Warn("qa owner identity unavailable", "reason", errorClass(err))
		owner = store.OwnerIdentity{}
	}
	system, user := summaryPrompt(question, language, entity, msgs, owner)
	callCtx, cancel := s.generationContext(ctx)
	defer cancel()
	res, err := gen.Generate(callCtx, system, user, engine.GenerateOptions{Prose: true})
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return AskView{}, ctx.Err()
		}
		s.log.Warn("qa summary call failed", "notice", NoticeGenerationFailed,
			"provider", view.Provider, "model", view.Model, "reason", errorClass(err))
		return retrieval(view, NoticeGenerationFailed, msgs, view.Provider, view.Model), nil
	}
	raw := strings.TrimSpace(res.Content)
	if raw == "" {
		s.log.Warn("qa summary contract failed", "notice", NoticeNoGroundedAnswer,
			"provider", view.Provider, "model", view.Model)
		return retrieval(view, NoticeNoGroundedAnswer, msgs, view.Provider, view.Model), nil
	}
	if looksLikeJSON(raw) {
		// The summary prompt asks for prose; a plan/JSON object is a
		// contract failure, not a grounded summary.
		s.log.Warn("qa summary contract failed", "notice", NoticeGenerationFailed,
			"provider", view.Provider, "model", view.Model)
		return retrieval(view, NoticeGenerationFailed, msgs, view.Provider, view.Model), nil
	}
	// A summary must cite the numbered conversation itself. Crediting every
	// message for a markerless paragraph would make an ungrounded summary
	// look sourced.
	answer, citations := groundAnswer(raw, msgs, nil)
	if strings.TrimSpace(answer) == "" || len(citations) == 0 {
		s.log.Warn("qa summary not grounded", "notice", NoticeNoGroundedAnswer,
			"provider", view.Provider, "model", view.Model)
		return retrieval(view, NoticeNoGroundedAnswer, msgs, view.Provider, view.Model), nil
	}
	view.Mode, view.Intent, view.Answer = ModeGenerated, IntentFreeform, answer
	view.Citations, view.Counts = citations, map[string]int{}
	view.Plan, view.Result, view.Notice = nil, nil, ""
	return s.finalizeGeneratedAnswer(view, language), nil
}

// summaryPrompt is the dedicated summary contract. It carries only the
// numbered conversation and the resolved identity: no plan schema, no
// mailbox-wide context, no instruction that could turn the answer into a
// message list.
func summaryPrompt(question, language string, entity resolvedEntity, msgs []contextMessage, owner store.OwnerIdentity) (system, user string) {
	system = `You summarize one owner-visible mail conversation for Mailsense.
` + ownerPromptLine(owner) + `
Rules:
- Summarize only the numbered messages below. Do not use any other knowledge and do not guess.
- Write at most 120 words in ` + languageName(language) + `.
- State the subject, who said what, the decisions, the open items and the next action.
- Cite every claim with [n], naming the numbered message that supports it.
- Never list message subjects as the answer, never print a message count, and never return a plan or JSON.
- If the messages do not support a point, omit it; do not invent names, dates or commitments.
- The owner's own messages are "sent by the owner"; never describe the owner as a third party.
`
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(oneLine(question))
	b.WriteString("\nLanguage: ")
	b.WriteString(languageName(language))
	b.WriteString("\n")
	writeResolvedEntity(&b, &entity)
	b.WriteString("\nConversation (numbered; oldest first):\n")
	writeContextMessages(&b, msgs)
	return system, b.String()
}
