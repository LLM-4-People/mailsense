package store

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// The stored sender keys are the writer's output, and the sender filter
// compares against them. They must be exactly the two mail rules, or a filter
// that is correct in Go silently misses rows in SQL.
func TestSenderKeysAreTheMailRules(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	cases := []mail.Message{
		msgAt("mixed", mail.RoleInbox, "2026-01-01T00:00:00Z"),
		msgAt("unicode", mail.RoleInbox, "2026-01-02T00:00:00Z"),
		msgAt("plain", mail.RoleInbox, "2026-01-03T00:00:00Z"),
		msgAt("no-at", mail.RoleInbox, "2026-01-04T00:00:00Z"),
		msgAt("empty", mail.RoleInbox, "2026-01-05T00:00:00Z"),
	}
	cases[0].SenderAddress = "  MiXeD@Sub.Example.TEST  "
	cases[1].SenderAddress = "Élodie@Exemple.TEST"
	cases[2].SenderAddress = "plain@example.test"
	cases[3].SenderAddress = "postmaster"
	cases[4].SenderAddress = ""
	if err := s.UpsertMessages(ctx, 0, cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		norm := mail.NormalizeAddress(c.SenderAddress)
		wantNorm, wantDomain := norm, mail.AddressDomain(norm)
		var gotNorm, gotDomain string
		if err := s.db.QueryRowContext(ctx,
			"SELECT sender_norm, sender_domain FROM messages WHERE msg_id = ?", c.ID).
			Scan(&gotNorm, &gotDomain); err != nil {
			t.Fatal(err)
		}
		if gotNorm != wantNorm || gotDomain != wantDomain {
			t.Errorf("%q stored (%q,%q), mail rules say (%q,%q)",
				c.SenderAddress, gotNorm, gotDomain, wantNorm, wantDomain)
		}
	}
}

// A domain filter is an exact domain: it must not widen to a subdomain, and
// it must read the stored key rather than scan the mailbox.
func TestSenderDomainFilterIsExactAndIndexed(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	rows := []mail.Message{
		msgAt("root", mail.RoleInbox, "2026-01-01T00:00:00Z"),
		msgAt("sub", mail.RoleInbox, "2026-01-02T00:00:00Z"),
		msgAt("other", mail.RoleInbox, "2026-01-03T00:00:00Z"),
	}
	rows[0].SenderAddress = "a@example.test"
	rows[1].SenderAddress = "b@sub.example.test"
	rows[2].SenderAddress = "c@elsewhere.test"
	ids := seedMsgs(t, s, rows...)
	for _, id := range ids {
		if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.FilterIDs(ctx, SearchFilter{Sender: "@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != ids["root"] {
		t.Fatalf("the domain filter returned %v, want only the root domain row", got)
	}

	for _, tc := range []struct{ name, where string }{
		{"exact", "m.sender_norm = 'a@example.test'"},
		{"domain", "m.sender_domain = 'example.test'"},
	} {
		q := `EXPLAIN QUERY PLAN SELECT m.id FROM messages m
			JOIN folders f ON f.id = m.folder_id
			JOIN scope_verdicts v ON v.message_id = m.id
			WHERE f.active = 1 AND f.picked = 1 AND v.verdict = 'owner' AND ` + tc.where + `
			ORDER BY m.instant DESC, m.id DESC LIMIT 101`
		planRows, err := s.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for planRows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail)
			plan.WriteString("\n")
		}
		_ = planRows.Close()
		index := "messages_sender_norm"
		if tc.name == "domain" {
			index = "messages_sender_domain"
		}
		if !strings.Contains(plan.String(), index) || strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Fatalf("%s sender plan does not read %s in page order:\n%s", tc.name, index, plan.String())
		}
	}
}
