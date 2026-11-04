package source

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Settings of the explicit mailbox selection (schema.json owns them).
const (
	// SettingSelected lists the picked mailbox stores by StoreKey. It is
	// empty by default: nothing syncs, and no store is ever preselected.
	SettingSelected = "mailboxes.selected"
	// SettingDeclared lists owner addresses the user declares for a
	// store ("<store key>=<address>"), for sources that report none (a
	// PST, a file import) or to add an alias.
	SettingDeclared = "mailboxes.declared_addresses"
)

// Selected returns the picked store keys of a configuration snapshot.
func Selected(s *config.Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, k := range s.Strings(SettingSelected) {
		out[k] = true
	}
	return out
}

// Declared returns the declared owner addresses of each store key, as
// declared and in setting order (scope.NewIdentity normalizes them).
func Declared(s *config.Snapshot) map[string][]string {
	out := map[string][]string{}
	for _, p := range s.Strings(SettingDeclared) {
		key, addr, _ := strings.Cut(p, "=") // the setting's pattern guarantees the "="
		out[key] = append(out[key], addr)
	}
	return out
}

// ErrUnknownMailbox is returned for a store key no discovery has listed.
var ErrUnknownMailbox = errors.New("source: no discovered mailbox has this key")

// ErrInvalidAddress is returned for an owner address that is neither an
// SMTP address nor an Exchange DN.
var ErrInvalidAddress = errors.New("source: not a mail address")

// Pick adds stores to the selection (the user's explicit choice, saved to
// settings.json). Every key must be a discovered mailbox (known);
// duplicates and keys already picked are no-ops.
func Pick(cfg *config.Config, known []Mailbox, keys ...string) error {
	if len(keys) == 0 {
		return errors.New("source: no mailbox given")
	}
	cur := slices.Clone(cfg.Strings(SettingSelected))
	for _, k := range keys {
		if !slices.ContainsFunc(known, func(m Mailbox) bool { return m.Key == k }) {
			return fmt.Errorf("%w: %q", ErrUnknownMailbox, k)
		}
		if !slices.Contains(cur, k) {
			cur = append(cur, k)
		}
	}
	return cfg.Set(SettingSelected, cur)
}

// Unpick removes stores from the selection (a key that is not picked is
// a no-op). Their mail is kept, outside every view, until they are picked
// again.
func Unpick(cfg *config.Config, keys ...string) error {
	if len(keys) == 0 {
		return errors.New("source: no mailbox given")
	}
	cur := slices.DeleteFunc(slices.Clone(cfg.Strings(SettingSelected)), func(v string) bool { return slices.Contains(keys, v) })
	return cfg.Set(SettingSelected, append([]string{}, cur...))
}

// Declare adds an owner address of a discovered store (an SMTP address or
// an Exchange DN; declaring one twice is a no-op).
func Declare(cfg *config.Config, known []Mailbox, key, address string) error {
	if !slices.ContainsFunc(known, func(m Mailbox) bool { return m.Key == key }) {
		return fmt.Errorf("%w: %q", ErrUnknownMailbox, key)
	}
	a := strings.TrimSpace(address)
	if local, domain, ok := strings.Cut(a, "@"); !(ok && local != "" && domain != "" && !strings.ContainsAny(a, " \t,;<>")) && !mail.IsLegacyDN(a) {
		return fmt.Errorf("%w: %q", ErrInvalidAddress, address)
	}
	entry := key + "=" + a
	cur := slices.Clone(cfg.Strings(SettingDeclared))
	if slices.ContainsFunc(cur, func(v string) bool { return strings.EqualFold(v, entry) }) {
		return nil
	}
	return cfg.Set(SettingDeclared, append(cur, entry))
}

// Undeclare removes a declared address of a store (case-insensitive; an
// address that is not declared is a no-op).
func Undeclare(cfg *config.Config, key, address string) error {
	entry := key + "=" + strings.TrimSpace(address)
	cur := slices.DeleteFunc(slices.Clone(cfg.Strings(SettingDeclared)), func(v string) bool { return strings.EqualFold(v, entry) })
	return cfg.Set(SettingDeclared, append([]string{}, cur...))
}

// Scope names the current selection (sorted keys): what learning covers.
func Scope(s *config.Snapshot) string {
	keys := slices.Clone(s.Strings(SettingSelected))
	slices.Sort(keys)
	return strings.Join(slices.Compact(keys), ",")
}
