package qa

import (
	"context"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/world"
)

// A question that names one organization has two different meanings. It can
// ask about the organization itself — its volume, its people, what it still
// owes or expects — and then the deterministic profile is the right answer.
// It can also ask for one fact that merely happens to be about that
// organization ("who is the laptop for?"), and then the profile answers a
// different question. This file owns that distinction and the mail scope the
// second kind reads.

// companyProfileWords is the closed vocabulary the profile can answer: one
// word per dimension companyAnswer and companyCounts report (messages,
// people, conversations, waiting replies, last contact, deadlines, events,
// open threads) plus the closed-class EN/FR question frame. It is a filter,
// not an intent table: a word outside it sends the question to the planner,
// so new wording lowers the profile's reach instead of producing a confident
// answer to a question it cannot answer.
var companyProfileWords = wordSet(strings.Join([]string{
	// What the profile counts or narrates.
	"message", "messages", "mail", "mails", "email", "emails", "courriel", "courriels",
	"echange", "echanges", "exchange", "exchanges", "correspondance", "correspondances",
	"person", "persons", "people", "personne", "personnes", "gens", "monde",
	"contact", "contacts", "participant", "participants", "interlocuteur", "interlocuteurs",
	"conversation", "conversations", "thread", "threads", "fil", "fils", "discussion", "discussions",
	"attente", "attendent", "attend", "attends", "awaiting", "waiting", "wait",
	"reply", "replies", "repondu", "repond", "reponse", "reponses", "response", "responses",
	"respond", "answered", "answer", "answers",
	"last", "latest", "recent", "recente", "recents", "recentes",
	"dernier", "derniere", "derniers", "dernieres", "date", "fois",
	"deadline", "deadlines", "echeance", "echeances", "due",
	"commitment", "commitments", "engagement", "engagements",
	"event", "events", "meeting", "meetings", "rendezvous", "appointment", "appointments",
	"agenda", "calendar", "calendrier",
	"count", "counts", "nombre", "total", "combien", "many", "much",
	"open", "opens", "ouvert", "ouverte", "ouverts", "ouvertes", "closed", "ferme", "fermee",
	"overview", "summary", "resume", "resumes", "synthese", "bilan", "recap", "brief",
	"apercu", "fiche", "profile", "profil", "info", "infos", "information", "informations",
	"status", "statut", "state", "etat", "situation", "niveau", "avancement",
	"activity", "activite", "activites",
	"history", "historique", "relationship", "relation", "relations",
	"know", "knows", "sais", "sait", "savoir", "connais", "connaitre",
	"parle", "parler", "raconte", "raconter", "topo",
	"company", "companies", "organization", "organizations", "organisation", "organisations",
	"entreprise", "entreprises", "compagnie", "compagnies", "societe", "societes", "business",
	// Closed-class question frame (EN).
	"the", "a", "an", "of", "to", "in", "on", "at", "for", "with", "about", "by",
	"from", "and", "or", "is", "are", "was", "were", "be", "been", "being",
	"do", "does", "did", "have", "has", "had", "i", "me", "my", "we", "our", "us",
	"you", "your", "it", "its", "this", "that", "these", "those", "what", "which",
	"who", "whom", "whose", "when", "where", "why", "how", "there", "here",
	"not", "no", "any", "some", "all", "please", "tell", "show", "give", "say", "said", "says",
	"going", "stand", "stands",
	"more", "most", "than", "can", "could", "would", "should", "will", "shall", "may", "might", "must",
	"s", "t", "d", "ll", "re", "ve", "m",
	// Closed-class question frame (FR).
	"le", "la", "les", "l", "un", "une", "des", "du", "de", "d", "au", "aux",
	"et", "ou", "est", "sont", "ete", "etait", "etaient", "ce", "c", "cet", "cette", "ces",
	"ca", "cela", "ceci", "il", "elle", "ils", "elles", "on", "nous", "vous", "je", "j",
	"tu", "moi", "toi", "te", "se", "y", "en", "qui", "que", "qu", "quoi", "quel", "quelle",
	"quels", "quelles", "dont", "comment", "pourquoi", "quand", "ne", "pas", "plus", "moins", "tres",
	"par", "avec", "sans", "sur", "sous", "dans", "chez", "entre", "vers",
	"pendant", "avant", "apres", "deja", "encore", "maintenant", "aujourd", "hui",
	"ai", "as", "avons", "avez", "ont", "avoir", "a", "va", "vas", "vont", "aller",
	"fait", "faire", "fais", "faut", "falloir", "dois", "doit", "devez", "devons",
	"peux", "peut", "pouvez", "pouvons", "pouvoir", "veux", "veut", "voulez", "voulons",
	"voudrais", "aimerais", "souhaite", "puis",
}, " "))

// personDimensionWords and whoWords catch the one structural case the
// profile cannot answer: a who-question about people asks for an identity,
// not for the count the profile reports. "Who is the contact at X?" must go
// to retrieval even though "contact" is a profile dimension.
var (
	personDimensionWords = wordSet("person persons people personne personnes gens monde contact contacts " +
		"participant participants interlocuteur interlocuteurs")
	whoWords = wordSet("who whom whose qui")
)

// companyContentWords separate the profile's counts from the message
// content a profile cannot answer. The profile knows how many messages and
// when the last contact was; it does not know what a message says or
// whether a specific reply arrived. A latest message, a speech verb, or a
// yes/no question about a reply is a planner question even though every
// word is otherwise a profile dimension.
var (
	// The company guard derives from the intent vocabulary and adds only the
	// company-specific synonyms. A new intent synonym therefore reaches both
	// the parser and the profile guard instead of one of them.
	companyMessageWords = wordSet(strings.Join(withWords(messageWords,
		"echange", "echanges", "exchange", "exchanges", "correspondance", "correspondances"), " "))
	// tell, ask, write and mention can frame a profile request ("Tell me
	// about X"), so the company guard removes them from the content verbs and
	// leaves them to the reported-speech frame instead.
	companyContentVerbs = wordSet(strings.Join(withWords(
		withoutWords(contentVerbs, "tell", "ask", "write", "mention"),
		"content", "contenu", "objet", "subject", "body", "texte"), " "))
	companyLatestWords = wordSet(strings.Join(latestWords, " "))
	companyReplyWords  = wordSet(strings.Join(withWords(intentWords[IntentReply],
		"repondu", "responded", "responses", "answered", "answers"), " "))
)

// companyContentQuestion reports the dimensions the profile must not answer
// with a count: message content, the newest message, or a yes/no reply state.
// The yes/no cue must open the question ("Did X reply?", "Est-ce que X a
// répondu ?"); a later "are" in "how many conversations are waiting on my
// reply" is a count, not a yes/no frame. yesNoFrameQuestion is the package's
// one positional yes/no frame owner, shared with the intent parser.
func companyContentQuestion(question string, words []string) bool {
	folded := mail.Fold(strings.TrimSpace(question))
	switch {
	case containsAny(words, companyContentVerbs) || reportedContentFrame(folded):
		return true
	case containsAny(words, companyLatestWords) && containsAny(words, companyMessageWords):
		return true
	case containsAny(words, companyReplyWords) &&
		(yesNoFrameQuestion(folded) || containsAny(words, whoWords) ||
			companyInvertedReply(words)):
		return true
	}
	return false
}

// companyInvertedReply catches the French subject-first yes/no form
// ("Atelier Nord a-t-elle répondu ?", "Atelier Nord vous a-t-il répondu ?") without
// treating every later auxiliary as a yes/no frame. The existential
// "y a-t-il" is not a subject-first inversion: "Combien y a-t-il de réponses
// d'Atelier Nord ?" stays a profile count, so the a after y is skipped. The
// frame tokens themselves still come from the shared yesNoFrameWords owner;
// only the position logic is company-specific.
func companyInvertedReply(words []string) bool {
	for i := 1; i < len(words) && i <= 2; i++ {
		if words[i-1] == "y" {
			continue
		}
		if yesNoFrameWords[words[i]] {
			return true
		}
	}
	return false
}

// companyQuestionStopwords are question words that must not become person
// search terms even though the profile guard no longer accepts them as a
// company dimension. "news" and "happening" ask for mail content; they are
// not names.
var companyQuestionStopwords = wordSet("news actualite actualites happening happen happens happened")

// companyProfileQuestion reports whether the question is about the
// organization itself, so the profile's counts and relationship facts are an
// answer rather than a detour. Every word of the question must be either the
// organization's own name, a profile dimension, or a closed-class frame word;
// one unexplained content word ("portable", "facture", "prix", "projet")
// sends the question to the planner. It fails closed: an unknown word can
// only make the answer more careful.
func companyProfileQuestion(question string, org world.CompanyRow) bool {
	words := compactWords(question)
	if containsAny(words, whoWords) && containsAny(words, personDimensionWords) {
		return false
	}
	if companyContentQuestion(question, words) {
		return false
	}
	alias := companyAliasWords(org)
	for _, word := range words {
		if alias[word] || companyProfileWords[word] {
			continue
		}
		return false
	}
	return true
}

// companyAliasWords is every compact word and whole compact form of the
// organization's aliases. The whole form covers a hyphenated domain
// ("rive-education"), the words cover a multi-word label ("Clinique Exemple"), and
// the distinctive domain-base prefixes cover the short prose form
// ("cirrus" for cirrus-media.test) that matchCompany accepts.
func companyAliasWords(org world.CompanyRow) map[string]bool {
	out := map[string]bool{}
	for _, alias := range companyAliases(org) {
		for _, word := range compactWords(alias) {
			out[word] = true
		}
		if whole := mail.CompactFold(alias); whole != "" {
			out[whole] = true
		}
	}
	for _, base := range companyDomainBases(org) {
		target := []rune(mail.CompactFold(base))
		// The linker only accepts a prefix that leaves at least two runes
		// of the base unmatched; the profile vocabulary must not claim a
		// shorter prefix the linker would never resolve.
		for n := minCompanyPrefixRunes; n <= len(target)-2; n++ {
			out[string(target[:n])] = true
		}
	}
	return out
}

// Company-hint bounds. The hint is a bounded reading of the organization's
// own conversations, not a second full scan. The facts block keeps the top
// people and the next few events and commitments.
const (
	companyHintRecent       = 5
	companyHintThread       = 12
	companyHintHits         = 3
	companyPeopleLimit      = 5
	companyEntryLimit       = 3
	companyParticipantLimit = 6
)

// threadPage is one bounded recent page of an organization conversation,
// oldest message first, exactly as store.ThreadMessages returns it. The page
// also keeps the conversation facts that came with it, so the prompt needs
// no second read.
type threadPage struct {
	conversationID string
	messages       []store.ThreadMessage
	firstAt        string
	participants   []store.ThreadParticipant
}

// organizationFacts is the owner-safe profile metadata the planner renders
// before the numbered messages. It carries only the fields the Companies
// screen already shows: volume, thread states, people, the next events and
// the next commitments. No body or shared/delegate content is available in
// the profile, so none can be copied here.
type organizationFacts struct {
	Label       string
	Messages    int
	Sent        int
	Received    int
	PeopleTotal int
	Threads     int
	FirstAt     string
	LastAt      string
	States      []organizationState
	OpenThreads int
	People      []organizationPerson
	Events      []organizationEntry
	Deadlines   []organizationEntry
}

// organizationState is one thread state and its profile count, in the
// resolver's own order.
type organizationState struct {
	State string
	Count int
}

// organizationPerson is one counterpart the profile reports: display name,
// address and message count. The prompt pairs the two identifiers when both
// are known, so a person question can cite the address the mailbox stored.
type organizationPerson struct {
	Name     string
	Address  string
	Messages int
}

// organizationEntry is one event or commitment as the prompt shows it.
type organizationEntry struct {
	Subject string
	At      string
}

// companyEvidence retrieves the owner mail that grounds a specific question
// naming one organization. Three deterministic sources are merged, in this
// order: the last exchange (the newest message the owner sent and the
// newest message received after it), the organization-scoped search hits,
// and the latest and earliest message of each recent conversation. The
// last exchange gets the first numbered slots because it is what a
// reply-state question ("did they answer my last request?") asks about,
// and because the search leg is inbound-only and cannot see the owner's
// sent request. Every row is stored owner mail; nothing is summarized or
// invented here. The returned threadEvidence is the resolver's state for
// each recent conversation that contributed a numbered message, and facts
// are the profile's owner-safe counts, people and next events.
func (s *Service) companyEvidence(ctx context.Context, question string, org world.CompanyRow) ([]contextMessage, []threadEvidence, *organizationFacts, error) {
	var hits []search.Hit
	if domain := companySearchDomain(question, org); domain != "" {
		res, err := s.search.Search(ctx, search.Query{Text: question,
			Filter: store.SearchFilter{OwnerOnly: true, Sender: domain}, Limit: companyHintHits})
		if err != nil {
			return nil, nil, nil, err
		}
		hits = res.Hits
	}
	var profile world.ProfileView
	var facts *organizationFacts
	haveProfile := false
	if profiles := s.companyProfiles(); profiles != nil {
		prof, err := profiles.Profile(ctx, org.Key, companyProfileMonths, companyProfileDays)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, nil, ctx.Err()
			}
			s.log.Warn("qa company profile unavailable for retrieval", "reason", errorClass(err))
		} else {
			profile, haveProfile = prof, true
			facts = organizationFactsFrom(profile, org, s.zone())
		}
	}

	pages := make([]threadPage, 0, companyHintRecent)
	if haveProfile {
		for _, conversationID := range recentConversations(profile.Recent, companyHintRecent) {
			page, err := s.store.ThreadMessages(ctx, conversationID, companyHintThread, 0)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, nil, ctx.Err()
				}
				s.log.Warn("qa company thread unavailable", "reason", errorClass(err))
				continue
			}
			if len(page.Messages) > 0 {
				pages = append(pages, threadPage{conversationID: conversationID, messages: page.Messages,
					firstAt: page.FirstAt, participants: page.Participants})
			}
		}
	}

	candidates := make([]int64, 0, 2+len(hits)+2*len(pages))
	seen := map[int64]bool{}
	add := func(id int64) {
		if id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		candidates = append(candidates, id)
	}
	// The conversation that moved most recently contributes its task
	// statement first: it is the opening the latest exchange belongs to.
	if len(pages) > 0 {
		add(pages[0].messages[0].ID)
	}
	// The last exchange pairs the newest owner-sent message with the newest
	// message received after it. Only metadata is used here; the pair is
	// chosen by direction and instant, never by wording.
	if haveProfile {
		request := -1
		for i := range profile.Recent {
			if profile.Recent[i].Sent {
				request = i
				break
			}
		}
		if request >= 0 {
			add(profile.Recent[request].MessageID)
			for i := range profile.Recent {
				m := profile.Recent[i]
				if !m.Sent && m.Instant > profile.Recent[request].Instant {
					add(m.MessageID)
					break
				}
			}
		}
	}
	for _, h := range hits {
		add(h.RowID)
	}
	// The latest message is the other side's last word; the earliest of the
	// other recent conversations is the task statement when a thread is
	// longer than one page.
	for _, p := range pages {
		add(p.messages[len(p.messages)-1].ID)
	}
	for _, p := range pages {
		add(p.messages[0].ID)
	}
	if len(candidates) == 0 {
		return nil, nil, facts, nil
	}
	if limit := s.cfg.Int(SettingMaxMessages); len(candidates) > limit {
		candidates = candidates[:limit]
	}
	selected := make(map[int64]bool, len(candidates))
	for _, id := range candidates {
		selected[id] = true
	}
	// The stored dimensions sharpen the answer; a failed detail read must not
	// discard the summaries the organization evidence still has, and it never
	// fabricates unread or a low importance (owner: contextMessages).
	msgs, err := s.contextMessages(ctx, candidates, "company", "qa company context unavailable", nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	threads, err := s.companyThreadEvidence(ctx, pages, selected)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		s.log.Warn("qa company thread states unavailable", "reason", errorClass(err))
		threads = nil
	}
	return msgs, threads, facts, nil
}

// companyThreadEvidence resolves the state of each recent conversation that
// contributed a numbered message. The resolver sees every visible message
// of the conversation, including shared mail, so a colleague's reply is not
// mistaken for silence. The assessment contributes state, reason, visibility,
// message count, actor and last time; the already-loaded page contributes the
// first instant and its owner-safe participant list. No shared subject or
// body leaves this method.
func (s *Service) companyThreadEvidence(ctx context.Context, pages []threadPage, selected map[int64]bool) ([]threadEvidence, error) {
	if len(pages) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(pages))
	for _, p := range pages {
		ids = append(ids, p.conversationID)
	}
	states, err := s.threadStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]threadEvidence, 0, len(pages))
	for _, p := range pages {
		used := false
		for _, m := range p.messages {
			if selected[m.ID] {
				used = true
				break
			}
		}
		if !used {
			continue
		}
		assessment, ok := states[p.conversationID]
		if !ok {
			continue
		}
		subject := p.messages[len(p.messages)-1].Subject
		lastAt := assessment.LastAt
		if lastAt == "" {
			lastAt = p.messages[len(p.messages)-1].ReceivedAt
		}
		evidence := threadEvidenceFrom(assessment, subject, lastAt)
		evidence.Participants = participantLabels(p.participants)
		evidence.FirstAt = p.firstAt
		out = append(out, evidence)
	}
	return out, nil
}

// participantLabels is the compact, owner-safe participant list of one
// conversation page: the display name, the address when no name is stored,
// capped so a busy thread stays one short line. ThreadMessages already
// applies the owner content guard, so this only repeats what that read
// returned.
func participantLabels(participants []store.ThreadParticipant) []string {
	out := make([]string, 0, min(len(participants), companyParticipantLimit))
	for _, participant := range participants {
		label := oneLine(participant.Name)
		if label == "" {
			label = oneLine(participant.Address)
		}
		if label == "" {
			continue
		}
		out = append(out, label)
		if len(out) == companyParticipantLimit {
			break
		}
	}
	return out
}

// organizationFactsFrom copies the profile metadata the planner may cite.
// Counts keep the profile's own numbers; the states keep the resolver's six
// ordered levels, and an unparseable date is omitted rather than guessed.
func organizationFactsFrom(profile world.ProfileView, org world.CompanyRow, loc *time.Location) *organizationFacts {
	facts := &organizationFacts{
		Label:       companyLabel(profile, org),
		Messages:    profile.Messages,
		Sent:        profile.Sent,
		Received:    profile.Received,
		PeopleTotal: profile.PeopleContacted,
		Threads:     profile.Threads,
		FirstAt:     companyDay(profile.FirstAt, loc),
		LastAt:      companyDay(profile.LastAt, loc),
	}
	// The profile's OpenThreads list is capped; the state counts are the
	// complete open-thread population.
	facts.OpenThreads = threadintel.OpenTotal(profile.ThreadStates)
	for _, state := range threadintel.States {
		facts.States = append(facts.States, organizationState{
			State: string(state), Count: profile.ThreadStates[string(state)]})
	}
	for _, person := range profile.People {
		name := oneLine(person.Name)
		address := oneLine(person.Address)
		if name == "" && address == "" {
			continue
		}
		facts.People = append(facts.People, organizationPerson{Name: name, Address: address, Messages: person.Messages})
		if len(facts.People) == companyPeopleLimit {
			break
		}
	}
	for _, event := range profile.Events {
		entry := organizationEntry{Subject: event.Subject}
		if !event.Start.IsZero() {
			start := event.Start
			if loc != nil {
				start = start.In(loc)
			}
			entry.At = start.Format("2006-01-02 15:04")
		}
		if entry.Subject != "" || entry.At != "" {
			facts.Events = append(facts.Events, entry)
		}
		if len(facts.Events) == companyEntryLimit {
			break
		}
	}
	for _, deadline := range profile.Deadlines {
		entry := organizationEntry{Subject: deadline.Subject}
		if !deadline.Due.IsZero() {
			due := deadline.Due
			if loc != nil {
				due = due.In(loc)
			}
			entry.At = due.Format("2006-01-02")
		}
		if entry.Subject != "" || entry.At != "" {
			facts.Deadlines = append(facts.Deadlines, entry)
		}
		if len(facts.Deadlines) == companyEntryLimit {
			break
		}
	}
	return facts
}

// recentConversations returns the newest distinct conversation ids of the
// profile's recent messages, in the order they last moved.
func recentConversations(messages []world.CompanyMessage, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	seen := map[string]bool{}
	for _, m := range messages {
		if m.ConversationID == "" || seen[m.ConversationID] {
			continue
		}
		seen[m.ConversationID] = true
		out = append(out, m.ConversationID)
		if len(out) == limit {
			break
		}
	}
	return out
}

// companySearchDomain picks the organization domain the question names, or
// the first one, so retrieval is scoped to the organization's mail. It is a
// scope hint only: a wrong choice can miss evidence, never cite another
// organization as this one.
func companySearchDomain(question string, org world.CompanyRow) string {
	words := compactWords(question)
	for _, domain := range org.Domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		if containsCompactPhrase(words, mail.CompactFold(domain)) {
			return domain
		}
		if i := strings.IndexByte(domain, '.'); i > 0 && containsCompactPhrase(words, mail.CompactFold(domain[:i])) {
			return domain
		}
	}
	if len(org.Domains) > 0 {
		return strings.TrimSpace(org.Domains[0])
	}
	return ""
}
