package subject

import (
	"cmp"
	"slices"
	"strings"
)

// Phrase is one row of the topic recognition table: the catalogue path and
// the folded question wording that names it. EN and FR are flat lists of
// alias runs; a run is one word or several contiguous words ("status update",
// "mise a jour produit"). Aliases are question wording only, never display
// copy: the UI composes labels from the catalogue. Subtopic is empty when the
// row names the whole topic.
//
// The table deliberately excludes every priority-intent word (reply, urgent,
// deadline, security, decision, workload, priority and their EN/FR forms) and
// the pinned scope words (budget, clients, upcoming, active, left, quarter,
// week, semaine), plus the standalone generic words test, request,
// information and question. A count of "urgent invoices" or "budget invoices"
// therefore cannot be reduced to a topic total, and "support request" is a
// topic alias while a bare "request" is not.
type Phrase struct {
	Topic    string
	Subtopic string
	EN       []string
	FR       []string
}

// cataloguePhrases is the one recognition table, in catalogue order: each
// topic's whole-topic row first, then its subtopics in display order. Rows
// name only catalogue paths (the package test verifies every Subtopic against
// suboptions). Every alias is already folded: lowercase, accent-free, single
// spaces, word characters only (see mail.Fold).
func cataloguePhrases() []Phrase {
	return []Phrase{
		{Topic: "project_work", EN: []string{"project", "projects"}, FR: []string{"projet", "projets"}},
		{Topic: "project_work", Subtopic: "status_update",
			EN: []string{"status update", "status updates"}},
		{Topic: "project_work", Subtopic: "deliverable",
			EN: []string{"deliverable", "deliverables"}, FR: []string{"livrable", "livrables"}},

		{Topic: "support_issue",
			EN: []string{"support", "support request", "support requests", "bug", "bugs"},
			FR: []string{"assistance"}},
		{Topic: "support_issue", Subtopic: "incident", EN: []string{"incident", "incidents"}},
		{Topic: "support_issue", Subtopic: "troubleshooting",
			EN: []string{"troubleshooting"}, FR: []string{"depannage"}},
		{Topic: "support_issue", Subtopic: "fix",
			EN: []string{"fix", "fixes"}, FR: []string{"correction", "corrections"}},
		{Topic: "support_issue", Subtopic: "escalation",
			EN: []string{"escalation", "escalations"}, FR: []string{"escalade", "escalades"}},

		{Topic: "sales_inquiry", EN: []string{"sales"}, FR: []string{"vente", "ventes"}},
		{Topic: "sales_inquiry", Subtopic: "lead",
			EN: []string{"lead", "leads", "prospect", "prospects"}},
		{Topic: "sales_inquiry", Subtopic: "quote_pricing",
			EN: []string{"quote", "quotes", "pricing"}, FR: []string{"devis"}},
		{Topic: "sales_inquiry", Subtopic: "proposal",
			EN: []string{"proposal", "proposals"}, FR: []string{"proposition", "propositions"}},
		{Topic: "sales_inquiry", Subtopic: "demo",
			EN: []string{"demo", "demos"}, FR: []string{"demonstration", "demonstrations"}},

		{Topic: "billing_account", EN: []string{"billing"}, FR: []string{"facturation"}},
		{Topic: "billing_account", Subtopic: "invoice",
			EN: []string{"invoice", "invoices"}, FR: []string{"facture", "factures"}},
		{Topic: "billing_account", Subtopic: "payment",
			EN: []string{"payment", "payments"}, FR: []string{"paiement", "paiements"}},
		{Topic: "billing_account", Subtopic: "contract",
			EN: []string{"contract", "contracts"}, FR: []string{"contrat", "contrats"}},

		{Topic: "meeting_scheduling",
			EN: []string{"meeting", "meetings"}, FR: []string{"reunion", "reunions"}},
		{Topic: "meeting_scheduling", Subtopic: "invitation",
			EN: []string{"invitation", "invitations"}},
		{Topic: "meeting_scheduling", Subtopic: "availability",
			EN: []string{"availability"}, FR: []string{"disponibilite", "disponibilites"}},
		{Topic: "meeting_scheduling", Subtopic: "reschedule",
			EN: []string{"reschedule", "rescheduling"}},

		{Topic: "technical_integration",
			EN: []string{"integration", "integrations", "api", "apis"},
			FR: []string{"integration", "integrations"}},
		{Topic: "technical_integration", Subtopic: "setup", EN: []string{"setup", "setups"}},
		{Topic: "technical_integration", Subtopic: "access_credentials",
			EN: []string{"access credentials", "credentials", "credential"},
			FR: []string{"identifiant", "identifiants"}},
		{Topic: "technical_integration", Subtopic: "configuration",
			EN: []string{"configuration", "configurations"}},

		{Topic: "hiring",
			EN: []string{"hiring", "recruitment", "candidate", "candidates"},
			FR: []string{"recrutement", "candidat", "candidats"}},
		{Topic: "hiring", Subtopic: "application",
			EN: []string{"application", "applications"}, FR: []string{"candidature", "candidatures"}},
		{Topic: "hiring", Subtopic: "interview",
			EN: []string{"interview", "interviews"}, FR: []string{"entrevue", "entrevues"}},
		{Topic: "hiring", Subtopic: "offer",
			EN: []string{"offer", "offers"}, FR: []string{"offre", "offres"}},

		{Topic: "partnership",
			EN: []string{"partnership", "partnerships", "vendor", "vendors", "reseller", "resellers"},
			FR: []string{"partenariat", "partenariats", "fournisseur", "fournisseurs"}},
		{Topic: "partnership", Subtopic: "agreement", EN: []string{"agreement", "agreements"}},

		{Topic: "internal_ops",
			EN: []string{"internal operations", "internal ops", "admin", "hr"},
			FR: []string{"operations internes", "rh"}},
		{Topic: "internal_ops", Subtopic: "policy",
			EN: []string{"policy", "policies"}, FR: []string{"politique", "politiques"}},

		{Topic: "news_updates",
			EN: []string{"news", "update", "updates"},
			FR: []string{"actualite", "actualites", "mise a jour", "mises a jour"}},
		{Topic: "news_updates", Subtopic: "newsletter",
			EN: []string{"newsletter", "newsletters"}, FR: []string{"infolettre", "infolettres"}},
		{Topic: "news_updates", Subtopic: "product_update",
			EN: []string{"product update", "product updates"},
			FR: []string{"mise a jour produit", "mises a jour produit"}},
		{Topic: "news_updates", Subtopic: "announcement",
			EN: []string{"announcement", "announcements"}, FR: []string{"annonce", "annonces"}},
		{Topic: "news_updates", Subtopic: "digest", EN: []string{"digest", "digests"}},

		{Topic: "other", EN: []string{"other"}, FR: []string{"autre", "autres"}},
	}
}

// aliasEntry is one flattened alias run of the table.
type aliasEntry struct {
	phrase Phrase
	words  []string
}

// aliasIndex is the flattened table, longest run first, then alias text. The
// order lets Match break as soon as the remaining runs are shorter than the
// longest match it already holds.
var (
	phraseTable = cataloguePhrases()
	aliasIndex  = buildAliasIndex(phraseTable)
)

func buildAliasIndex(rows []Phrase) []aliasEntry {
	out := make([]aliasEntry, 0, len(rows)*4)
	seen := map[string]bool{}
	for _, row := range rows {
		for _, alias := range append(append([]string(nil), row.EN...), row.FR...) {
			words := strings.Fields(alias)
			if len(words) == 0 {
				continue
			}
			key := row.Topic + "/" + row.Subtopic + "\x00" + alias
			if seen[key] {
				// The same wording may name a path in both language lists
				// ("integration" is the French word too); it is one run.
				continue
			}
			seen[key] = true
			out = append(out, aliasEntry{phrase: row, words: words})
		}
	}
	slices.SortFunc(out, func(a, b aliasEntry) int {
		if c := cmp.Compare(len(b.words), len(a.words)); c != 0 {
			return c
		}
		return strings.Compare(strings.Join(a.words, " "), strings.Join(b.words, " "))
	})
	return out
}

// Phrases returns every recognition row, in catalogue order. It returns
// copies, so a caller cannot mutate the table the matcher reads.
func Phrases() []Phrase {
	out := make([]Phrase, len(phraseTable))
	for i, row := range phraseTable {
		out[i] = Phrase{Topic: row.Topic, Subtopic: row.Subtopic,
			EN: append([]string(nil), row.EN...), FR: append([]string(nil), row.FR...)}
	}
	return out
}

// Match finds the catalogue path the folded words name. The longest alias run
// wins, so "status updates" is the project status subtopic and not the news
// topic; two different paths matched at the same longest length are ambiguous
// (a comparison or a compound question) and return ok false. The returned
// words are the run the caller must subtract from the question residual.
func Match(words []string) (Phrase, []string, bool) {
	var (
		best      Phrase
		bestRun   []string
		bestLen   int
		ambiguous bool
	)
	for _, entry := range aliasIndex {
		if bestLen > 0 && len(entry.words) < bestLen {
			break
		}
		run, ok := matchRun(words, entry.words)
		if !ok {
			continue
		}
		if len(entry.words) > bestLen {
			best, bestRun, bestLen, ambiguous = entry.phrase, run, len(entry.words), false
			continue
		}
		if entry.phrase.Topic != best.Topic || entry.phrase.Subtopic != best.Subtopic {
			ambiguous = true
		}
	}
	if bestLen == 0 || ambiguous {
		return Phrase{}, nil, false
	}
	return best, append([]string(nil), bestRun...), true
}

// matchRun returns the first contiguous run of words equal to alias.
func matchRun(words, alias []string) ([]string, bool) {
	if len(alias) == 0 || len(words) < len(alias) {
		return nil, false
	}
	for i := 0; i+len(alias) <= len(words); i++ {
		match := true
		for j, want := range alias {
			if words[i+j] != want {
				match = false
				break
			}
		}
		if match {
			return words[i : i+len(alias)], true
		}
	}
	return nil, false
}
