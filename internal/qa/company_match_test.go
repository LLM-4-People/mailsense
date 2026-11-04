package qa

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// The live book: "services ex" must reach the Services E.X. / Exemple family
// through the acronym and the domain, and must not reach Delta Systèmes through the
// generic "services" subdomain or the unrelated Gestion company.
func TestCompanyMatchAcronymAbbreviationAndGenericSubdomain(t *testing.T) {
	companies := []worldsvc.CompanyRow{
		{Key: "c:services exemple", Label: "Services Exemple", Domains: []string{"services-exemple.test"}, Messages: 989},
		{Key: "c:info", Label: "Info - Services E.X.", Domains: []string{"services-exemple.test"}, Messages: 14},
		{Key: "c:ulysse assurance", Label: "Ulysse Assurance", Domains: []string{"ulysse-assurance.test"}, Messages: 11},
		{Key: "c:ovh", Label: "Delta Systèmes", Domains: []string{"services.delta-systemes.test"}, Messages: 9},
	}
	question := "donne-moi le courriel pour la création de nouveaux employés de simon de services ex"
	matches := matchCompanies(question, companies)
	byKey := map[string]int{}
	for _, match := range matches {
		byKey[match.Org.Key] = match.Evidence
	}
	if _, ok := byKey["c:ovh"]; ok {
		t.Fatalf("the generic services subdomain selected Delta Systèmes: %+v", matches)
	}
	if _, ok := byKey["c:ulysse assurance"]; ok {
		t.Fatalf("an unrelated Gestion company matched: %+v", matches)
	}
	if byKey["c:services exemple"] == 0 && byKey["c:info"] == 0 {
		t.Fatalf("the Services E.X. family did not match: %+v", matches)
	}
	if byKey["c:info"] < byKey["c:services exemple"] {
		t.Fatalf("the exact E.X. acronym should outrank the Exemple abbreviation: %+v", matches)
	}
}

// The domain base is the form people write in prose, and the live question
// spells clinique-exemple.test as two adjacent words ("clinique exemple"). The domain-base
// loop must accept that compact phrase exactly like the label run does; a
// longer word that merely starts with the base is not a mention.
func TestCompanyMatchDomainBaseCompactPhrase(t *testing.T) {
	companies := []worldsvc.CompanyRow{
		{Key: "d:clinique-exemple.test", Label: "clinique-exemple.test", Domains: []string{"clinique-exemple.test"}},
	}
	question := "résume moi les 5 dernières demandes de clinique exemple"
	matches := matchCompanies(question, companies)
	if len(matches) != 1 || matches[0].Org.Key != "d:clinique-exemple.test" {
		t.Fatalf("%q selected %+v, want d:clinique-exemple.test", question, matches)
	}
	if matches[0].Evidence < 40 {
		t.Fatalf("%q evidence %d, want the domain-base score (>=40)", question, matches[0].Evidence)
	}
	for _, question := range []string{"What about cliniqueexempleen?", "The subcliniqueexemple project"} {
		if matches := matchCompanies(question, companies); len(matches) != 0 {
			t.Errorf("%q matched inside a longer word: %+v", question, matches)
		}
	}
}

// A company contact answer lists the shared mailboxes on its domain, and the
// question's subject picks the desk: employees -> rh@, invoices -> billing.
func TestCompanyContactsFollowTheQuestionIntent(t *testing.T) {
	companies := []resolvedEntity{{Kind: entityCompany, Label: "Services E.X.",
		Domains: []string{"services-exemple.test"}}}
	people := []resolvedEntity{
		{Kind: entityPerson, Label: "Info - Services E.X.", Addresses: []string{"info@services-exemple.test"}, Messages: 4},
		{Kind: entityPerson, Label: "Ressource humaine Services E.X.", Addresses: []string{"rh@services-exemple.test"}, Messages: 9},
		{Kind: entityPerson, Label: "Simon Berthier", Addresses: []string{"sberthier@services-exemple.test"}, Messages: 379},
	}
	employees := withCompanyContacts(append([]resolvedEntity{}, companies...), people,
		"le courriel pour la création de nouveaux employés")
	if len(employees[0].Contacts) == 0 || employees[0].Contacts[0] != "rh@services-exemple.test" {
		t.Fatalf("employee contacts %+v, want rh@ first", employees[0].Contacts)
	}
	if strings.Contains(strings.Join(employees[0].Contacts, " "), "sberthier@") {
		t.Fatalf("a person's address leaked into the company mailboxes: %+v", employees[0].Contacts)
	}
	invoices := withCompanyContacts(append([]resolvedEntity{}, companies...), people,
		"la facture du mois")
	if len(invoices[0].Contacts) == 0 || invoices[0].Contacts[0] != "info@services-exemple.test" {
		t.Fatalf("invoice contacts %+v, want the general desk when no billing desk exists", invoices[0].Contacts)
	}
}

// The full path with no person at the company: the answer is the company's
// domain and its employee mailbox, not a clarification.
func TestCompanyContactQuestionAnswersTheMailbox(t *testing.T) {
	w := newWorld(t, false)
	w.svc.SetCompanies(&fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{
			{Key: "c:services exemple", Label: "Services Exemple", Domains: []string{"services-exemple.test"}, Messages: 989},
			{Key: "c:info", Label: "Info - Services E.X.", Domains: []string{"services-exemple.test"}, Messages: 14},
		}},
		profiles: map[string]worldsvc.ProfileView{},
	})
	w.store.people = []store.PersonCandidate{
		{Address: "info@services-exemple.test", Name: "Info - Services E.X.", Messages: 4},
		{Address: "rh@services-exemple.test", Name: "Ressource humaine Services E.X.", Messages: 9},
	}
	view, err := w.svc.Ask(t.Context(),
		"donne-moi le courriel pour la création de nouveaux employés de services ex", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Intent != "contact" ||
		!strings.Contains(view.Answer, "rh@services-exemple.test") {
		t.Fatalf("answer = %+v, want the employee mailbox", view)
	}
	if view.Clarify != nil {
		t.Fatalf("an obvious company must not ask: %+v", view.Clarify)
	}
}
