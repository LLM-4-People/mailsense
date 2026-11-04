package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Catalog is the complete set of provider records read from
// engine.providers.file. A missing or empty file is an empty catalog: there
// are no built-in providers. Digest identifies the document the records came
// from; it is part of the route key's decider identity, so editing a
// provider moves the key even before the role is configured.
type Catalog struct {
	Digest string
	byID   map[string]Provider
	order  []string
}

// ProvidersFileSetting names the one provider catalog file.
const ProvidersFileSetting = "engine.providers.file"

// LoadCatalog reads engine.providers.file and validates every record,
// reporting all problems at once. A missing file means no providers.
func LoadCatalog(cfg *config.Config) (*Catalog, error) {
	path := cfg.Paths().Resolve(cfg.String(ProvidersFileSetting))
	data, ok, err := fsx.ReadOptional(path)
	if err != nil {
		return nil, fmt.Errorf("engine: providers file %s: %w", path, err)
	}
	if !ok {
		data = nil
	}
	cat, err := ParseCatalog(data, limitsOf(cfg.Snapshot()))
	if err != nil {
		return nil, fmt.Errorf("engine: %s: %w", path, err)
	}
	return cat, nil
}

// ParseCatalog decodes and validates a provider document. The top-level
// object is keyed by immutable record id; every record is strict (unknown
// fields are errors). Empty or whitespace-only data is an empty catalog.
func ParseCatalog(data []byte, l limits) (*Catalog, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &Catalog{Digest: digest.Hex(nil, 32), byID: map[string]Provider{}}, nil
	}
	v, err := jsonx.DecodeOrdered(data)
	if err != nil {
		return nil, fmt.Errorf("invalid providers: %w", err)
	}
	doc, ok := v.(jsonx.Object)
	if !ok {
		return nil, errors.New("invalid providers: must be an object keyed by record id")
	}
	cat := &Catalog{Digest: digest.Hex(bytes.TrimSpace(data), 32), byID: make(map[string]Provider, len(doc))}
	var problems []string
	problems = append(problems, protocolProblems()...)
	for _, m := range doc {
		raw, ok := m.Value.(jsonx.Object)
		if !ok {
			problems = append(problems, fmt.Sprintf("providers/%s: must be an object", m.Key))
			continue
		}
		for _, k := range raw.Keys() {
			if k == "id" {
				problems = append(problems, fmt.Sprintf("providers/%s: id is the top-level key, not a record field", m.Key))
			}
		}
		p := Provider{ID: m.Key}
		if err := jsonx.DecodeStrict(marshalOrNil(m.Value), &p); err != nil {
			problems = append(problems, fmt.Sprintf("providers/%s: %v", m.Key, err))
			continue
		}
		p.ID = m.Key
		cat.byID[m.Key] = p
		cat.order = append(cat.order, m.Key)
	}
	for _, id := range cat.order {
		p := cat.byID[id]
		problems = append(problems, p.problems(l)...)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, &ValidationError{Problems: problems}
	}
	return cat, nil
}

func marshalOrNil(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return data
}

// BuildCatalog validates a complete record set (the API write path) and
// returns the catalog it describes.
func BuildCatalog(cfg *config.Config, records []Provider) (*Catalog, error) {
	cat := &Catalog{byID: make(map[string]Provider, len(records))}
	var problems []string
	problems = append(problems, protocolProblems()...)
	for _, p := range records {
		if _, dup := cat.byID[p.ID]; dup {
			problems = append(problems, fmt.Sprintf("providers/%s: duplicate id", p.ID))
			continue
		}
		cat.byID[p.ID] = p
		cat.order = append(cat.order, p.ID)
	}
	for _, id := range cat.order {
		p := cat.byID[id]
		problems = append(problems, p.problems(limitsOf(cfg.Snapshot()))...)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, &ValidationError{Problems: problems}
	}
	return cat, nil
}

// SaveCatalog writes the complete record set atomically to
// engine.providers.file. Callers validate first (BuildCatalog); SaveCatalog
// revalidates so a malformed set can never be written.
func SaveCatalog(cfg *config.Config, cat *Catalog) error {
	if err := cat.Validate(limitsOf(cfg.Snapshot())); err != nil {
		return err
	}
	data, err := cat.document()
	if err != nil {
		return err
	}
	path := cfg.Paths().Resolve(cfg.String(ProvidersFileSetting))
	if err := fsx.EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("engine: providers file %s: %w", path, err)
	}
	if err := fsx.WriteFileAtomic(path, data, fsx.FilePerm, cfg.Snapshot().Replace(nil)); err != nil {
		return fmt.Errorf("engine: providers file %s: %w", path, err)
	}
	return nil
}

// document encodes the provider file: a JSON object keyed by record id.
func (c *Catalog) document() ([]byte, error) {
	doc := make(map[string]Provider, len(c.order))
	for _, id := range c.order {
		p := c.byID[id]
		p.ID = "" // the object key is the id
		doc[id] = p
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("engine: encode providers: %w", err)
	}
	return append(data, '\n'), nil
}

// limitsOf reads the schema rules a provider's retry overrides must keep.
func limitsOf(s *config.Snapshot) limits {
	l := limits{never: httpx.NeverRetried(s)}
	if st, ok := s.Schema().Lookup("http.retry.max_attempts"); ok {
		lo, hi, _ := st.IntRange()
		l.minAttempts, l.maxAttempts = int(lo), int(hi)
	}
	return l
}

// Get returns a provider by record id.
func (c *Catalog) Get(id string) (Provider, bool) {
	p, ok := c.byID[id]
	return p, ok
}

// Select returns a record of one kind, or an explicit not-configured error
// (empty id, unknown id or the wrong kind). An empty kind accepts any kind.
func (c *Catalog) Select(id string, kind Kind) (Provider, error) {
	switch {
	case id == "":
		return Provider{}, fmt.Errorf("%w: no provider is selected", ErrNotConfigured)
	case c == nil:
		return Provider{}, fmt.Errorf("%w: %q is not a configured provider", ErrNotConfigured, id)
	}
	p, ok := c.byID[id]
	switch {
	case !ok:
		return Provider{}, fmt.Errorf("%w: %w: %q (known: %v)", ErrNotConfigured, ErrUnknownProvider, id, c.Names(kind))
	case kind != "" && p.Kind() != kind:
		return Provider{}, fmt.Errorf("%w: %q is a %s, not a %s", ErrNotConfigured, id, p.Kind(), kind)
	}
	return p, nil
}

// List returns every provider in file order (for the UI).
func (c *Catalog) List() []Provider {
	out := make([]Provider, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.byID[id])
	}
	return out
}

// Records returns a copy of every record (the API write path).
func (c *Catalog) Records() []Provider { return c.List() }

// Names returns the record ids of a kind, sorted.
func (c *Catalog) Names(kind Kind) []string {
	var out []string
	for id, p := range c.byID {
		if kind == "" || p.Kind() == kind {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Len is the number of records.
func (c *Catalog) Len() int { return len(c.order) }

// Role is one settings-owned selector of a provider record.
type Role struct {
	Setting string
	Label   string
	Kind    Kind
}

// ProviderRoles are the four role settings. Their values are record ids;
// empty means not configured.
func ProviderRoles() []Role {
	return []Role{
		{Setting: "triage.decider", Label: "triage decider", Kind: KindDecider},
		{Setting: "judge.provider", Label: "judge generator", Kind: KindGenerator},
		{Setting: "qa.provider", Label: "Q&A generator", Kind: KindGenerator},
		{Setting: "embed.provider", Label: "semantic embedder", Kind: KindEmbedder},
	}
}

// RolesUsing names the role settings that select id. A record write uses it
// to refuse a delete or kind change that would strand a configured role.
func RolesUsing(cfg *config.Config, id string) []string {
	var out []string
	for _, role := range ProviderRoles() {
		if cfg.String(role.Setting) == id {
			out = append(out, role.Setting)
		}
	}
	return out
}

// RoleStatus is the resolved state of one provider role selector. Configured
// means the selected record exists with the role's kind; Available means a
// configured record also has its key. Detail names the blocker in the words
// every screen and the startup log use, and is empty only when Available.
type RoleStatus struct {
	Role       Role
	Provider   string
	Configured bool
	Available  bool
	Detail     string
}

// RoleStatuses evaluates every role selector against the catalog and the
// provider statuses. It is the one owner of the role-state rules: the API
// projection and the startup log both render its answer instead of reading
// the selector, the catalog and the key presence themselves. statusByID may
// be nil where no health monitor exists (the startup log); configured roles
// then report the status as unavailable but stay Configured.
func RoleStatuses(cfg *config.Config, cat *Catalog, statusByID map[string]ProviderStatus) []RoleStatus {
	roles := ProviderRoles()
	out := make([]RoleStatus, 0, len(roles))
	for _, role := range roles {
		id := cfg.String(role.Setting)
		status := RoleStatus{Role: role, Provider: id}
		switch {
		case id == "":
			status.Detail = "not configured: no record is selected"
		default:
			if _, err := cat.Select(id, role.Kind); err != nil {
				status.Detail = err.Error()
				break
			}
			status.Configured = true
			st, ok := statusByID[id]
			switch {
			case !ok:
				status.Detail = "provider status is unavailable"
			case !st.KeyPresent:
				status.Detail = "the provider's API key is not set"
			default:
				status.Available = true
			}
		}
		out = append(out, status)
	}
	return out
}
