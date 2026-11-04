// Package secrets stores API keys. On Windows they live in one file in the
// data directory encrypted with DPAPI for the current user; elsewhere (dev)
// they are read-only MAILSENSE_SECRET_<NAME> environment variables. Every
// value is registered with logx redaction when loaded or set, and every
// change is published (names only, never values).
package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Store is the secret storage interface.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
	List() ([]string, error)
	Kind() string   // "dpapi" or "env"
	ReadOnly() bool // Set/Delete always fail with ErrReadOnly
}

// Errors returned by stores.
var (
	ErrNotFound = errors.New("secrets: not found")
	ErrReadOnly = errors.New("secrets: store is read-only (set " + config.EnvSecretPrefix + "<NAME> instead)")
)

// maxNameLength bounds secret names (they double as environment variable
// suffixes and UI labels).
const maxNameLength = 64

// Names are upper snake case so they map 1:1 to environment variables.
var nameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,` + strconv.Itoa(maxNameLength-1) + `}$`)

// ValidName checks a secret name (upper snake case: it doubles as the
// MAILSENSE_SECRET_<NAME> variable suffix).
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("secrets: invalid name %q (want %s)", name, nameRE)
	}
	return nil
}

// Change is published after a secret was set or deleted.
type Change struct {
	Name   string `json:"name"`
	Action string `json:"action"` // "set" or "delete"
}

// TopicChanged carries secret changes (never values).
var TopicChanged = events.NewTopic[Change]("secrets.changed")

// Open returns the platform store for cfg's data directory. bus and clk may be nil.
func Open(cfg *config.Config, environ []string, bus *events.Bus, clk clock.Clock) (Store, error) {
	policy := func() fsx.Replace { return cfg.Snapshot().Replace(clk) }
	inner, err := openPlatform(cfg.Paths().Resolve(cfg.String("secrets.file")), environ, policy)
	if err != nil {
		return nil, err
	}
	names, err := inner.List()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if v, err := inner.Get(n); err == nil {
			logx.RegisterSecret(v)
		}
	}
	logx.For("secrets").Info("secret store opened", "kind", inner.Kind(), "read_only", inner.ReadOnly(), "count", len(names))
	return &observed{Store: inner, bus: bus}, nil
}

// observed is the choke point for redaction registration and change events.
type observed struct {
	Store
	bus *events.Bus
}

func (o *observed) Set(name, value string) error {
	logx.RegisterSecret(value) // before anything can log it
	if err := o.Store.Set(name, value); err != nil {
		return err
	}
	o.publish(name, "set")
	return nil
}

func (o *observed) Delete(name string) error {
	if err := o.Store.Delete(name); err != nil {
		return err
	}
	o.publish(name, "delete")
	return nil
}

func (o *observed) publish(name, action string) {
	logx.For("secrets").Info("secret changed", "name", name, "action", action)
	if o.bus != nil {
		events.Publish(o.bus, TopicChanged, Change{Name: name, Action: action})
	}
}
