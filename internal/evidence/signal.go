package evidence

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// Type is the type of a signal's values.
type Type string

// Value types.
const (
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeFloat    Type = "float"
	TypeEnum     Type = "enum"     // a closed vocabulary (Signal.Values)
	TypeCategory Type = "category" // an open, opaque value (a hash): hashed into features
	TypeSet      Type = "set"      // a set of categories
	TypeInts     Type = "ints"     // a list of integers (date distances)
)

// Signal is one extractor of the registry: one small file each (sig_*.go).
type Signal struct {
	Name    string
	Version int // bump on any change of what Extract returns: only this signal is recomputed
	Type    Type
	Values  []string // the vocabulary of an enum
	// Needs are capabilities the source must declare (all of them); AnyOf
	// capabilities of which one is enough. A message whose source lacks
	// them gets the value Unavailable (ReasonCapability + name).
	Needs []string
	AnyOf []string
	// Causal: the value uses only data at or before the message's own
	// time; only causal signals feed features, facts and rules. The
	// owner's later actions (folder, flags, read state, replies) are not.
	Causal bool
	// Context: the value depends on other messages (history, threads,
	// vectors) and is computed again by every run; otherwise it depends on
	// the message alone and is reused while the message is unchanged.
	Context bool
	// Settings are the settings Extract reads: their values are part of
	// the signal's effective version (a change recomputes it).
	Settings []string
	// Data are the pinned data files Extract reads (Data* names): their
	// digests are part of the effective version (regenerated data
	// recomputes it).
	Data []string
	// Summary: Extract reads only the summary fields of a message (sender,
	// times, folder, flags, verdict), so it has a value while the
	// message's details (body, headers, recipients, attachments, thread
	// ids) are pending (mail.Message.Partial); every other signal is then
	// unavailable (ReasonPending).
	Summary bool
	// Unsure maps a value that, read on a cut input (Value.Partial), may
	// only mean "not in the part read" (none, absent; "false" of a bool)
	// to the values the whole input could give instead (nil: any other):
	// a rule takes it as settled only when every one of them would satisfy
	// it too (a partial "present" List-Unsubscribe may be "one_click").
	Unsure map[string][]string
	// Bound says how a number (an ints list: its length) read on a cut
	// input relates to the whole input's.
	Bound Bound
	// Opaque: the values are hashes (a mailer, a folder): meaningful as
	// features, never shown as facts.
	Opaque bool
	// Shown maps a value to what aggregates (Coverage) show of it (nil:
	// the value as it is): a custom message class is "custom".
	Shown   func(string) string
	Group   string // headers, recipients, thread, history, content, owner
	Doc     string
	Extract func(*Input) Value
}

// Bound is what a number read on a cut input tells about the whole input.
type Bound string

// Bounds of partial numbers.
const (
	// BoundUnknown (the zero value): either way; a rule never takes a
	// partial number as settled.
	BoundUnknown Bound = ""
	// AtLeast: a count; more may lie past the cut.
	AtLeast Bound = "at_least"
	// AtMost: a nearest distance; a nearer one may lie past the cut.
	AtMost Bound = "at_most"
)

// Groups of signals (documentation and the UI).
const (
	GroupHeaders    = "headers"
	GroupRecipients = "recipients"
	GroupThread     = "thread"
	GroupHistory    = "history"
	GroupContent    = "content"
	GroupOwner      = "owner_actions"
)

var registry struct {
	mu      sync.Mutex
	signals []Signal
}

// register adds a signal (init of its file).
func register(s Signal) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.signals = append(registry.signals, s)
}

// Signals lists the registered signals, sorted by name.
func Signals() []Signal {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	out := slices.Clone(registry.signals)
	slices.SortFunc(out, func(a, b Signal) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Lookup returns a registered signal by name.
func Lookup(name string) (Signal, bool) {
	for _, s := range Signals() {
		if s.Name == name {
			return s, true
		}
	}
	return Signal{}, false
}

// Validate checks the registry: unique names, a version, a known type,
// an enum's vocabulary, known capabilities, a group, a doc and an
// extractor. Every problem is reported.
func Validate(signals []Signal) error {
	var errs []error
	seen := map[string]bool{}
	caps := source.CapabilityNames()
	types := []Type{TypeBool, TypeInt, TypeFloat, TypeEnum, TypeCategory, TypeSet, TypeInts}
	for _, s := range signals {
		bad := func(f string, a ...any) {
			errs = append(errs, fmt.Errorf("evidence: signal %q: "+f, append([]any{s.Name}, a...)...))
		}
		if s.Name == "" || seen[s.Name] {
			bad("empty or duplicate name")
		}
		seen[s.Name] = true
		if s.Version < 1 || !slices.Contains(types, s.Type) || s.Extract == nil || s.Doc == "" || s.Group == "" {
			bad("needs a version ≥ 1, a known type, an extractor, a group and a doc")
		}
		if (s.Type == TypeEnum) != (len(s.Values) > 0) {
			bad("an enum (and only an enum) has a vocabulary")
		}
		for _, c := range append(slices.Clone(s.Needs), s.AnyOf...) {
			if !slices.Contains(caps, c) {
				bad("unknown capability %q", c)
			}
		}
		for u, could := range s.Unsure {
			for _, x := range append([]string{u}, could...) {
				if s.Type == TypeEnum && !slices.Contains(s.Values, x) || s.Type == TypeBool && x != "true" && x != "false" ||
					numeric(s.Type) || s.Type == TypeSet {
					bad("unsure value %q is not a value of its type", x)
				}
			}
		}
		for _, d := range s.Data {
			if _, ok := dataDigest(d); !ok {
				bad("unknown data %q", d)
			}
		}
		if s.Bound != BoundUnknown && (!numeric(s.Type) || s.Bound != AtLeast && s.Bound != AtMost) {
			bad("bound %q needs a number and is at_least or at_most", s.Bound)
		}
	}
	return errors.Join(errs...)
}
