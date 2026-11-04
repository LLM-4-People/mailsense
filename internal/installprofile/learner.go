// Package installprofile runs the learners of the install profile (the
// learned layer of config, between the shipped defaults and the user's
// overrides): what this install measures about its own mail. Each learner
// is structural (never a word list), records its sample size and
// provenance through config.Learn, and follows sync runs that advanced the
// stored mail through one coalescing, rate-limited worker.
package installprofile

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Learner learns the install profile from the stored mail.
type Learner struct {
	cfg   *config.Config
	st    *store.Store
	clock clock.Clock
	log   *slog.Logger
	// admissionMu guards the late-bound resource guard. It is read once per
	// pass, never held across a store scan.
	admissionMu sync.RWMutex
	admission   Admission
}

// Admission is the resource guard's one decision for a learning pass. A nil
// dependency means no admission: tests, tools and a build without a guard run
// exactly as before.
type Admission interface {
	Admit(ctx context.Context) error
}

// SetAdmission installs or clears the resource guard after construction. The
// app builds the learner before its API wire owns the guard, so this is the
// one late binding; a nil value restores the no-admission behavior.
func (l *Learner) SetAdmission(a Admission) {
	l.admissionMu.Lock()
	l.admission = a
	l.admissionMu.Unlock()
}

// admit asks the guard whether a whole-mailbox learning pass may run.
func (l *Learner) admit(ctx context.Context) error {
	l.admissionMu.RLock()
	a := l.admission
	l.admissionMu.RUnlock()
	if a == nil {
		return nil
	}
	return a.Admit(ctx)
}

// New returns a learner; clk may be nil.
func New(cfg *config.Config, st *store.Store, clk clock.Clock) *Learner {
	return &Learner{cfg: cfg, st: st, clock: clock.Or(clk), log: logx.For("installprofile")}
}

// Report is what one learning pass measured and learned.
type Report struct {
	Messages int          `json:"messages"`
	Shared   SharedReport `json:"shared_domains"`
	Prefixes PrefixReport `json:"reply_prefixes"`
}

// Run measures the owner-scope mail of the picked mailboxes and stores
// the learned values (shared domains, reply prefixes).
func (l *Learner) Run(ctx context.Context) (Report, error) {
	start := time.Now()
	set, err := threads.SettingsFrom(l.cfg)
	if err != nil {
		return Report{}, err
	}
	a, err := threads.Load(ctx, l.st, set)
	if err != nil {
		return Report{}, err
	}
	views, err := l.st.MailboxViews(ctx)
	if err != nil {
		return Report{}, err
	}
	owners := map[string]bool{} // of the picked stores only
	for _, v := range views {
		for _, a := range v.Identity.Addresses {
			owners[a] = owners[a] || v.Selected
		}
	}
	snap := l.cfg.Snapshot()
	rep := Report{Messages: len(a.Messages)}
	// One domain naming several unrelated declared companies is shared even
	// when those people never meet in a thread; the address book supplies that
	// evidence, and sharedDomains unions it with the thread graph.
	book, err := l.st.CompanyBook(ctx)
	if err != nil {
		return Report{}, err
	}
	rep.Shared = sharedDomains(a, owners, snap.Int("mail.domains.learn.min_senders"), snap.Float("mail.domains.learn.max_cothread_share"),
		book.StructurallySharedDomains()...)
	rep.Prefixes = replyPrefixes(a, mail.SubjectPrefixes(snap.Strings(mail.SettingSubjectPrefixes)), snap.Int("mail.subject.learn.min_count"),
		snap.Float("mail.subject.learn.min_reply_share"), snap.Int("mail.subject.learn.max_runes"))
	at := l.clock.Now().UTC().Format(mail.TimeLayout)
	if err := l.cfg.Learn(mail.SettingSharedDomains, rep.Shared.Domains, rep.Shared.Senders, config.Provenance{Learner: "installprofile.shared_domains",
		Basis:     map[string]int{"senders": rep.Shared.Senders, "domains": rep.Shared.Candidates, "threads": rep.Shared.Threads},
		LearnedAt: at}); err != nil {
		return rep, err
	}
	if err := l.cfg.Learn(mail.SettingSubjectPrefixesLearned, rep.Prefixes.Prefixes, rep.Prefixes.Replies, config.Provenance{Learner: "installprofile.reply_prefixes",
		Basis:     map[string]int{"replies": rep.Prefixes.Replies, "subjects": rep.Prefixes.Subjects},
		LearnedAt: at}); err != nil {
		return rep, err
	}
	l.log.Info("install profile learned", "duration", time.Since(start), "messages", rep.Messages, "shared_domains", len(rep.Shared.Domains), "reply_prefixes", len(rep.Prefixes.Prefixes))
	return rep, nil
}
