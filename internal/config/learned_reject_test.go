package config

import (
	"slices"
	"testing"
)

// Review L0 rules §5: the user rejects a learned value from the UI; the
// rejection applies at once, is persisted, survives the next Learn and is
// queryable; misuse is refused.
func TestRejectALearnedValue(t *testing.T) {
	dir := t.TempDir()
	c := loadIn(t, dir)
	prov := Provenance{Learner: "installprofile.shared_domains", Basis: map[string]int{"senders": 40}, LearnedAt: "2026-10-15T08:00:00Z"}
	if err := c.Reject("mail.domains.shared", "example.net"); err == nil {
		t.Fatal("nothing learned yet: nothing to reject")
	}
	if err := c.Learn("mail.domains.shared", []string{"example.net", "example.org"}, 40, prov); err != nil {
		t.Fatal(err)
	}
	if err := c.Reject("mail.domains.shared", "no dot"); err == nil {
		t.Fatal("an invalid item is refused (the setting's pattern)")
	}
	if err := c.Reject("mail.domains.shared", "example.org"); err != nil {
		t.Fatal(err)
	}
	if got := c.Strings("mail.domains.shared"); !slices.Equal(got, []string{"example.net"}) {
		t.Fatalf("applied at once: %v", got)
	}
	if err := c.Learn("mail.domains.shared", []string{"example.org", "example.com"}, 41, prov); err != nil {
		t.Fatal(err)
	}
	reloaded := loadIn(t, dir)
	if got := reloaded.Strings("mail.domains.shared"); !slices.Equal(got, []string{"example.com"}) {
		t.Fatalf("persisted and never learned again: %v", got)
	}
	if l := reloaded.Learned(); len(l) != 1 || len(l[0].Rejected) != 1 || l[0].Rejected[0] != "example.org" {
		t.Fatalf("queryable: %+v", l)
	}
	if err := c.Unreject("mail.domains.shared", "example.org"); err != nil {
		t.Fatal(err)
	}
	if got := c.Strings("mail.domains.shared"); !slices.Equal(got, []string{"example.org", "example.com"}) {
		t.Fatalf("withdrawn: %v", got)
	}
	for name, err := range map[string]error{
		"not learnable": c.Reject("log.level", "debug"),
		"unknown":       c.Reject("nope", 1),
	} {
		if err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}
