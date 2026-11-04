package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/scope"
)

// State says whether a signal has a value for a message.
type State string

// Value states.
const (
	OK State = "ok"
	// NA: the signal does not apply (no previous message in the thread, no
	// amount to measure); Reason says why.
	NA State = "na"
	// Unavailable: the input the signal needs is missing for this message
	// (a capability its source lacks, a sent item without transport
	// headers, a message without a vector): never guessed.
	Unavailable State = "unavailable"
)

// Reasons of NA and Unavailable values (machine-readable).
const (
	ReasonCapability = "capability:"     // + the capability name (source.CapabilityNames)
	ReasonNoHeaders  = "no_headers"      // the message carries no transport headers (a sent item)
	ReasonNoDate     = "no_date"         // the message has no usable time
	ReasonPending    = "details_pending" // the source has not delivered the message's details yet
	ReasonNotThread  = "not_threaded"    // outside the threads (not owner-scope mail, a draft)
	ReasonNoVector   = "no_vector"       // no embedding of the current space
	ReasonSent       = string(scope.ReasonSentByOwner)
	ReasonNone       = "none"  // nothing to measure (no amount, no previous message…)
	ReasonFirst      = "first" // the first message of its thread or of its correspondent
)

// Value is one signal's value for one message. Partial marks a value
// computed on a truncated input (a header block or body cut by its
// source): a count is a lower bound, and false or "none" means "not in
// the part that was read".
type Value struct {
	State   State
	Reason  string
	Partial bool
	Bool    bool
	Num     float64
	Str     string
	List    []string
	Ints    []int
}

// Constructors.
func Bool(b bool) Value            { return Value{State: OK, Bool: b} }
func Num(n float64) Value          { return Value{State: OK, Num: n} }
func Int(n int) Value              { return Value{State: OK, Num: float64(n)} }
func Str(s string) Value           { return Value{State: OK, Str: s} }
func List(l []string) Value        { return Value{State: OK, List: l} }
func Ints(l []int) Value           { return Value{State: OK, Ints: l} }
func NotApplicable(r string) Value { return Value{State: NA, Reason: r} }
func Missing(r string) Value       { return Value{State: Unavailable, Reason: r} }

// Cut marks a value as computed on a truncated input.
func (v Value) Cut(truncated bool) Value {
	v.Partial = v.Partial || truncated
	return v
}

// Equal reports whether two values are the same.
func (v Value) Equal(o Value) bool {
	return v.State == o.State && v.Reason == o.Reason && v.Partial == o.Partial && v.Bool == o.Bool && v.Num == o.Num &&
		v.Str == o.Str && slices.Equal(v.List, o.List) && slices.Equal(v.Ints, o.Ints)
}

// encode is the stored form: the bare value when OK and complete,
// {"p": value} when partial, {"n": reason} / {"u": reason} otherwise —
// the reason by its interned id (reason) —, with "p": true when read on a
// cut input ("not there in the part read").
func (v Value) encode(t Type, reason func(string) (int64, error)) (json.RawMessage, error) {
	switch v.State {
	case NA, Unavailable:
		key := map[State]string{NA: "n", Unavailable: "u"}[v.State]
		id, err := reason(v.Reason)
		if err != nil {
			return nil, err
		}
		if v.Partial {
			return json.Marshal(map[string]any{key: id, "p": true})
		}
		return json.Marshal(map[string]int64{key: id})
	case OK:
	default:
		return nil, fmt.Errorf("evidence: value state %q", v.State)
	}
	var bare any
	switch t {
	case TypeBool:
		bare = v.Bool
	case TypeInt, TypeFloat:
		bare = v.Num
	case TypeEnum, TypeCategory:
		bare = v.Str
	case TypeSet:
		bare = nonNil(v.List)
	case TypeInts:
		bare = nonNil(v.Ints)
	default:
		return nil, fmt.Errorf("evidence: type %q", t)
	}
	if v.Partial {
		return json.Marshal(map[string]any{"p": bare})
	}
	return json.Marshal(bare)
}

func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

// decode reads a stored value of type t; reason gives the text of an
// interned reason (a reason stored as text is read as it is).
func decode(raw json.RawMessage, t Type, reason func(int64) (string, error)) (Value, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		var o map[string]json.RawMessage
		if err := json.Unmarshal(raw, &o); err != nil {
			return Value{}, err
		}
		for key, state := range map[string]State{"n": NA, "u": Unavailable} {
			if r, ok := o[key]; ok {
				v := Value{State: state, Partial: string(o["p"]) == "true"}
				var id int64
				if json.Unmarshal(r, &id) == nil {
					var err error
					v.Reason, err = reason(id)
					return v, err
				}
				return v, json.Unmarshal(r, &v.Reason)
			}
		}
		p, ok := o["p"]
		if !ok {
			return Value{}, fmt.Errorf("evidence: stored value %s", raw)
		}
		v, err := decode(p, t, reason)
		v.Partial = true
		return v, err
	}
	v := Value{State: OK}
	var err error
	switch t {
	case TypeBool:
		err = json.Unmarshal(raw, &v.Bool)
	case TypeInt, TypeFloat:
		err = json.Unmarshal(raw, &v.Num)
	case TypeEnum, TypeCategory:
		err = json.Unmarshal(raw, &v.Str)
	case TypeSet:
		err = json.Unmarshal(raw, &v.List)
	case TypeInts:
		err = json.Unmarshal(raw, &v.Ints)
	default:
		err = fmt.Errorf("evidence: type %q", t)
	}
	return v, err
}
