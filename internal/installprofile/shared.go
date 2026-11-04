package installprofile

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// SharedReport is the measurement behind the learned shared domains.
type SharedReport struct {
	Domains    []string `json:"domains"`
	Senders    int      `json:"senders"`    // distinct correspondents measured
	Candidates int      `json:"candidates"` // registrable domains seen
	Threads    int      `json:"threads"`
}

// sharedDomains finds the registrable domains used by many unrelated
// people: at least minSenders distinct correspondents, and at most
// maxShare of the threads involving the domain hold two of its addresses
// (colleagues copy each other; strangers on one free-mail domain never
// meet in a thread). The owner's own addresses are left out of both
// counts — the owner is in every thread.
func sharedDomains(a *threads.Analysis, owners map[string]bool, minSenders int, maxShare float64, extra ...string) SharedReport {
	senders := map[string]map[string]bool{}
	involved, together := map[string]int{}, map[string]int{}
	all := map[string]bool{}
	for _, t := range a.Threads {
		people := map[string]map[string]bool{}
		for _, i := range t.Members {
			m := &a.Messages[i]
			for _, addr := range append([]string{m.Sender}, m.Recipients...) {
				d := mail.RegistrableDomain(addr)
				if d == "" || owners[addr] {
					continue
				}
				if people[d] == nil {
					people[d] = map[string]bool{}
				}
				people[d][addr] = true
			}
			if d := mail.RegistrableDomain(m.Sender); d != "" && !owners[m.Sender] {
				if senders[d] == nil {
					senders[d] = map[string]bool{}
				}
				senders[d][m.Sender] = true
				all[m.Sender] = true
			}
		}
		for d, p := range people {
			involved[d]++
			if len(p) >= 2 {
				together[d]++
			}
		}
	}
	rep := SharedReport{Senders: len(all), Candidates: len(senders), Threads: len(a.Threads), Domains: []string{}}
	shared := map[string]bool{}
	for d, s := range senders {
		if len(s) >= minSenders && involved[d] > 0 && float64(together[d]) <= maxShare*float64(involved[d]) {
			shared[d] = true
		}
	}
	// The thread graph misses a free-mail domain whose senders sometimes share
	// a thread; the address book supplies that evidence. Union both here, once,
	// so a domain learned from either source appears exactly once, sorted.
	for _, d := range extra {
		if d != "" {
			shared[d] = true
		}
	}
	for d := range shared {
		rep.Domains = append(rep.Domains, d)
	}
	slices.Sort(rep.Domains)
	return rep
}
