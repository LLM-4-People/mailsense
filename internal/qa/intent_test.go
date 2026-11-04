package qa

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestIntentParsing(t *testing.T) {
	cases := []struct {
		question string
		intent   string
		sender   string
	}{
		{"Qu'est-ce qui est urgent ?", IntentUrgent, ""},
		{"URGENT: que dois-je faire ?", IntentUrgent, ""},
		{"Quelles sont mes échéances ?", IntentDeadline, ""},
		{"What is due this week?", IntentDeadline, ""},
		{"Y a-t-il des problèmes de sécurité ?", IntentSecurity, ""},
		{"Is this phishing?", IntentSecurity, ""},
		{"Faut-il approuver cette facture ?", IntentDecision, ""},
		{"Do I need to decide?", IntentDecision, ""},
		{"Combien de travail demandent ces messages ?", IntentWorkload, ""},
		{"Quels sont les plus importants ?", IntentPriority, ""},
		{"What should I read first?", IntentPriority, ""},
		{"Combien de messages importants ?", IntentFreeform, ""},
		{"Quelles réponses attendent une réponse ?", IntentReply, ""},
		{"Résume ma journée", IntentSummaryToday, ""},
		{"What happened today?", IntentSummaryToday, ""},
		{"Tell me about the budget discussion", IntentFreeform, ""},
		// Content questions contain words that also name a mailbox filter
		// ("today", "approve", "due", "first"); they must reach the planner,
		// never a count of filtered mail.
		{"What did Alice say today?", IntentFreeform, ""},
		{"When is the payment due?", IntentFreeform, ""},
		{"Who approved the invoice?", IntentFreeform, ""},
		{"What was the first message?", IntentFreeform, ""},
		{"Quel jour la facture a-t-elle été envoyée ?", IntentFreeform, ""},
		{"Quand la facture a-t-elle été envoyée ?", IntentFreeform, ""},
		{"Qui est le plus important ?", IntentFreeform, ""},
		{"Qui m'écrit le plus ?", IntentFreeform, ""},
		{"Who is the most important sender?", IntentFreeform, ""},
		{"Who sent me the most mail?", IntentFreeform, ""},
		{"Top senders", IntentFreeform, ""},
		{"destinataires avec qui j'ai le plus discuté", IntentFreeform, ""},
		{"sender:Alice", IntentSender, "Alice"},
		{"From: bob@example.com", IntentSender, "bob@example.com"},
		{"Messages de Alice Martin", IntentSender, "Alice Martin"},
		{"De la part de Bob", IntentSender, "Bob"},
		{"Who sent the budget email?", IntentFreeform, ""},
	}
	for _, tc := range cases {
		got, sender := parseIntent(tc.question)
		if got != tc.intent || sender != tc.sender {
			t.Errorf("%q: (%q, %q), want (%q, %q)", tc.question, got, sender, tc.intent, tc.sender)
		}
	}
}

// TestAggregateCountSynonymsUseTheCanonicalCueOwner pins the one count-cue
// owner: amount, quantity, quantité and bare "number" before a category noun
// are recognized by aggregateCountRequest, and each component is declared
// once in aggregateCountWords or aggregateCountPhrases (no parallel list).
// "number" alone is not a cue; it needs the adjacent category/mailbox noun.
func TestAggregateCountSynonymsUseTheCanonicalCueOwner(t *testing.T) {
	recognized := []string{
		"Amount of deadlines",
		"amount",
		"amount of",
		"Quantity of urgent messages",
		"quantity",
		"quantity of",
		"Quantité d'échéances",
		"quantité",
		"quantité de",
		"Number deadlines",
		"Number urgent messages",
		"number of deadlines",
	}
	for _, question := range recognized {
		if !aggregateCountRequest(mail.Fold(question)) {
			t.Errorf("aggregateCountRequest(%q) = false; the count synonym is not recognized", question)
		}
	}
	notCounts := []string{
		"phone number for Bob",
		"what is the number",
		"serial number for the invoice",
	}
	for _, question := range notCounts {
		if aggregateCountRequest(mail.Fold(question)) {
			t.Errorf("aggregateCountRequest(%q) = true; a non-count number was recognized", question)
		}
	}

	seen := map[string]string{}
	for _, word := range aggregateCountWords {
		if prior, ok := seen[word]; ok {
			t.Errorf("count cue %q declared twice (%s and words)", word, prior)
		}
		seen[word] = "words"
	}
	for _, phrase := range aggregateCountPhrases {
		if prior, ok := seen[phrase]; ok {
			t.Errorf("count cue %q declared twice (%s and phrases)", phrase, prior)
		}
		seen[phrase] = "phrases"
	}
	for _, want := range []string{"amount", "quantity", "quantite"} {
		if seen[want] == "" {
			t.Errorf("count synonym %q is not in the canonical cue list", want)
		}
	}
	for _, want := range []string{"amount of", "quantity of", "quantite de"} {
		if seen[want] == "" {
			t.Errorf("count synonym phrase %q is not in the canonical cue list", want)
		}
	}
}

// TestAggregateRepeatedCategoryWordNormalizesPlurals pins the -ies -> -y and
// trailing-s stems for the bare-category guard. The reply row uses the real
// reply vocabulary; the security/workload rows use the pair directly because
// "securities" and "workloads" are not declared category words.
func TestAggregateRepeatedCategoryWordNormalizesPlurals(t *testing.T) {
	cases := []struct {
		name     string
		question string
		words    map[string]bool
		want     bool
	}{
		{"reply -ies", "How many replies Reply?", wordSet(strings.Join(intentWords[IntentReply], " ")), true},
		{"deadline -s", "How many deadlines Deadline?", wordSet(strings.Join(intentWords[IntentDeadline], " ")), true},
		{"urgent -s", "How many urgents Urgent?", wordSet(strings.Join(intentWords[IntentUrgent], " ")), true},
		{"decision -s", "How many decisions Decision?", wordSet(strings.Join(intentWords[IntentDecision], " ")), true},
		{"security -ies", "How many securities Security?", wordSet("security securities"), true},
		{"workload -s", "How many workloads Workload?", wordSet("workload workloads"), true},
		{"reply control", "How many reply messages?", wordSet(strings.Join(intentWords[IntentReply], " ")), false},
		{"deadline distinct synonym", "How many deadlines are due?", wordSet(strings.Join(intentWords[IntentDeadline], " ")), false},
	}
	for _, tc := range cases {
		if got := aggregateRepeatedCategoryWord(mail.Fold(tc.question), tc.words); got != tc.want {
			t.Errorf("%s: %q -> %v, want %v", tc.name, tc.question, got, tc.want)
		}
	}
}

// contentActionCase is one row of the content-action routing contract. Every
// row carries an explicit expected intent and an independent wantContent
// oracle; the predicate test does not compare the two production functions
// with each other.
type contentActionCase struct {
	name        string
	question    string
	want        string
	wantContent bool
}

func contentActionRoutingCases() []contentActionCase {
	return []contentActionCase{
		{"S1 aggregate count stays deadline", "Combien d'échéances ai-je approuvées ?", IntentDeadline, false},
		{"S2 explicit email list stays decision", "Which emails did I approve?", IntentDecision, false},
		{"S3 explicit message list stays decision", "List messages I need to approve", IntentDecision, false},
		{"S4 plain deadline question stays deadline", "Quelles sont mes échéances ?", IntentDeadline, false},
		{"S5 how many deadlines stays freeform", "How many deadlines do I have?", IntentFreeform, false},
		{"S6 count of approved messages stays freeform", "Combien de messages ai-je approuvés ?", IntentFreeform, false},
		{"F1 approved deadlines reach the planner", "Quelles échéances ai-je approuvées ?", IntentFreeform, true},
		{"F2 signed deadlines reach the planner", "Quelles échéances ai-je signées ?", IntentFreeform, true},
		{"F3 paid deadlines reach the planner", "Quelles échéances ai-je payées ?", IntentFreeform, true},
		{"F4 approved decisions reach the planner", "Quelles décisions ai-je approuvées ?", IntentFreeform, true},
		{"F5 approved deadlines in English reach the planner", "Which deadlines did I approve?", IntentFreeform, true},
		{"F6 sent deadlines stay freeform", "Quelles échéances ai-je envoyées ?", IntentFreeform, true},
	}
}

// contentActionWhCases are questions about an act recorded in a message. A
// wh-word frames the question as a fact request even when the action verb is
// separated by a modal or is a French infinitive.
func contentActionWhCases() []contentActionCase {
	return []contentActionCase{
		{"W1 what to decide", "What do I need to decide about the contract?", IntentFreeform, true},
		{"W2 which deadline to approve", "Which deadline do I need to approve?", IntentFreeform, true},
		{"W3 which deadlines to approve", "Which deadlines do I need to approve?", IntentFreeform, true},
		{"W4 what to sign", "What do I need to sign before Friday?", IntentFreeform, true},
		{"W5 what Alice wanted approved", "What did Alice want me to approve?", IntentFreeform, true},
		{"W6 French approved deadlines", "Quelles échéances dois-je approuver ?", IntentFreeform, true},
		{"W7 French what to approve", "Que dois-je approuver ?", IntentFreeform, true},
	}
}

// contentActionNecessityCases ask whether an action is required. They have no
// wh-word, start with a necessity auxiliary or modal and carry an obligation
// cue, so they keep the deterministic decision intent.
func contentActionNecessityCases() []contentActionCase {
	return []contentActionCase{
		{"N1 need to decide", "Do I need to decide?", IntentDecision, false},
		{"N2 faut-il approuver", "Faut-il approuver cette facture ?", IntentDecision, false},
		{"N3 dois-je approuver", "Dois-je approuver cette facture ?", IntentDecision, false},
		{"N4 must approve", "Must I approve this invoice?", IntentDecision, false},
	}
}

// contentActionAdviceCases ask for advice, not whether an action is required.
// A priority decision list cannot read the invoice, so they are freeform.
func contentActionAdviceCases() []contentActionCase {
	return []contentActionCase{
		{"A1 should approve", "Should I approve this invoice?", IntentFreeform, true},
		{"A2 should pay", "Should I pay this invoice?", IntentFreeform, true},
	}
}

// contentActionFrenchFrameCases pin the French yes/no complementizers and the
// accented où wh-word. "que" after est-ce/faut-il is a complementizer, while
// "ou" without the accent is the conjunction.
func contentActionFrenchFrameCases() []contentActionCase {
	return []contentActionCase{
		{"FR1 est-ce que dois-je", "Est-ce que je dois approuver cette facture ?", IntentDecision, false},
		{"FR2 faut-il que j'approuve", "Faut-il que j'approuve cette facture ?", IntentDecision, false},
		{"FR3 approve or sign", "Dois-je approuver ou signer cette facture ?", IntentDecision, false},
		{"FR4 accented where", "Où est la facture ?", IntentFreeform, true},
		{"FR5 what to approve", "Qu’est-ce que je dois approuver ?", IntentFreeform, true},
	}
}

// contentActionOracleExtras are negative and boundary controls outside the wh
// list: a freeform content question with no wh-word, and an aggregate question
// whose computed count plan is allowed.
func contentActionOracleExtras() []contentActionCase {
	return []contentActionCase{
		{"X1 content without wh", "Did I approve the invoice?", IntentFreeform, true},
		{"X2 aggregate content", "How many emails did Alice say?", IntentFreeform, false},
	}
}

// TestIntentContentActionNeedsAPlanner separates a count or list of mail
// (a structured priority answer) from a question about the act recorded inside
// a message. The concrete action verb is a content cue, but it must not
// override an explicit aggregate request: "which emails did I approve?" is
// still the decision list, while "which deadlines did I approve?" asks what
// the messages say and belongs to the planner.
func TestIntentContentActionNeedsAPlanner(t *testing.T) {
	cases := contentActionRoutingCases()
	misroutes := 0
	for _, tc := range cases {
		got, sender := parseIntent(tc.question)
		if got != tc.want || sender != "" {
			misroutes++
			t.Errorf("%s: %q -> (%q, %q), want (%q, \"\")", tc.name, tc.question, got, sender, tc.want)
		}
	}
	t.Logf("misroutes: %d/%d", misroutes, len(cases))
}

// TestContentActionWhQuestionsReachThePlanner proves a wh-framed question
// about a recorded act is content, whether the verb follows "to", is a French
// infinitive, or is separated from the wh-word by a modal.
func TestContentActionWhQuestionsReachThePlanner(t *testing.T) {
	cases := contentActionWhCases()
	misroutes := 0
	for _, tc := range cases {
		got, sender := parseIntent(tc.question)
		if got != tc.want || sender != "" {
			misroutes++
			t.Errorf("%s: %q -> (%q, %q), want (%q, \"\")", tc.name, tc.question, got, sender, tc.want)
		}
	}
	t.Logf("misroutes: %d/%d", misroutes, len(cases))
}

// TestContentActionYesNoModalStaysStructured proves the frame rule preserves
// the necessity questions the existing intent table owns.
func TestContentActionYesNoModalStaysStructured(t *testing.T) {
	for _, tc := range contentActionNecessityCases() {
		if got, sender := parseIntent(tc.question); got != tc.want || sender != "" {
			t.Errorf("%s: %q -> (%q, %q), want (%q, \"\")", tc.name, tc.question, got, sender, tc.want)
		}
	}
}

// TestContentActionAdviceVsNecessity separates advice from necessity. Advice
// is freeform because the planner must read the invoice; necessity keeps the
// deterministic decision intent.
func TestContentActionAdviceVsNecessity(t *testing.T) {
	for _, tc := range contentActionAdviceCases() {
		if got, sender := parseIntent(tc.question); got != IntentFreeform || sender != "" {
			t.Errorf("%s: %q -> (%q, %q), want (freeform, \"\")", tc.name, tc.question, got, sender)
		}
	}
	for _, tc := range contentActionNecessityCases() {
		if got, sender := parseIntent(tc.question); got != IntentDecision || sender != "" {
			t.Errorf("%s: %q -> (%q, %q), want (decision, \"\")", tc.name, tc.question, got, sender)
		}
	}
}

// TestContentActionFrenchYesNoFrames pins the French complementizer handling
// and the accented où/ou distinction, including the true wh questions that
// must stay content.
func TestContentActionFrenchYesNoFrames(t *testing.T) {
	mismatches := 0
	for _, tc := range contentActionFrenchFrameCases() {
		got, sender := parseIntent(tc.question)
		if got != tc.want || sender != "" {
			mismatches++
			t.Errorf("%s: %q -> (%q, %q), want (%q, \"\")", tc.name, tc.question, got, sender, tc.want)
		}
		if content := asksForMessageContent(tc.question); content != tc.wantContent {
			mismatches++
			t.Errorf("%s: asksForMessageContent(%q) = %v, want %v", tc.name, tc.question, content, tc.wantContent)
		}
	}
	t.Logf("mismatches: %d/%d", mismatches, len(contentActionFrenchFrameCases()))
}

// TestContentPredicatesAgree asserts both production predicates against an
// explicit wantContent oracle. It also includes a plain structured control
// (S4), a content question outside the wh list (X1), and an aggregate content
// question whose computed plan must remain allowed (X2).
func TestContentPredicatesAgree(t *testing.T) {
	var cases []contentActionCase
	cases = append(cases, contentActionRoutingCases()...)
	cases = append(cases, contentActionWhCases()...)
	cases = append(cases, contentActionNecessityCases()...)
	cases = append(cases, contentActionAdviceCases()...)
	cases = append(cases, contentActionFrenchFrameCases()...)
	cases = append(cases, contentActionOracleExtras()...)

	disagreements := 0
	for _, tc := range cases {
		if got, _ := parseIntent(tc.question); got != tc.want {
			disagreements++
			t.Errorf("%s: parseIntent(%q) = %q, want %q", tc.name, tc.question, got, tc.want)
		}
		if got := asksForMessageContent(tc.question); got != tc.wantContent {
			disagreements++
			t.Errorf("%s: asksForMessageContent(%q) = %v, want %v", tc.name, tc.question, got, tc.wantContent)
		}
	}
	t.Logf("oracle disagreements: %d/%d assertions (%d cases)", disagreements, 2*len(cases), len(cases))
}

// TestReplyQuestionsStayStructured pins the reply guard's frame position: a
// waiting-list or count question stays a reply-list intent, while a yes/no
// question about one specific reply reaches the planner.
func TestReplyQuestionsStayStructured(t *testing.T) {
	structured := []string{
		"Which messages need a reply?",
		"What needs a reply today?",
		"Y a-t-il des réponses en attente ?",
		"Combien y a-t-il de réponses en attente ?",
	}
	misroutes := 0
	for _, question := range structured {
		if got, sender := parseIntent(question); got != IntentReply || sender != "" {
			misroutes++
			t.Errorf("%q -> (%q, %q), want (reply, \"\")", question, got, sender)
		}
	}
	freeform := []string{
		"Did they reply?",
		"Should I reply?",
		"Do I need to reply?",
	}
	for _, question := range freeform {
		if got, sender := parseIntent(question); got != IntentFreeform || sender != "" {
			misroutes++
			t.Errorf("%q -> (%q, %q), want (freeform, \"\")", question, got, sender)
		}
	}
	frames := map[string]bool{
		"Y a-t-il des réponses en attente ?":        true,
		"A-t-il répondu ?":                          true,
		"Est-ce que tu as répondu ?":                true,
		"Faut-il répondre ?":                        true,
		"Which messages need a reply?":              false,
		"Combien y a-t-il de réponses en attente ?": false,
	}
	for question, want := range frames {
		if got := yesNoFrameQuestion(mail.Fold(question)); got != want {
			misroutes++
			t.Errorf("yesNoFrameQuestion(%q) = %v, want %v", question, got, want)
		}
	}
	t.Logf("reply misroutes: %d/%d", misroutes, len(structured)+len(freeform)+len(frames))
}

func TestSenderPrefixDoesNotTruncateAccents(t *testing.T) {
	if intent, text := parseIntent("Expéditeur : Élodie"); intent != IntentSender || text != "Élodie" {
		t.Fatalf("accented sender: %q %q", intent, text)
	}
	if intent, text := parseIntent("Écris à Bob"); intent != IntentFreeform || text != "" {
		t.Fatalf("a non-prefix stays freeform: %q %q", intent, text)
	}
}
