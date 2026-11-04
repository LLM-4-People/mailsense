package qa

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/store"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

func summaryThread(ids ...int64) store.ThreadMessagesResult {
	page := store.ThreadMessagesResult{ConversationID: "C-summary", Messages: []store.ThreadMessage{}}
	for _, id := range ids {
		page.Messages = append(page.Messages, store.ThreadMessage{
			ID: id, ConversationID: "C-summary", Subject: "Facture Nadia", SenderName: "Nadia Tremblay",
			SenderAddress: "nadia.tremblay@atelier-nord.test", ReceivedAt: "2026-10-25T10:00:00Z",
		})
	}
	return page
}

func summaryBodies(w *testWorld, ids ...int64) {
	for _, id := range ids {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: "Facture Nadia", SenderName: "Nadia Tremblay",
			SenderAddress: "nadia.tremblay@atelier-nord.test", Instant: "2026-10-25T10:00:00Z",
			Role: mail.RoleInbox, Body: "Le corps du message " + string(rune('0'+id%10))}
	}
}

// The live case: "résumer le dernier échange avec Nadia" must not become a
// 902-message list. Two Nadias are an explicit pick; one Nadia summarizes her
// newest conversation through the generator.
func TestPluralEntityQuestionKeepsTheWholeSet(t *testing.T) {
	cases := []struct {
		question string
		plural   bool
		listing  bool
	}{
		{"donne-moi la liste de clients qui ont déjà fait mention de l'IA, n'importe laquelle dans n'importe quel contexte", true, true},
		{"who are the clients who mentioned AI?", true, true},
		{"quels fournisseurs ont parlé de l'IA ?", true, true},
		{"Peux-tu me résumer le dernier échange avec Nadia ?", false, false},
		{"what did the clients say about the merger?", true, false},
		{"liste les messages de Nadia", false, false},
		{"Combien de clients ont mentionné l'IA ?", true, true},
		{"Combien de messages ai-je envoyés ?", false, false},
	}
	for _, tc := range cases {
		if got := pluralEntityQuestion(tc.question); got != tc.plural {
			t.Errorf("pluralEntityQuestion(%q) = %v, want %v", tc.question, got, tc.plural)
		}
		if got := listingEntityQuestion(tc.question); got != tc.listing {
			t.Errorf("listingEntityQuestion(%q) = %v, want %v", tc.question, got, tc.listing)
		}
	}
}

func TestEntityFollowUpKeepsAddressWithinStoreBound(t *testing.T) {
	option := entityFollowUp(strings.Repeat("é", 500), "Nadia Tremblay <nadia.tremblay@atelier-nord.test>")
	if option == "" || len(option) > 300 {
		t.Fatalf("option length %d: %q", len(option), option)
	}
	if !utf8.ValidString(option) || !strings.Contains(option, "nadia.tremblay@atelier-nord.test") {
		t.Fatalf("option lost its exact identifier or UTF-8: %q", option)
	}
}

func TestEntityAmbiguityClarifiesAndPickedPersonSummarizes(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{
		{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 10,
			LastAt: "2026-10-25T10:00:00Z"},
		{Address: "nadia.smith@beta.test", Name: "Nadia Smith", Messages: 3,
			LastAt: "2026-10-20T10:00:00Z"},
	}
	w.store.latestThread = "C-summary"
	w.store.threads = map[string]store.ThreadMessagesResult{"C-summary": summaryThread(201, 202)}
	summaryBodies(w, 201, 202)
	w.gen.content = "Nadia a demandé la facture [1]. La décision reste ouverte [2]."
	question := "Peux-tu me résumer le dernier échange avec Nadia ?"

	first, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeClarify || first.Clarify == nil || len(first.Clarify.Options) != 2 {
		t.Fatalf("two Nadias must ask before answering: %+v", first)
	}
	if first.Clarify.Question != "De quelle personne parlez-vous ?" {
		t.Fatalf("clarify question %q", first.Clarify.Question)
	}
	if !strings.Contains(first.Clarify.Options[0], "nadia.tremblay@atelier-nord.test") ||
		!strings.Contains(first.Clarify.Options[1], "nadia.smith@beta.test") {
		t.Fatalf("clarify options must carry exact addresses: %+v", first.Clarify.Options)
	}
	if len(first.Clarify.Candidates) != 2 {
		t.Fatalf("clarify candidates %+v, want two structured rows", first.Clarify.Candidates)
	}
	for i, candidate := range first.Clarify.Candidates {
		if candidate.Kind != "person" || candidate.Address == "" || candidate.Messages <= 0 ||
			candidate.Question != first.Clarify.Options[i] {
			t.Fatalf("candidate %d %+v does not match option %q", i, candidate, first.Clarify.Options[i])
		}
	}
	if w.gen.calls != 0 || w.store.queryCalls != 0 {
		t.Fatalf("ambiguous resolution called the planner: generator=%d query=%d", w.gen.calls, w.store.queryCalls)
	}
	if len(w.store.plans) != 0 {
		t.Fatalf("ambiguous resolution cached a plan: %+v", w.store.plans)
	}

	picked, err := w.svc.Ask(t.Context(), first.Clarify.Options[0], "fr")
	if err != nil {
		t.Fatal(err)
	}
	if picked.Mode != ModeGenerated || picked.Clarify != nil {
		t.Fatalf("clicked candidate did not summarize: %+v", picked)
	}
	if w.gen.calls != 1 {
		t.Fatalf("summary generator calls %d", w.gen.calls)
	}
	if len(w.store.latestAddrs) != 1 || w.store.latestAddrs[0] != "nadia.tremblay@atelier-nord.test" {
		t.Fatalf("picked summary scoped to %+v", w.store.latestAddrs)
	}
	if len(picked.Citations) != 2 || picked.Citations[0].MessageID != 201 || picked.Citations[1].MessageID != 202 {
		t.Fatalf("summary citations %+v", picked.Citations)
	}
	if !strings.Contains(w.gen.system, "Never list message subjects") ||
		!strings.Contains(w.gen.user, "Conversation (numbered") ||
		!strings.Contains(w.gen.user, "Resolved entity") {
		t.Fatalf("summary prompt missing contract: system=%q user=%q", w.gen.system, w.gen.user)
	}
	if picked.Plan != nil || picked.Result != nil {
		t.Fatalf("a summary must not carry a computed plan: %+v", picked)
	}
	if !w.gen.prose {
		t.Fatal("a conversation summary must ask for prose, not the planner's JSON mode")
	}
}

func TestEntityUniquePersonSummaryGenerates(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 3}}
	w.store.latestThread = "C-summary"
	w.store.threads = map[string]store.ThreadMessagesResult{"C-summary": summaryThread(501, 502)}
	summaryBodies(w, 501, 502)
	w.gen.content = "Nadia a envoyé la facture [1]. La décision est ouverte [2]."

	view, err := w.svc.Ask(t.Context(), "Peux-tu me résumer le dernier échange avec Nadia ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Answer == "" || len(view.Citations) != 2 {
		t.Fatalf("unique Nadia summary %+v", view)
	}
	if view.Citations[0].MessageID != 501 || view.Citations[1].MessageID != 502 {
		t.Fatalf("summary citations %+v", view.Citations)
	}
	if w.gen.calls != 1 || strings.Contains(w.gen.user, "902") {
		t.Fatalf("summary call: calls=%d user=%q", w.gen.calls, w.gen.user)
	}
	if !w.gen.prose {
		t.Fatal("a conversation summary must ask for prose, not the planner's JSON mode")
	}
	if view.Notice != "" {
		t.Fatalf("a French summary gained a false notice %q", view.Notice)
	}
}

func TestEntitySummaryLanguageNotice(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 3}}
	w.store.latestThread = "C-summary"
	w.store.threads = map[string]store.ThreadMessagesResult{"C-summary": summaryThread(601, 602)}
	summaryBodies(w, 601, 602)
	const englishSummary = "Nadia sent the invoice and the decision remains open [1]. A reply is expected next week [2]."
	w.gen.content = englishSummary
	question := "Peux-tu me résumer le dernier échange avec Nadia ?"

	first, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeGenerated || first.Notice != NoticeAnswerLanguage || first.Answer != englishSummary {
		t.Fatalf("English summary for a French request: %+v", first)
	}
	if len(first.Citations) != 2 || first.Citations[0].MessageID != 601 || first.Citations[1].MessageID != 602 {
		t.Fatalf("the summary citations changed: %+v", first.Citations)
	}
	if len(w.store.saved) != 1 || w.store.saved[0].Notice != NoticeAnswerLanguage {
		t.Fatalf("the noticed summary was not saved to history: %+v", w.store.saved)
	}

	second, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if second.Notice != NoticeAnswerLanguage {
		t.Fatalf("second summary notice = %q", second.Notice)
	}
	if w.gen.calls != 2 {
		t.Fatalf("a noticed summary was cached: generator calls = %d, want 2", w.gen.calls)
	}
	if len(w.store.saved) != 2 {
		t.Fatalf("history rows = %d, want 2", len(w.store.saved))
	}
	t.Logf("wrong-language summary generator calls after repeat: %d", w.gen.calls)
}

func TestEntityResolverGroupsOnePersonsAddresses(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{
		{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 4},
		{Address: "n.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 2},
	}
	entities := w.svc.resolveQuestionEntities(t.Context(), "Peux-tu me résumer le dernier échange avec Nadia ?")
	if len(entities) != 1 || entities[0].Kind != entityPerson || len(entities[0].Addresses) != 2 {
		t.Fatalf("one person with two addresses: %+v", entities)
	}
	if entities[0].Addresses[0] != "n.tremblay@atelier-nord.test" || entities[0].Addresses[1] != "nadia.tremblay@atelier-nord.test" {
		t.Fatalf("addresses are not exact and sorted: %+v", entities[0].Addresses)
	}
	if w.gen.calls != 0 || w.store.queryCalls != 0 {
		t.Fatal("entity resolution made a provider call or executed a plan")
	}
}

func TestEntityResolverKeepsSameNameOnDifferentDomainsAmbiguous(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{
		{Address: "nadia@atelier-nord.test", Name: "Nadia Tremblay", Messages: 4},
		{Address: "nadia@beta.test", Name: "Nadia Tremblay", Messages: 2},
	}
	entities := w.svc.resolveQuestionEntities(t.Context(), "Résume le dernier échange avec Nadia Tremblay")
	if len(entities) != 2 {
		t.Fatalf("same name on unrelated domains must stay two candidates: %+v", entities)
	}
	for _, entity := range entities {
		if len(entity.Addresses) != 1 {
			t.Fatalf("a silent merge happened: %+v", entities)
		}
	}
}

func TestEntitySummaryWithoutGeneratorKeepsThreadCitations(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 2}}
	w.store.latestThread = "C-summary"
	w.store.threads = map[string]store.ThreadMessagesResult{"C-summary": summaryThread(401, 402)}
	summaryBodies(w, 401, 402)
	w.svc.source = nil

	view, err := w.svc.Ask(t.Context(), "Peux-tu me résumer le dernier échange avec Nadia ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeUnavailable {
		t.Fatalf("missing generator summary %+v", view)
	}
	if len(view.Citations) != 2 || view.Citations[0].MessageID != 401 || view.Citations[1].MessageID != 402 {
		t.Fatalf("specific-thread citations %+v", view.Citations)
	}
}

func TestEntitySummaryNoConversationIsNoMatch(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 1}}
	view, err := w.svc.Ask(t.Context(), "Peux-tu me résumer le dernier échange avec Nadia ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoMatches || len(view.Citations) != 0 {
		t.Fatalf("a person without a conversation is no-match: %+v", view)
	}
	if w.gen.calls != 0 {
		t.Fatalf("no-thread summary called the generator %d times", w.gen.calls)
	}
}

func TestEntityCompanyAmbiguityPicksByDomainAndSummarizes(t *testing.T) {
	w := newWorld(t, false)
	companies := &fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{
			{Key: "c:clinique-a", Label: "Clinique Exemple", Domains: []string{"clinique-exemple-a.test"},
				Messages: 9, LastAt: "2026-10-25T10:00:00Z"},
			{Key: "c:clinique-b", Label: "Clinique Exemple", Domains: []string{"clinique-exemple-b.test"},
				Messages: 4, LastAt: "2026-10-20T10:00:00Z"},
		}},
		profiles: map[string]worldsvc.ProfileView{
			"c:clinique-a": {Key: "c:clinique-a", Label: "Clinique Exemple", Domains: []string{"clinique-exemple-a.test"},
				Recent: []worldsvc.CompanyMessage{{MessageID: 301, ConversationID: "C-iso-a"}}},
			"c:clinique-b": {Key: "c:clinique-b", Label: "Clinique Exemple", Domains: []string{"clinique-exemple-b.test"},
				Recent: []worldsvc.CompanyMessage{{MessageID: 302, ConversationID: "C-iso-b"}}},
		},
	}
	w.svc.SetCompanies(companies)
	w.store.threads = map[string]store.ThreadMessagesResult{"C-iso-b": {ConversationID: "C-iso-b",
		Messages: []store.ThreadMessage{{ID: 302, ConversationID: "C-iso-b", Subject: "Iso B",
			SenderName: "Client B", SenderAddress: "client@clinique-exemple-b.test", ReceivedAt: "2026-10-20T10:00:00Z"}}}}
	w.store.summaries[302] = store.Summary{RowID: 302, Subject: "Iso B", SenderName: "Client B",
		SenderAddress: "client@clinique-exemple-b.test", Instant: "2026-10-20T10:00:00Z", Role: mail.RoleInbox,
		Body: "Le client B a confirmé la commande."}
	w.gen.content = "Iso B a confirmé la commande [1]."
	question := "Peux-tu me résumer le dernier échange avec Clinique Exemple ?"

	first, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeClarify || first.Clarify == nil || len(first.Clarify.Options) != 2 {
		t.Fatalf("two companies must ask before answering: %+v", first)
	}
	if first.Clarify.Question != "De quel client parlez-vous ?" {
		t.Fatalf("company clarify question %q", first.Clarify.Question)
	}
	if !strings.Contains(first.Clarify.Options[0], "clinique-exemple-a.test") ||
		!strings.Contains(first.Clarify.Options[1], "clinique-exemple-b.test") {
		t.Fatalf("company options must carry domains: %+v", first.Clarify.Options)
	}
	if w.gen.calls != 0 {
		t.Fatalf("ambiguous companies called the generator %d times", w.gen.calls)
	}

	picked, err := w.svc.Ask(t.Context(), first.Clarify.Options[1], "fr")
	if err != nil {
		t.Fatal(err)
	}
	if picked.Mode != ModeGenerated || picked.Clarify != nil {
		t.Fatalf("picked company did not summarize: %+v", picked)
	}
	if companies.lastKey != "c:clinique-b" {
		t.Fatalf("picked summary read profile %q", companies.lastKey)
	}
	if len(picked.Citations) != 1 || picked.Citations[0].MessageID != 302 {
		t.Fatalf("company summary citations %+v", picked.Citations)
	}
}

// The resolver's company pool reads the same linker as the company shortcut.
// The live domain organization is stored under d:clinique-exemple.test and the
// question spells the base as two adjacent words, so the organization must
// reach the entity layer with its domain-base evidence instead of only the
// label-run score.
func TestEntityCompanyPoolLinksSpacedDomainBase(t *testing.T) {
	book := worldsvc.ListResult{Companies: []worldsvc.CompanyRow{
		{Key: "d:clinique-exemple.test", Label: "clinique-exemple.test", Domains: []string{"clinique-exemple.test"}},
	}}
	question := "résume moi les 5 dernières demandes de clinique exemple"
	candidates := companyEntityCandidates(book, question)
	if len(candidates) != 1 || candidates[0].Kind != entityCompany ||
		candidates[0].Key != "d:clinique-exemple.test" {
		t.Fatalf("company candidates %+v, want the domain organization", candidates)
	}
	if candidates[0].Evidence < 40 {
		t.Fatalf("company evidence %d, want the domain-base score (>=40)", candidates[0].Evidence)
	}
}

// isoCompanyPeople is the live organization's person pool: five senders on
// clinique-exemple.test, only one of which carries a label term outside the company
// name (CSMH).
func isoCompanyPeople() []store.PersonCandidate {
	return []store.PersonCandidate{
		{Address: "csmh@clinique-exemple.test", Name: "Clinique Exemple - CSMH", Messages: 12, LastAt: "2026-10-28T10:00:00Z"},
		{Address: "info@clinique-exemple.test", Name: "CLINIQUE EXEMPLE", Messages: 9, LastAt: "2026-10-27T10:00:00Z"},
		{Address: "readapt@clinique-exemple.test", Name: "Clinique Exemple Réadapt", Messages: 7, LastAt: "2026-10-26T10:00:00Z"},
		{Address: "stelie@clinique-exemple.test", Name: "Clinique Exemple - St-Élie", Messages: 5, LastAt: "2026-10-25T10:00:00Z"},
		{Address: "physio@clinique-exemple.test", Name: "Clinique Exemple - Physiothérapie", Messages: 4, LastAt: "2026-10-24T10:00:00Z"},
	}
}

func isoCompanyBook() worldsvc.ListResult {
	return worldsvc.ListResult{Companies: []worldsvc.CompanyRow{
		{Key: "d:clinique-exemple.test", Label: "Clinique Exemple", Domains: []string{"clinique-exemple.test"},
			Messages: 37, LastAt: "2026-10-28T10:00:00Z"},
	}}
}

// A question that names only the organization selects it, even when every
// sender on its domain is a person candidate.
func TestCompanyOnlyQuestionSelectsTheOrganization(t *testing.T) {
	question := "résume moi les 5 dernières demandes de clinique exemple"
	mentions := entityMention(question)
	companies := companyEntityCandidates(isoCompanyBook(), question)
	persons := personEntityCandidates(isoCompanyPeople(), mentions)
	selected := selectEntityCandidates(persons, companies, mentions, question)
	if len(selected) != 1 || selected[0].Kind != entityCompany || selected[0].Label != "Clinique Exemple" {
		t.Fatalf("selected %+v, want the organization", selected)
	}

	w := newWorld(t, false)
	w.svc.SetCompanies(&fakeCompanies{list: isoCompanyBook(), profiles: map[string]worldsvc.ProfileView{}})
	w.store.people = isoCompanyPeople()
	planFallback(w)
	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeClarify || view.Clarify != nil {
		t.Fatalf("company-only question asked for a person: %+v", view)
	}
	if strings.Contains(view.Answer, "De quelle personne parlez-vous") {
		t.Fatalf("company-only question asked for a person: %q", view.Answer)
	}
}

// A term that belongs to one person candidate and not to the company keeps
// the person-at-company route: CSMH is a distinct label term, Simon is a
// distinct person name.
func TestPersonTermAtNamedCompanyKeepsThePerson(t *testing.T) {
	t.Run("distinguishing label term selects CSMH", func(t *testing.T) {
		question := "résume les demandes de Clinique Exemple CSMH"
		mentions := entityMention(question)
		selected := selectEntityCandidates(
			personEntityCandidates(isoCompanyPeople(), mentions),
			companyEntityCandidates(isoCompanyBook(), question), mentions, question)
		if len(selected) != 1 || selected[0].Kind != entityPerson || selected[0].Label != "Clinique Exemple - CSMH" {
			t.Fatalf("selected %+v, want the CSMH person", selected)
		}
	})
	t.Run("named person selects Simon", func(t *testing.T) {
		question := "le dernier message de Simon de Services Exemple"
		persons := []resolvedEntity{
			{Kind: entityPerson, Label: "Simon Berthier", Addresses: []string{"sberthier@services-exemple.test"},
				Evidence: 1, Messages: 379},
			{Kind: entityPerson, Label: "Simon Viau", Addresses: []string{"simon.viau@acme.test"},
				Evidence: 1, Messages: 23},
			{Kind: entityPerson, Label: "Info - Services E.X.", Addresses: []string{"info@services-exemple.test"},
				Evidence: 1, Messages: 4},
		}
		companies := []resolvedEntity{{Kind: entityCompany, Label: "Services Exemple",
			Domains: []string{"services-exemple.test"}, Evidence: 23}}
		selected := selectEntityCandidates(persons, companies, entityMention(question), question)
		if len(selected) != 1 || selected[0].Label != "Simon Berthier" {
			t.Fatalf("selected %+v, want Simon Berthier", selected)
		}
	})
}

func TestPersonTermOutsideCompany(t *testing.T) {
	question := "résume moi les 5 dernières demandes de clinique exemple"
	mentions := entityMention(question)
	persons := personEntityCandidates(isoCompanyPeople(), mentions)
	companies := companyEntityCandidates(isoCompanyBook(), question)
	addressOnly := []resolvedEntity{{Kind: entityPerson, Label: "CLINIQUE EXEMPLE",
		Addresses: []string{"csmh@clinique-exemple.test"}}}
	unkeyed := []resolvedEntity{{Kind: entityCompany, Label: "Clinique Exemple",
		Domains: []string{"clinique-exemple.test"}}}
	cases := []struct {
		name      string
		persons   []resolvedEntity
		companies []resolvedEntity
		terms     []string
		want      bool
	}{
		{"company name only", persons, companies, mentions.terms, false},
		{"request nouns only", persons, companies, []string{"dernieres", "demandes"}, false},
		{"person label term", persons, companies, []string{"csmh"}, true},
		{"person address local part", addressOnly, companies, []string{"csmh"}, true},
		{"unrelated request noun", persons, companies, []string{"facture"}, false},
		{"unkeyed company fallback", persons, unkeyed, mentions.terms, false},
		{"unkeyed company still sees person term", persons, unkeyed, []string{"csmh"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personTermOutsideCompany(tc.persons, tc.companies, tc.terms); got != tc.want {
				t.Fatalf("personTermOutsideCompany(%v) = %v, want %v", tc.terms, got, tc.want)
			}
		})
	}
}

func TestResolvedPersonPlanUsesExactAddressesAndCachesThem(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 4}}
	planFallback(w)
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","contact":"Nadia","limit":5,"order":"desc"}}`
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Total: 1, Rows: []mailquery.Row{{MessageID: 201, Subject: "Facture", SenderName: "Nadia Tremblay",
			Instant: "2026-10-25T10:00:00Z"}}}

	view, err := w.svc.Ask(t.Context(), "Combien de messages avec Nadia ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed {
		t.Fatalf("mode %q: %+v", view.Mode, view)
	}
	if view.Plan == nil || view.Plan.Contact != "" || len(view.Plan.ContactAddresses) != 1 ||
		view.Plan.ContactAddresses[0] != "nadia.tremblay@atelier-nord.test" {
		t.Fatalf("plan was not rewritten to exact addresses: %+v", view.Plan)
	}
	if w.store.lastPlan.Contact != "" || len(w.store.lastPlan.ContactAddresses) != 1 {
		t.Fatalf("executor received the substring plan: %+v", w.store.lastPlan)
	}
	for _, cached := range w.store.plans {
		if len(cached.ContactAddresses) != 1 || cached.ContactAddresses[0] != "nadia.tremblay@atelier-nord.test" {
			t.Fatalf("plan cache did not persist the resolved addresses: %+v", cached)
		}
	}
}

func TestSummaryPlanFromPlannerIsRejected(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","contact":"Nadia","limit":5,"order":"desc"}}`
	view, err := w.svc.Ask(t.Context(), "Peux-tu me résumer le dernier échange avec Nadia ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeComputed || (view.Plan != nil && view.Plan.Kind == mailquery.KindMessages) {
		t.Fatalf("a summary request executed a message plan: %+v", view)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoGroundedAnswer || len(view.Citations) == 0 {
		t.Fatalf("summary plan fallback %+v", view)
	}
	if w.store.queryCalls != 0 {
		t.Fatalf("rejected summary plan reached the executor %d times", w.store.queryCalls)
	}
}

func TestResolvedEntityPromptRules(t *testing.T) {
	entity := resolvedEntity{Kind: entityPerson, Label: "Nadia Tremblay",
		Addresses: []string{"nadia.tremblay@atelier-nord.test"}, Company: "Atelier Nord"}
	system, user := plannerPrompt("Résume le dernier échange avec Nadia", "fr", nil, nil, nil, nil,
		store.OwnerIdentity{}, &entity, nil, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), "UTC")
	for _, want := range []string{"contact_addresses", "never the first-name substring",
		"never a \"messages\" plan", "one complete option per candidate",
		"topic keywords", "never return a clarify for a plural request",
		"clients who mentioned ai"} {
		if !strings.Contains(strings.ToLower(system), strings.ToLower(want)) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	if !strings.Contains(user, "Nadia Tremblay <nadia.tremblay@atelier-nord.test>") ||
		!strings.Contains(user, "company: Atelier Nord") {
		t.Fatalf("resolved entity block missing:\n%s", user)
	}
}

// The live failure: "simon de services ex" names a person and a company.
// The person at that company must win without asking which Simon.
func TestPersonAtNamedCompanyOutranksNamesakes(t *testing.T) {
	question := "donne-moi le courriel pour la création de nouveaux employés de simon de services ex"
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Simon Viau", Addresses: []string{"simon.viau@acme.test"}, Evidence: 1, Messages: 23},
		{Kind: entityPerson, Label: "nslone Rapidenet", Addresses: []string{"simon@mistral-transport.test"}, Evidence: 1, Messages: 28},
		{Kind: entityPerson, Label: "Simon Exemple", Addresses: []string{"simon@services-exemple.test"},
			Company: "Services E.X.", Evidence: 1, Messages: 3},
	}
	companies := []resolvedEntity{
		{Kind: entityCompany, Label: "Services Exemple", Domains: []string{"services-exemple.test"}, Evidence: 1, Messages: 989},
		{Kind: entityCompany, Label: "Info - Services E.X.", Domains: []string{"services-exemple.test"}, Evidence: 1, Messages: 14},
		{Kind: entityCompany, Label: "Ulysse Assurance", Domains: []string{"ulysse-assurance.test"}, Evidence: 1, Messages: 11},
		{Kind: entityCompany, Label: "Delta Systèmes", Domains: []string{"services.delta-systemes.test"}, Evidence: 1, Messages: 9},
	}
	selected := selectEntityCandidates(persons, companies, entityMention(question), question)
	if len(selected) != 1 || !strings.Contains(selected[0].Addresses[0], "simon@services-exemple.test") {
		t.Fatalf("selected %+v, want the Simon at Services E.X. (terms %v)", selected, entityMention(question).terms)
	}
}

// The live question end to end: "le courriel ... de simon de services ex"
// must answer the Simon at Services E.X., not ask which of six contacts.
func TestContactQuestionAnswersThePersonAtTheNamedCompany(t *testing.T) {
	w := newWorld(t, false)
	w.svc.SetCompanies(&fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{
			{Key: "c:services exemple", Label: "Services Exemple", Domains: []string{"services-exemple.test"},
				Messages: 989, LastAt: "2026-10-28T10:00:00Z"},
			{Key: "c:info-services exemple", Label: "Info - Services E.X.", Domains: []string{"services-exemple.test"},
				Messages: 14, LastAt: "2025-10-01T13:00:00Z"},
			{Key: "c:ulysse assurance", Label: "Ulysse Assurance", Domains: []string{"ulysse-assurance.test"},
				Messages: 11, LastAt: "2026-06-15T13:00:00Z"},
			{Key: "c:ovh", Label: "Delta Systèmes", Domains: []string{"services.delta-systemes.test"},
				Messages: 9, LastAt: "2026-10-16T14:00:00Z"},
		}},
		profiles: map[string]worldsvc.ProfileView{},
	})
	w.store.people = []store.PersonCandidate{
		{Address: "sberthier@services-exemple.test", Name: "Simon Berthier", Messages: 379,
			LastAt: "2026-10-28T10:00:00Z"},
		{Address: "simon@mistral-transport.test", Name: "nslone Rapidenet", Messages: 28,
			LastAt: "2026-04-20T21:00:00Z"},
		{Address: "simon.viau@acme.test", Name: "Simon Viau", Messages: 23,
			LastAt: "2023-07-28T12:00:00Z"},
	}
	view, err := w.svc.Ask(t.Context(),
		"donne-moi le courriel pour la création de nouveaux employés de simon de services ex", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Intent != "contact" ||
		!strings.Contains(view.Answer, "sberthier@services-exemple.test") {
		t.Fatalf("answer = %+v, want the Services E.X. Simon's address", view)
	}
	if view.Clarify != nil {
		t.Fatalf("an obvious person must not ask: %+v", view.Clarify)
	}
}

// A word shared by several organizations is a category, not an identity.
func TestGenericCompanyWordDoesNotSelectOrganizations(t *testing.T) {
	companies := []resolvedEntity{
		{Kind: entityCompany, Label: "Services Exemple", Domains: []string{"services-exemple.test"}, Evidence: 1},
		{Kind: entityCompany, Label: "Info - Services E.X.", Domains: []string{"services-exemple.test"}, Evidence: 1},
		{Kind: entityCompany, Label: "Services Nord", Domains: []string{"services-nord.test"}, Evidence: 1},
	}
	question := "services"
	if selected := selectEntityCandidates(nil, companies, entityMention(question), question); len(selected) != 0 {
		t.Fatalf("generic word selected %+v", selected)
	}
}

func TestContactAnswerCarriesExactAddress(t *testing.T) {
	view := contactAnswer(AskView{}, resolvedEntity{Kind: entityPerson, Label: "Nadia Tremblay",
		Addresses: []string{"nadia.tremblay@atelier-nord.test"}, Company: "Atelier Nord", Messages: 3})
	if view.Mode != ModeComputed || view.Intent != "contact" || !strings.Contains(view.Answer, "nadia.tremblay@atelier-nord.test") {
		t.Fatalf("contact answer %+v", view)
	}
}

func TestCompanyCandidatesPreferCompanyWordAndKeepPersonWord(t *testing.T) {
	person := resolvedEntity{Kind: entityPerson, Label: "Nadia Tremblay",
		Addresses: []string{"nadia.tremblay@atelier-nord.test"}}
	company := resolvedEntity{Kind: entityCompany, Label: "Atelier Nord", Domains: []string{"atelier-nord.test"}}
	first := "Quel client Atelier Nord a écrit à Nadia ?"
	selected := selectEntityCandidates([]resolvedEntity{person}, []resolvedEntity{company},
		entityMention(first), first)
	if len(selected) != 1 || selected[0].Kind != entityCompany {
		t.Fatalf("client must prefer the company: %+v", selected)
	}
	second := "Résume le dernier échange avec Nadia et Atelier Nord"
	selected = selectEntityCandidates([]resolvedEntity{person}, []resolvedEntity{company},
		entityMention(second), second)
	if len(selected) != 1 || selected[0].Kind != entityPerson {
		t.Fatalf("avec must prefer the person: %+v", selected)
	}
}

// The live failures: an explicit address must resolve to its person even
// though that domain also names a company, and a bare domain must resolve to
// the company even though its people also match. A same first name on
// different domains stays a choice: neither identity is merged or guessed.
func TestSelectEntityCandidatesPrefersExplicitIdentity(t *testing.T) {
	nadia := resolvedEntity{Kind: entityPerson, Label: "Nadia Lapointe",
		Addresses: []string{"nadia@famille-exemple.test", "nlapointe@famille-exemple.test"},
		Company:   "Famille Exemple", Messages: 12, Evidence: 2}
	firme := resolvedEntity{Kind: entityCompany, Label: "Famille Exemple",
		Domains: []string{"famille-exemple.test"}, Key: "d:famille-exemple.test", Messages: 40, Evidence: 1}
	secondNadia := resolvedEntity{Kind: entityPerson, Label: "Nadia Bouchard",
		Addresses: []string{"nadia@messagerie-exemple.test"}, Company: "Messagerie Exemple", Messages: 40, Evidence: 2}
	mailbox := resolvedEntity{Kind: entityCompany, Label: "Messagerie Exemple",
		Domains: []string{"messagerie-exemple.test"}, Key: "d:messagerie-exemple.test", Messages: 300, Evidence: 1}
	dupontNadia := resolvedEntity{Kind: entityPerson, Label: "Nadia Tremblay",
		Addresses: []string{"nb@entreprises-dupont.test"}, Company: "Entreprises Dupont", Messages: 5, Evidence: 1}
	tieNadia := nadia
	tieNadia.Evidence = 1

	cases := []struct {
		name      string
		persons   []resolvedEntity
		companies []resolvedEntity
		question  string
		want      []string
	}{
		{
			name:    "explicit address selects its person, not the company of its domain",
			persons: []resolvedEntity{nadia}, companies: []resolvedEntity{firme},
			question: "Summarize the last exchange with nadia@famille-exemple.test",
			want:     []string{"person:Nadia Lapointe"},
		},
		{
			name:    "bare domain selects the company, not a person at that domain",
			persons: []resolvedEntity{secondNadia}, companies: []resolvedEntity{mailbox},
			question: "Summarize the last exchange with messagerie-exemple.test",
			want:     []string{"company:Messagerie Exemple"},
		},
		{
			name:     "same first name on different domains stays a choice",
			persons:  []resolvedEntity{tieNadia, dupontNadia},
			question: "Summarize the last exchange with Nadia",
			want:     []string{"person:Nadia Lapointe", "person:Nadia Tremblay"},
		},
		{
			name:    "company word still selects the company",
			persons: []resolvedEntity{nadia}, companies: []resolvedEntity{firme},
			question: "Quel client Famille Exemple a écrit à Nadia ?",
			want:     []string{"company:Famille Exemple"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected := selectEntityCandidates(tc.persons, tc.companies, entityMention(tc.question), tc.question)
			got := make([]string, 0, len(selected))
			for _, candidate := range selected {
				got = append(got, candidate.Kind+":"+candidate.Label)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("selected %v, want %v", got, tc.want)
			}
		})
	}
}

// An address is one mention, not an address plus its domain: the domain
// branch must not turn a person into a company/person choice.
func TestEntityMentionKeepsAnAddressOutOfDomains(t *testing.T) {
	mentions := entityMention("Summarize the last exchange with nadia@famille-exemple.test")
	if len(mentions.emails) != 1 || mentions.emails[0] != "nadia@famille-exemple.test" {
		t.Fatalf("emails %v", mentions.emails)
	}
	if len(mentions.domains) != 0 {
		t.Fatalf("an address became a domain: %v", mentions.domains)
	}
}

// TestSharedMailboxesAreNotPersonChoices: the question names a person and a
// company, but the first name is shared by several people and the company
// word is generic. The resolver must not turn the shared mailboxes at that
// company (info@, rh@) into "which person?" options beside the real person.
func TestSharedMailboxesAreNotPersonChoices(t *testing.T) {
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Simon Berthier", Addresses: []string{"sberthier@services-exemple.test"}, Evidence: 3},
		{Kind: entityPerson, Label: "Info - Services E.X.", Addresses: []string{"info@services-exemple.test"}, Evidence: 3},
		{Kind: entityPerson, Label: "Ressource humaine Services E.X.", Addresses: []string{"rh@services-exemple.test"}, Evidence: 3},
	}
	companies := []resolvedEntity{{Kind: entityCompany, Label: "Services Exemple", Domains: []string{"services-exemple.test"}, Evidence: 23}}
	mentions := entityMentions{terms: []string{"services", "simon"}}
	got := selectEntityCandidates(persons, companies, mentions, "simon de services ex")
	if len(got) != 1 || got[0].Label != "Simon Berthier" {
		t.Fatalf("selected %+v, want only Simon Berthier", got)
	}
}

// TestAllSharedMailboxesStayWhenNoPersonIsNamed: a question about a company's
// mailboxes keeps them all; the person filter never empties a real choice.
func TestAllSharedMailboxesStayWhenNoPersonIsNamed(t *testing.T) {
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Info - Services E.X.", Addresses: []string{"info@services-exemple.test"}, Evidence: 3},
		{Kind: entityPerson, Label: "Ressource humaine Services E.X.", Addresses: []string{"rh@services-exemple.test"}, Evidence: 3},
	}
	got := selectEntityCandidates(persons, nil, entityMentions{terms: []string{"services"}}, "les courriels de services ex")
	if len(got) != 2 {
		t.Fatalf("selected %+v, want both mailboxes", got)
	}
}

// TestMultiTokenPersonNameRequiresTheWholeName: "richard harvey" names one
// person, not every Richard and every Harvey. When nobody matches both tokens,
// the near-misses are clearly labelled partial matches, never a "which
// person?" choice between unrelated people.
func TestMultiTokenPersonNameRequiresTheWholeName(t *testing.T) {
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Olivier Robert", Addresses: []string{"orobert@ouest-immobilier.test"}, Messages: 417, Evidence: 1},
		{Kind: entityPerson, Label: "Richard Pelletier", Addresses: []string{"richard.pelletier@prisme-architecture.test"}, Messages: 76, Evidence: 1},
		{Kind: entityPerson, Label: "richard@mistral-transport.test", Addresses: []string{"richard@mistral-transport.test"}, Messages: 29, Evidence: 1},
		{Kind: entityPerson, Label: "David Harvey", Addresses: []string{"david.harvey@kappa-logiciel.test"}, Messages: 9, Evidence: 1},
		{Kind: entityPerson, Label: "Richard Gagnon", Addresses: []string{"rgagnon@controle-quartz.test"}, Messages: 6, Evidence: 1},
		{Kind: entityPerson, Label: "Jean-Guy Harvey", Addresses: []string{"jean-guy@climatisation-exemple.test"}, Messages: 6, Evidence: 1},
	}
	mentions := entityMentions{terms: []string{"richard", "harvey"}}
	got := selectEntityCandidates(persons, nil, mentions, "qui est richard harvey")
	if len(got) == 0 {
		t.Fatal("no near-miss offered")
	}
	if len(got) > maxPartialPersonChoices {
		t.Fatalf("offered %d near-misses, want at most %d", len(got), maxPartialPersonChoices)
	}
	for _, candidate := range got {
		if !candidate.Partial {
			t.Fatalf("%q is not marked partial", candidate.Label)
		}
	}
	// The rarer token (the surname) leads: the two Harveys come first.
	if got[0].Label != "David Harvey" && got[0].Label != "Jean-Guy Harvey" {
		t.Fatalf("near-miss list starts with %q", got[0].Label)
	}
}

// TestMultiTokenPersonNameKeepsTheFullMatch: a person who matches every name
// token is the answer, not a choice.
func TestMultiTokenPersonNameKeepsTheFullMatch(t *testing.T) {
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Richard Pelletier", Addresses: []string{"richard.pelletier@prisme-architecture.test"}, Messages: 76, Evidence: 2},
		{Kind: entityPerson, Label: "David Harvey", Addresses: []string{"david.harvey@kappa-logiciel.test"}, Messages: 9, Evidence: 1},
	}
	mentions := entityMentions{terms: []string{"richard", "pelletier"}}
	got := selectEntityCandidates(persons, nil, mentions, "qui est richard pelletier")
	if len(got) != 1 || got[0].Label != "Richard Pelletier" || got[0].Partial {
		t.Fatalf("selected %+v, want only Richard Pelletier", got)
	}
}

// TestMultiTokenPersonNameKeepsThePersonAtTheNamedCompany: "simon de
// services ex" names one person even though "services" is also a name token
// on a shared mailbox: the person at the named organization is the match.
func TestMultiTokenPersonNameKeepsThePersonAtTheNamedCompany(t *testing.T) {
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Simon Berthier", Addresses: []string{"sberthier@services-exemple.test"}, Messages: 721, Evidence: 1},
		{Kind: entityPerson, Label: "Ressource humaine Services E.X.", Addresses: []string{"rh@services-exemple.test"}, Messages: 9, Evidence: 1},
	}
	companies := []resolvedEntity{{Kind: entityCompany, Label: "Services Exemple", Domains: []string{"services-exemple.test"}, Evidence: 23}}
	mentions := entityMentions{terms: []string{"simon", "services"}}
	got := selectEntityCandidates(persons, companies, mentions, "simon de services ex")
	if len(got) != 1 || got[0].Label != "Simon Berthier" || got[0].Partial {
		t.Fatalf("selected %+v, want only Simon Berthier", got)
	}
}

// TestNameShapeGeneralizesToUnseenNames: the full-name rule is derived from
// the address book, not from a list of known questions. A mailbox of other
// names gets the same treatment: the shape learns that "marc" is a given
// name and "tremblay" a surname from the people themselves.
func TestNameShapeGeneralizesToUnseenNames(t *testing.T) {
	persons := []resolvedEntity{
		{Kind: entityPerson, Label: "Marc Bélanger", Addresses: []string{"marc@exemple.test"}, Messages: 40, Evidence: 1},
		{Kind: entityPerson, Label: "Sophie Tremblay", Addresses: []string{"sophie@exemple.test"}, Messages: 30, Evidence: 1},
		{Kind: entityPerson, Label: "Julie Tremblay", Addresses: []string{"julie@exemple.test"}, Messages: 20, Evidence: 1},
	}
	shape := newNameShape(persons)
	if !shape.fullName([]string{"marc", "tremblay"}) {
		t.Fatal("marc tremblay is not recognised as a full name")
	}
	if shape.fullName([]string{"tremblay"}) {
		t.Fatal("a single surname is recognised as a full name")
	}
	if shape.fullName([]string{"belanger", "tremblay"}) {
		t.Fatal("two surnames with no given name are recognised as a full name")
	}
	mentions := entityMentions{terms: []string{"marc", "tremblay"}}
	got := selectEntityCandidates(persons, nil, mentions, "qui est marc tremblay")
	if len(got) == 0 {
		t.Fatal("no near-miss offered")
	}
	if len(got) > maxPartialPersonChoices {
		t.Fatalf("offered %d near-misses, want at most %d", len(got), maxPartialPersonChoices)
	}
	for _, candidate := range got {
		if !candidate.Partial {
			t.Fatalf("%q is not marked partial", candidate.Label)
		}
	}
}

// TestWhoIsIsAnEntitySummary: "qui est richard pelletier" asks who the resolved
// person is, so the answer comes from his own conversation, not from a
// mailbox-wide search. A content question that happens to start with "qui"
// keeps the search.
func TestWhoIsIsAnEntitySummary(t *testing.T) {
	person := resolvedEntity{Kind: entityPerson, Label: "Richard Pelletier"}
	if !isEntitySummaryQuestion("qui est richard pelletier", person) {
		t.Fatal("who-is is not an entity summary")
	}
	if isEntitySummaryQuestion("qui a envoyé la facture de messagerie-exemple", person) {
		t.Fatal("a content question was treated as an entity summary")
	}
}
