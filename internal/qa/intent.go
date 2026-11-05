package qa

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/subject"
)

// The deterministic intents. Structured intents map to a priority filter;
// sender and freeform have their own paths. IntentCompany is not produced by
// parseIntent: it is the freeform question that entity linking resolves to
// exactly one known organization and that asks about that organization
// itself, answered from its profile. IntentTopic is the catalogue topic count
// the count scope gate answers from the topic census.
const (
	IntentReply        = "reply"
	IntentUrgent       = "urgent"
	IntentDeadline     = "deadline"
	IntentSecurity     = "security"
	IntentDecision     = "decision"
	IntentWorkload     = "workload"
	IntentPriority     = "priority"
	IntentSummaryToday = "summary_today"
	IntentSender       = "sender"
	IntentFreeform     = "freeform"
	IntentCompany      = "company"
	IntentTopic        = "topic"
)

// intentWords are the EN+FR keywords, already accent/case folded. A token
// is compared exactly; a phrase is looked for with surrounding spaces.
var intentWords = map[string][]string{
	IntentReply:        {"reply", "replies", "respond", "response", "answer", "awaiting", "repondre", "repond", "reponse", "reponses"},
	IntentUrgent:       {"urgent", "urgente", "urgentes", "urgents", "urgency", "asap", "pressing", "pressant", "pressante"},
	IntentDeadline:     {"deadline", "deadlines", "echeance", "echeances", "limite", "due", "before"},
	IntentSecurity:     {"security", "securite", "phishing", "scam", "scams", "spam", "spams", "suspicious", "suspicion", "fraude", "arnaque", "arnaques", "hameconnage"},
	IntentDecision:     {"decision", "decisions", "decide", "approve", "approval", "approbation", "approuve", "approuver", "valider", "validation"},
	IntentWorkload:     {"workload", "effort", "efforts", "charge", "travail", "travaux", "work"},
	IntentPriority:     {"priority", "priorities", "priorite", "priorites", "important", "importante", "importants", "importantes"},
	IntentSummaryToday: summaryIntentWords,
}

var todayWords = []string{"today", "aujourd", "hui", "journee"}

// questionMentionsToday is the one detection of the owner-day scope: the
// structured today answer and freeform retrieval both use it, so a question
// that says "today" cannot be grounded on another day.
func questionMentionsToday(question string) bool {
	return containsAny(todayWords, wordSet(mail.Fold(strings.TrimSpace(question))))
}

// contentWords and contentVerbs separate a question about a message, a person
// or an event from a mailbox count. "Who approved the invoice?" cannot be
// answered by a decision-filter count, and "What did Alice say today?" is not
// a daily summary. The cue sends the question to the general planner.
var contentWords = []string{"when", "where", "why", "how", "who", "whom", "quand", "pourquoi", "comment"}

// contentPhrases catch the French "who did" form without treating the
// relative pronoun in "qu'est-ce qui est urgent" as a content question.
var contentPhrases = []string{"quel jour", "qui a ", "qui m'a ", "qui vous a "}

var contentVerbs = []string{"say", "says", "said", "tell", "tells", "told", "write", "writes", "wrote",
	"mention", "mentions", "mentioned", "ask", "asks", "asked", "raconte", "racontent", "raconter",
	"explique", "expliquent", "expliquer", "repond", "repondu", "responded", "replied"}

// contentActionVerbs are the concrete acts a "who ..." question asks about.
// They are not intent keywords: the answer is a fact inside the message, and
// a mailbox count or a list of subjects cannot state it. An explicit count or
// list request keeps its computed answer, so the verb only routes to the
// planner when aggregateRequest is false.
var contentActionVerbs = []string{"approve", "approved", "approuve", "approuvee", "approuves", "approuvees",
	"decide", "decided", "pay", "paid", "paye", "payee", "payes", "payees",
	"sign", "signed", "signe", "signee", "signes", "signees", "authorize", "authorized",
	"dit", "dis", "dire", "ecrit", "ecrite", "ecrits", "ecrites",
	"envoie", "envoye", "envoyee", "envoyes", "envoyees", "demande", "demandee", "demandes",
	"approuver", "signer", "payer", "envoyer", "demander", "decider"}

// contentActionSet is the membership form of contentActionVerbs, derived once
// so the frame scan below cannot drift from the vocabulary.
var contentActionSet = func() map[string]bool {
	out := make(map[string]bool, len(contentActionVerbs))
	for _, word := range contentActionVerbs {
		out[word] = true
	}
	return out
}()

// whWords are the question words that frame a request for a fact. They are
// not content cues by themselves: "Quelles sont mes échéances ?" is still the
// deadline list. They only stop a yes/no obligation frame from exempting an
// action verb, so "What do I need to decide?" is content while "Do I need to
// decide?" is a decision question.
var whWords = []string{"what", "which", "who", "whom", "whose", "when", "where", "why", "how",
	"quel", "quelle", "quels", "quelles", "qui", "que", "quoi", "quand", "ou", "pourquoi",
	"comment", "combien", "lequel", "laquelle", "lesquels", "lesquelles"}

// yesNoFrameTokens are the auxiliaries, modals and frame tokens that can open
// a yes/no question. This is the one shared owner: the obligation-frame test,
// the reply-intent guard, and the company yes/no guard all read it.
var yesNoFrameTokens = []string{"do", "does", "did", "was", "were", "is", "are", "have", "has",
	"should", "must", "can", "could", "would", "will", "need",
	"dois", "doit", "devons", "devez", "doivent", "faut", "peux", "peut", "pouvons", "pouvez",
	"peuvent", "est", "sont", "ai", "as", "a", "avons", "avez", "ont"}

var yesNoFrameWords = wordSet(strings.Join(yesNoFrameTokens, " "))

// yesNoFrameAt reports a yes/no frame beginning at words[at]: a frame
// auxiliary or modal, or the French existential "y a-t-il" form. mail.Fold
// keeps the hyphen, and tokenization splits it, so the actual tokens are
// y, a, t, il.
func yesNoFrameAt(words []string, at int) bool {
	if at < 0 || at >= len(words) {
		return false
	}
	if yesNoFrameWords[words[at]] {
		return true
	}
	return at+3 < len(words) && words[at] == "y" && words[at+1] == "a" &&
		words[at+2] == "t" && words[at+3] == "il"
}

// yesNoFrameQuestion reports a question opened by a yes/no frame. It is the
// one position-aware owner for the reply guard and the company guard; a frame
// word anywhere in the question is not a frame ("Which messages need a
// reply?").
func yesNoFrameQuestion(folded string) bool {
	return yesNoFrameAt(wordList(folded), 0)
}

// obligationWords and obligationPhrases are the necessity cues that make a
// yes/no question ask whether an action is required, rather than what a
// message records. "should" is deliberately absent: "Should I approve this
// invoice?" is advice and must read the invoice, not a priority decision list.
var obligationWords = []string{"must",
	"dois", "doit", "devons", "devez", "doivent", "faut", "peux", "peut", "pouvons", "pouvez", "peuvent"}

var obligationPhrases = []string{"need to", "needs to", "have to", "has to", "had to", "ought to", "besoin de"}

// frenchYesNoComplementizers are stripped only for wh detection: "est-ce
// que" and "faut-il que" introduce a yes/no clause, so their "que" is not a
// content wh-word. A true "qu'est-ce que" question does not match these
// prefixes and stays content.
var frenchYesNoComplementizers = []string{"est-ce que ", "est-ce qu'", "est ce que ", "est ce qu'",
	"faut-il que ", "faut-il qu'", "faut il que ", "faut il qu'"}

func stripFrenchYesNoComplementizer(folded string) string {
	for _, prefix := range frenchYesNoComplementizers {
		if strings.HasPrefix(folded, prefix) {
			return strings.TrimSpace(folded[len(prefix):])
		}
	}
	return folded
}

// frenchWhereQuestion distinguishes the accented wh-word "où" from the folded
// conjunction "ou". Folding collapses both to "ou", so only the original text
// can tell them apart.
func frenchWhereQuestion(question string) bool {
	return strings.Contains(strings.ToLower(question), "où")
}

// hasWhWord reports a content wh-word in text. The folded token "ou" is the
// wh-word only when the original question carries the accent; otherwise it is
// the conjunction ("approuver ou signer").
func hasWhWord(question, text string) bool {
	tokens := wordSet(text)
	for _, word := range whWords {
		if word == "ou" {
			if frenchWhereQuestion(question) {
				return true
			}
			continue
		}
		if tokens[word] {
			return true
		}
	}
	return false
}

// yesNoObligation reports a yes/no question that asks whether an action is
// required: it has no wh-word, opens with a yes/no auxiliary or modal, and
// carries a necessity cue. Only that frame exempts a content action verb.
func yesNoObligation(question, folded string, words []string) bool {
	if len(words) == 0 || !yesNoFrameWords[words[0]] {
		return false
	}
	if hasWhWord(question, stripFrenchYesNoComplementizer(folded)) {
		return false
	}
	tokens := wordSet(folded)
	if containsAny(obligationWords, tokens) {
		return true
	}
	for _, phrase := range obligationPhrases {
		if strings.Contains(folded, phrase) {
			return true
		}
	}
	return false
}

// contentActionReported reports an action verb used as a reported act, not as
// the action of a yes/no obligation frame. "Do I need to decide?" asks whether
// a decision is required and keeps its decision intent; "What do I need to
// decide?", "Quelles échéances dois-je approuver ?" and "Which deadlines did I
// approve?" ask what a message records and belong to the planner.
func contentActionReported(question, folded string) bool {
	words := wordList(folded)
	for _, word := range words {
		if contentActionSet[word] {
			return !yesNoObligation(question, folded, words)
		}
	}
	return false
}

// contentQuestion is the one message-content predicate the intent parser and
// the computed-plan rejection guard share. Aggregate framing exempts only the
// action-verb cue; content words and verbs still route to the planner.
// asksForMessageContent applies the aggregate exemption to the whole
// predicate so a count plan can execute.
func contentQuestion(question, folded string) bool {
	tokens := wordSet(folded)
	if containsAny(contentWords, tokens) || containsAny(contentVerbs, tokens) {
		return true
	}
	if frenchWhereQuestion(question) {
		return true
	}
	if contentActionReported(question, folded) && !aggregateRequest(folded) {
		return true
	}
	for _, phrase := range contentPhrases {
		if strings.Contains(folded, phrase) {
			return true
		}
	}
	return reportedContentFrame(folded)
}

// asksForMessageContent reports a question whose answer is a fact inside a
// message: what someone wrote, who approved or paid something. Retrieval can
// cite the mail, but a computed message list cannot state the fact, so the
// planner must answer from the numbered evidence. It is the same predicate as
// parseIntent, with the aggregate exemption for count and list plans.
func asksForMessageContent(question string) bool {
	folded := mail.Fold(strings.TrimSpace(question))
	if aggregateRequest(folded) {
		return false
	}
	return contentQuestion(question, folded)
}

// aggregateRequest reports a count, ranking or explicit message-list
// request. It is token and phrase based: "account" must not exempt a content
// question because it contains "count".
func aggregateRequest(folded string) bool {
	tokens := wordSet(folded)
	for _, word := range []string{"count", "counts", "nombre", "total", "combien", "rank", "ranking", "classement"} {
		if tokens[word] {
			return true
		}
	}
	for _, phrase := range []string{
		"how many", "how much", "number of", "nombre de",
		"latest message", "last message", "newest message", "most recent message",
		"dernier message", "dernier echange", "latest exchange", "last exchange",
		"first message", "premier message",
		"which emails", "which messages", "which mail",
		"quels emails", "quels messages", "quels mails", "quelles emails", "quelles messages", "quelles mails",
		"list emails", "list messages", "list mail", "list of emails", "list of messages",
		"liste des emails", "liste des messages", "liste des mails", "liste d'emails", "liste de messages",
		"show messages", "show emails", "show mail",
	} {
		if strings.Contains(folded, phrase) {
			return true
		}
	}
	return false
}

// providerlessAggregateIntents is the fixed structured precedence a plain
// count may fall back to when no generator is configured. It is the same
// order parseIntent uses and deliberately excludes summary_today (not one
// category) and priority (there is no priority filter; it is the unfiltered
// total, so a "priority" count would answer the wrong question).
var providerlessAggregateIntents = []string{
	IntentSecurity, IntentDecision, IntentDeadline, IntentUrgent, IntentReply, IntentWorkload,
}

// aggregateCountWords and aggregateCountPhrases are the count cues the
// fallback requires. aggregateRequest also accepts a list request; a list is
// not one category total, so it never takes this path. Bare "number" is
// position-sensitive and handled by aggregateBareNumberRequest: it counts
// only immediately before a category or mailbox noun.
var (
	aggregateCountWords = []string{
		"count", "counts", "nombre", "total", "combien",
		"amount", "quantity", "quantite",
	}
	aggregateCountPhrases = []string{
		"how many", "how much", "number of", "nombre de",
		"amount of", "quantity of", "quantite de",
	}
)

// aggregateCountIdioms are removed before the residual scan: "in total" and
// "au total" are count idioms, not a time or scope cue.
var aggregateCountIdioms = []string{"in total", "au total"}

// aggregateCountCueTokens are every token of a count cue. They are removed,
// together with the one matched category word, before the fail-closed filler
// scan. The French partitives are cue components, not scope: a scope noun
// after them remains and is rejected by the allowlist.
var aggregateCountCueTokens = []string{
	"count", "counts", "nombre", "total", "combien",
	"amount", "quantity", "quantite",
	"how", "many", "much", "number",
	"of", "de", "du", "des", "d",
}

// aggregateCountFillers is the explicit allowlist for the residual wording of
// a plain count. It contains only question/filler words and the mailbox nouns;
// a scope, topic, status, date, person, organization or any other unlisted
// word keeps the question freeform. The list is deliberately small: adding a
// word permits more counts, and no blocklist can enumerate every scope.
var aggregateCountFillers = []string{
	// English first/second-person question frame and fillers. Third-person
	// pronouns are deliberately absent: "does she have" names a person scope
	// and must not answer a mailbox total.
	"do", "does", "did", "i", "you", "we",
	"have", "has", "had", "are", "is", "was", "were", "there",
	"my", "me", "our", "us", "your", "yours",
	"message", "messages", "mail", "mails", "email", "emails",
	"currently", "now", "right", "at", "the", "moment", "please",
	"need", "needs", "a",
	// French first/second-person question frame and fillers. The
	// third-person pronouns (il/elle/ils/elles/lui/leur/leurs) are absent
	// for the same reason as their English counterparts.
	"je", "tu", "nous", "vous", "on",
	"me", "moi", "te", "toi",
	"mon", "ma", "mes", "notre", "nos", "votre", "vos",
	"le", "la", "les", "l", "un", "une",
	"ai", "as", "a", "avons", "avez", "ont",
	"suis", "es", "est", "sommes", "etes", "sont",
	"y", "en", "svp", "stp", "merci", "plait",
	"courriel", "courriels", "courrier", "courriers",
	// Relation prepositions frame a question without scoping it: "How many
	// messages about invoices?" and "Combien de messages sur les factures ?"
	// name one topic and one relation. A scope noun after them still fails
	// the allowlist ("How many messages about budget invoices?").
	"about", "sur",
}

var (
	aggregateCountCueSet    = wordSet(strings.Join(aggregateCountCueTokens, " "))
	aggregateCountFillerSet = wordSet(strings.Join(aggregateCountFillers, " "))
	// aggregateCountMailboxNouns are the mailbox nouns that may follow a
	// person-frame auxiliary without naming a person. A category noun after
	// "does/did" is deliberately absent: "does Deadline have" names a person.
	aggregateCountMailboxNouns = wordSet(strings.Join(messageWords, " "))
	// aggregateCountCategoryNouns are every intent word, not only the
	// provider-less fallback categories. They are valid bare-number targets
	// and person names when they occupy the subject position.
	aggregateCountCategoryNouns = func() map[string]bool {
		out := make(map[string]bool)
		for _, words := range intentWords {
			for _, word := range words {
				out[word] = true
			}
		}
		return out
	}()
)

// aggregateCountPersonSubjects returns the person-subject tokens of a count
// wording. After "does"/"did", every subject except I/we/you and a mailbox
// noun is a person, including a category noun ("does Deadline have"). Before
// "has"/"have", a cue token or category noun is a person subject unless the
// next token is a first/second-person pronoun ("deadlines have I got") or the
// subject is a mailbox noun. The same list feeds the frame rejection and the
// category-name mention kept for the freeform resolver.
func aggregateCountPersonSubjects(folded string) []string {
	words := wordList(folded)
	out := make([]string, 0, 1)
	for i, word := range words {
		switch word {
		case "does", "did":
			if i+1 >= len(words) {
				continue
			}
			subject := words[i+1]
			if subject == "i" || subject == "we" || subject == "you" || aggregateCountMailboxNouns[subject] {
				continue
			}
			out = append(out, subject)
		case "has", "have":
			if i == 0 {
				continue
			}
			subject := words[i-1]
			if subject == "i" || subject == "we" || subject == "you" || aggregateCountMailboxNouns[subject] {
				continue
			}
			if !aggregateCountCueSet[subject] && !aggregateCountCategoryNouns[subject] {
				continue
			}
			if i+1 < len(words) && (words[i+1] == "i" || words[i+1] == "we" || words[i+1] == "you") {
				continue
			}
			out = append(out, subject)
		}
	}
	return out
}

// aggregatePersonFrame reports a third-person subject after "does"/"did" or
// a repeated cue/category before "has"/"have" ("does Deadline have", "Amount
// of deadlines Amount has?"). A first/second-person subject ("I", "we",
// "you") and a mailbox noun are not a person frame, so "do I have", "do we
// have" and "do you have" keep their plain-count path.
func aggregatePersonFrame(folded string) bool {
	return len(aggregateCountPersonSubjects(folded)) > 0
}

// aggregateBareNumberRequest reports bare "number" immediately before a
// category or mailbox noun ("Number deadlines", "Number urgent messages").
// "number of" is a phrase cue; "phone number" and "serial number" are not
// counts because the following token is not a category/mailbox noun.
func aggregateBareNumberRequest(words []string) bool {
	for i := 0; i+1 < len(words); i++ {
		if words[i] == "number" && (aggregateCountCategoryNouns[words[i+1]] || aggregateCountMailboxNouns[words[i+1]]) {
			return true
		}
	}
	return false
}

// aggregateCountRequest reports a count request: a count token or a count
// phrase. It is the one owner for "is this wording asking for a number?" and
// deliberately excludes ranking and list cues.
func aggregateCountRequest(folded string) bool {
	tokens := wordSet(folded)
	if containsAny(aggregateCountWords, tokens) {
		return true
	}
	if aggregateBareNumberRequest(wordList(folded)) {
		return true
	}
	for _, phrase := range aggregateCountPhrases {
		if strings.Contains(folded, phrase) {
			return true
		}
	}
	return false
}

// aggregateRepeatedCategoryWord reports the same matched category word
// repeated in the question, normalizing the common English plurals: -ies to
// -y ("replies" -> "reply", "securities" -> "security") and a trailing -s
// ("deadlines" -> "deadline", "urgents" -> "urgent"). One occurrence is the
// count's object; the same word again is a person name or a stutter ("How
// many replies Reply?"), never one plain category total. Different synonyms
// of the category ("deadlines ... due") are not this case.
func aggregateRepeatedCategoryWord(folded string, categoryWords map[string]bool) bool {
	seen := make(map[string]bool)
	for _, word := range wordList(folded) {
		if !categoryWords[word] {
			continue
		}
		stem := word
		switch {
		case len(stem) > 4 && strings.HasSuffix(stem, "ies"):
			stem = strings.TrimSuffix(stem, "ies") + "y"
		case len(stem) > 3 && strings.HasSuffix(stem, "s"):
			stem = strings.TrimSuffix(stem, "s")
		}
		if seen[stem] {
			return true
		}
		seen[stem] = true
	}
	return false
}

// aggregateStructuredIntent returns the one structured category a plain
// provider-less count asks about. It requires a count cue, so a list-only
// request (which aggregateRequest also accepts) does not qualify, and it
// rejects person/latest/content/action wording. After removing the count cue
// and the one matched category word, every residual token must be in the
// explicit filler allowlist; any unlisted scope, topic, status or date word
// keeps the question freeform. The caller still checks the organization book
// and the person resolver: this pure helper only sees the wording.
func aggregateStructuredIntent(question string) (string, bool) {
	folded := mail.Fold(strings.TrimSpace(question))
	tokens := wordSet(folded)
	if !aggregateCountRequest(folded) {
		return "", false
	}
	if aggregatePersonFrame(folded) {
		return "", false
	}
	if containsAny(personWords, tokens) || containsAny(latestWords, tokens) ||
		containsAny(todayWords, tokens) || containsAny(contentVerbs, tokens) ||
		contentActionReported(question, folded) {
		return "", false
	}
	matched := ""
	for _, intent := range providerlessAggregateIntents {
		if !containsAny(intentWords[intent], tokens) {
			continue
		}
		if matched != "" {
			// Two category words are a comparison or a compound question;
			// one category total would silently drop the other.
			return "", false
		}
		matched = intent
	}
	if matched == "" {
		return "", false
	}
	categoryWords := wordSet(strings.Join(intentWords[matched], " "))
	if aggregateRepeatedCategoryWord(folded, categoryWords) {
		// A repeated category noun is a person name or a list, not one plain
		// category total: "How many deadlines Deadline?" must not answer the
		// mailbox-wide deadline count. The auxiliary frames above cover
		// "does Deadline have"; this covers the bare appositive.
		return "", false
	}
	residual := folded
	for _, idiom := range aggregateCountIdioms {
		residual = strings.ReplaceAll(residual, idiom, " ")
	}
	for _, word := range wordList(residual) {
		if aggregateCountCueSet[word] || categoryWords[word] {
			continue
		}
		if !aggregateCountFillerSet[word] {
			return "", false
		}
	}
	return matched, true
}

// aggregateTopicIntent returns the one catalogue topic a plain provider-less
// count asks about. It mirrors aggregateStructuredIntent's gate exactly: a
// count cue is required; a person frame, latest/today wording, a content verb
// or a reported action is rejected; one catalogue path must match (the
// longest alias wins, so "status updates" is the project status subtopic and
// not the news topic); the same alias word repeated is a person name or a
// stutter, not one topic total; and after removing the count cue and the
// matched alias words every residual token must be in the same filler
// allowlist. Any unlisted scope, date or status word — or a second category —
// keeps the question freeform. The caller checks the priority intents first,
// so topic never shadows a reply/urgent/deadline/security/decision/workload
// count.
func aggregateTopicIntent(question string) (subject.Phrase, []string, bool) {
	folded := mail.Fold(strings.TrimSpace(question))
	tokens := wordSet(folded)
	if !aggregateCountRequest(folded) {
		return subject.Phrase{}, nil, false
	}
	if aggregatePersonFrame(folded) {
		return subject.Phrase{}, nil, false
	}
	if containsAny(personWords, tokens) || containsAny(latestWords, tokens) ||
		containsAny(todayWords, tokens) || containsAny(contentVerbs, tokens) ||
		contentActionReported(question, folded) {
		return subject.Phrase{}, nil, false
	}
	phrase, alias, ok := subject.Match(wordList(folded))
	if !ok {
		return subject.Phrase{}, nil, false
	}
	aliasWords := wordSet(strings.Join(alias, " "))
	if aggregateRepeatedCategoryWord(folded, aliasWords) {
		// A repeated topic noun is a person name or a stutter ("How many
		// invoices Invoice?"), never one topic total. The residual scan would
		// otherwise skip every occurrence of the alias word.
		return subject.Phrase{}, nil, false
	}
	residual := folded
	for _, idiom := range aggregateCountIdioms {
		residual = strings.ReplaceAll(residual, idiom, " ")
	}
	for _, word := range wordList(residual) {
		if aggregateCountCueSet[word] || aliasWords[word] {
			continue
		}
		if !aggregateCountFillerSet[word] {
			// Any unlisted word — a scope, a date, a status, a second
			// category — keeps its freeform path; the allowlist is the only
			// thing standing between a topic count and a mailbox-wide total.
			return subject.Phrase{}, nil, false
		}
	}
	return phrase, alias, true
}

// priorityPhrases are the "what should I read first" cue. The bare token
// "first" is not a priority word: "what was the first message?" is a content
// question, not a request for the priority list.
var priorityPhrases = []string{"read first", "read next", "lire en premier", "commencer par"}

// personWords and aggregateWords are a structural guard, not a new intent
// table: a question about a person and a ranking ("who is the most
// important?", "top senders") belongs to the general planner, even when it
// contains a word such as "important" that also names a mail intent.
var personWords = []string{"who", "whom", "qui", "contact", "contacts", "destinataire", "destinataires",
	"expediteur", "expediteurs", "sender", "senders", "recipient", "recipients", "personne", "personnes",
	"correspondent", "correspondents", "correspondant", "correspondants"}

var aggregateWords = []string{"most", "top", "many", "plus", "combien", "rank", "ranking", "classement",
	"busiest", "premier", "premiers", "premiere", "premieres"}

// latestWords and messageWords are structural cues, not new intents. A
// latest-message or yes/no reply question is a content question: a
// mailbox-wide priority filter cannot answer it, even when a word such as
// "reply" or "urgent" also names a structured intent.
var latestWords = []string{"last", "latest", "recent", "recente", "recents", "recentes",
	"dernier", "derniere", "derniers", "dernieres"}

var messageWords = []string{"message", "messages", "mail", "mails", "email", "emails", "courriel", "courriels"}

// senderPrefixes are matched at the start of the folded question, longest
// first. The text after the prefix is what search receives.
var senderPrefixes = []string{
	"emails from ", "messages from ", "mails from ", "mail from ",
	"courriels de ", "courriel de ", "messages de ", "emails de ", "mail de ",
	"de la part de ", "sent by ", "sender: ", "sender:", "sender ",
	"expediteur : ", "expediteur :", "expediteur: ", "expediteur:", "expediteur ", "from: ", "from:",
}

// replyWaitingWords and replyListPhrases identify a waiting-list question:
// it asks about replies that are owed, pending or awaited, not about one
// specific reply. They reuse the reply intent's own wording instead of a
// general message-word bag.
var (
	replyWaitingWords = []string{"attente", "attendent", "attend", "attends", "awaiting", "waiting"}
	replyListPhrases  = []string{"need a reply", "needs a reply", "need an answer", "needs an answer",
		"need replies", "needs replies", "besoin d'une reponse", "besoin de reponse"}
)

func replyListQuestion(folded string, tokens map[string]bool) bool {
	if containsAny(replyWaitingWords, tokens) {
		return true
	}
	for _, phrase := range replyListPhrases {
		if strings.Contains(folded, phrase) {
			return true
		}
	}
	return false
}

// parseIntent returns the deterministic intent and, for sender, its text.
// Precedence: explicit sender, an explicit summary+today, security,
// decision, deadline, urgent, reply, workload, priority, a today-only
// question (summary_today), freeform.
func parseIntent(question string) (intent, senderText string) {
	folded := mail.Fold(strings.TrimSpace(question))
	tokens := wordSet(folded)
	if text, ok := senderArg(question, folded); ok {
		return IntentSender, text
	}
	// A person/contact ranked by a superlative is a different question from
	// a count of important or urgent mail. Let the general planner decide it
	// instead of letting a mail keyword capture it.
	if containsAny(personWords, tokens) && containsAny(aggregateWords, tokens) {
		return IntentFreeform, ""
	}
	// "Did X reply?", "Qui a répondu ?", and "latest message from X" ask
	// about one message or one thread. A mailbox-wide reply filter would
	// answer a different question.
	if contentQuestion(question, folded) {
		return IntentFreeform, ""
	}
	// A count with a person subject must never parse directly to a category,
	// even when a generator is configured: "Number deadlines does Deadline
	// have?" and "Amount of deadlines Amount has?" are not mailbox totals.
	if aggregateCountRequest(folded) && aggregatePersonFrame(folded) {
		return IntentFreeform, ""
	}
	// "Did X reply?" is about one specific reply; a waiting-list question
	// such as "Which messages need a reply?" or "Y a-t-il des réponses en
	// attente ?" keeps the reply intent. The yes/no frame must open the
	// question, and a waiting/list cue marks the list form even when the
	// question is wh-framed ("What needs a reply today?").
	if containsAny(intentWords[IntentReply], tokens) &&
		(containsAny(personWords, tokens) ||
			((yesNoFrameQuestion(folded) || containsAny(whWords, tokens)) &&
				!replyListQuestion(folded, tokens))) {
		return IntentFreeform, ""
	}
	if containsAny(latestWords, tokens) &&
		(containsAny(messageWords, tokens) || containsAny(intentWords[IntentReply], tokens)) {
		return IntentFreeform, ""
	}
	for _, phrase := range priorityPhrases {
		if strings.Contains(folded, phrase) {
			if aggregateCountRequest(folded) {
				// "Number of messages to read first?" is a count, and
				// there is no priority filter: the unfiltered total is
				// not the answer.
				return IntentFreeform, ""
			}
			return IntentPriority, ""
		}
	}
	hasToday := containsAny(todayWords, tokens)
	if containsAny(intentWords[IntentSummaryToday], tokens) && hasToday {
		return IntentSummaryToday, ""
	}
	for _, in := range []string{IntentSecurity, IntentDecision, IntentDeadline, IntentUrgent, IntentReply, IntentWorkload, IntentPriority} {
		if containsAny(intentWords[in], tokens) {
			if aggregateCountRequest(folded) && aggregateRepeatedCategoryWord(folded, wordSet(strings.Join(intentWords[in], " "))) {
				// A repeated category noun is a person name or a list, not
				// one mailbox total, even when a generator is configured.
				return IntentFreeform, ""
			}
			if in == IntentPriority && aggregateCountRequest(folded) {
				// There is no priority filter; a priority count must not
				// answer the unfiltered total. The provider-less fallback
				// already excludes priority, and this keeps a count whose
				// language reaches parseIntent directly (for example
				// "Combien de messages importants ?") off that path too.
				return IntentFreeform, ""
			}
			return in, ""
		}
	}
	if hasToday {
		return IntentSummaryToday, ""
	}
	return IntentFreeform, ""
}

// senderArg reads an explicit sender request, preserving the original
// spelling of the sender for search. It uses the folded byte offsets so
// accents and compatibility forms cannot truncate the original.
func senderArg(question, folded string) (string, bool) {
	_, offsets := mail.FoldMap(question)
	for _, prefix := range senderPrefixes {
		if !strings.HasPrefix(folded, prefix) {
			continue
		}
		end := len(question)
		if len(prefix) < len(folded) {
			end = offsets[len(prefix)]
		}
		text := strings.TrimSpace(question[end:])
		text = strings.TrimRight(text, " ?!.:;,")
		text = strings.TrimSpace(text)
		if text != "" && !containsAny(aggregateWords, wordSet(mail.Fold(text))) {
			return text, true
		}
	}
	return "", false
}

// wordList splits folded text into word tokens in order.
func wordList(folded string) []string {
	return strings.FieldsFunc(folded, func(r rune) bool { return !mail.IsWordRune(r) })
}

// withWords returns base plus extra without reusing base's backing array, so
// a derived vocabulary can never mutate the owner it derives from.
func withWords(base []string, extra ...string) []string {
	out := make([]string, len(base), len(base)+len(extra))
	copy(out, base)
	return append(out, extra...)
}

// withoutWords returns base without the dropped words, without reusing
// base's backing array.
func withoutWords(base []string, drop ...string) []string {
	skip := make(map[string]bool, len(drop))
	for _, word := range drop {
		skip[word] = true
	}
	out := make([]string, 0, len(base))
	for _, word := range base {
		if !skip[word] {
			out = append(out, word)
		}
	}
	return out
}

// reportedContentFrame is the English reported-speech frame ("what did X
// tell me", "what does X say") shared by the intent parser and the company
// profile guard. A bare profile imperative such as "Tell me about X" is not
// a content question; the frame is.
func reportedContentFrame(folded string) bool {
	return strings.Contains(folded, "what did ") || strings.Contains(folded, "what does ")
}

func wordSet(folded string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordList(folded) {
		if w != "" {
			out[w] = true
		}
	}
	return out
}

// containsAny reports whether words shares one entry with set. It is the one
// word-set membership test of the question vocabulary: the intent, company,
// entity and summary matchers all ask the same question, so a matcher cannot
// drift on what "contains" means.
func containsAny(words []string, set map[string]bool) bool {
	for _, word := range words {
		if set[word] {
			return true
		}
	}
	return false
}

// intentFilter maps a structured intent to the priority filter it reads.
func intentFilter(intent string) priority.Filter {
	switch intent {
	case IntentReply:
		return priority.FilterReply
	case IntentUrgent:
		return priority.FilterUrgent
	case IntentSecurity:
		return priority.FilterSecurity
	case IntentDecision:
		return priority.FilterDecision
	case IntentDeadline:
		return priority.FilterDeadline
	case IntentWorkload:
		return priority.FilterWorkload
	default:
		return priority.FilterAll
	}
}
