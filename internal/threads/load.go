package threads

import (
	"context"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Settings are the threads.* / mail.subject.* / mail.domains.* /
// time.zone settings.
type Settings struct {
	Normalizer   *mail.SubjectNormalizer
	Domains      mail.Domains
	Roles        []mail.FolderRole
	ExcerptChars int
	Options      Options
}

// Setting keys SettingsFrom reads.
const (
	SettingMaxPrefixes = "mail.subject.max_prefixes"
	SettingBuckets     = "threads.response_buckets"
	settingExamples    = "threads.examples_per_sender"
	settingRoles       = "threads.folder_roles"
	settingExcerpt     = "threads.reply_excerpt_chars"
)

// GroupingSettings are the settings conversations and the history's
// organisations depend on (the subject normalizer, the shared domains):
// every view built on threads reads them.
func GroupingSettings() []string {
	return []string{SettingMaxPrefixes, mail.SettingSubjectPrefixes, mail.SettingSubjectPrefixesLearned, mail.SettingSharedDomains}
}

// SettingsFrom reads the settings once (one snapshot: values agree).
func SettingsFrom(cfg *config.Config) (Settings, error) {
	s := cfg.Snapshot()
	norm, err := mail.NewSubjectNormalizer(s.Int(SettingMaxPrefixes), mail.SubjectPrefixes(s.Strings(mail.SettingSubjectPrefixes)),
		s.Strings(mail.SettingSubjectPrefixesLearned))
	if err != nil {
		return Settings{}, err
	}
	zone, err := clock.Zone(s.String(clock.SettingZone))
	if err != nil {
		return Settings{}, err
	}
	o := Options{Zone: zone, BucketMinutes: s.Ints(SettingBuckets), ExamplesPerSender: s.Int(settingExamples)}
	if err := o.Validate(); err != nil {
		return Settings{}, err
	}
	var roles []mail.FolderRole
	for _, r := range s.Strings(settingRoles) {
		roles = append(roles, mail.FolderRole(r))
	}
	return Settings{Normalizer: norm, Domains: mail.NewDomains(s.Strings(mail.SettingSharedDomains)), Roles: roles,
		ExcerptChars: s.Int(settingExcerpt), Options: o}, nil
}

// CausalRoles are the folder roles of the causal view of the mailbox:
// every role but drafts (an unsent draft is no mail that happened). Where
// a message is filed now is the owner's later action, so a view of what
// a message could see at its own instant never depends on it.
func CausalRoles() []mail.FolderRole {
	return slices.DeleteFunc(mail.Roles(), func(r mail.FolderRole) bool { return r == mail.RoleDrafts })
}

// Load reads the owner-scope mail of every folder of set.Roles of active
// folders and analyses it: CausalRoles for evidence and triage (the
// causal view), threads.folder_roles (all but drafts, deleted items and
// junk by default) for the install-profile learners.
// The owner's own messages are the ones scope judged sent by the owner,
// wherever they are filed.
func Load(ctx context.Context, st *store.Store, set Settings) (*Analysis, error) {
	return load(ctx, st, set, threadFields)
}

// load is Load over one message projection: the threads set in production,
// the full read in the equivalence test.
func load(ctx context.Context, st *store.Store, set Settings, fields store.MessageFields) (*Analysis, error) {
	var msgs []Message
	err := st.EachMessage(ctx, store.MessageFilter{Roles: set.Roles, OwnerOnly: true, Fields: fields},
		func(r *store.MessageRow) error {
			msgs = append(msgs, FromMail(r.RowID, &r.Message, r.Reason == scope.ReasonSentByOwner, set.ExcerptChars, set.Domains))
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("threads: load: %w", err)
	}
	a := Analyze(msgs, set.Normalizer)
	log.Debug("threads analysed", "messages", len(msgs), "threads", len(a.Threads), "my_replies", len(a.MyReplies),
		"their_replies", len(a.TheirReplies))
	return a, nil
}

var log = logx.For("threads")
