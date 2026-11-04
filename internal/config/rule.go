package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// rule validates and converts one raw value into its typed form:
// bool, int64, float64, string, time.Duration, []string or []int64.
type rule struct {
	key     string
	typ     Type
	item    *rule
	min     any
	max     any
	enum    map[string]bool
	pattern *regexp.Regexp
	format  func(string) error
	secret  bool
	notIn   []int64
}

func compileRule(st *Setting, s *Schema) (rule, error) {
	r := rule{key: st.Key, typ: st.Type, secret: st.Secret}
	if st.Type == TypeList {
		if st.ItemType != TypeString && st.ItemType != TypeInt {
			return r, fmt.Errorf("list item_type must be string or int, got %q", st.ItemType)
		}
		item := rule{key: st.Key, typ: st.ItemType, secret: st.Secret}
		if err := item.compileConstraints(st, s); err != nil {
			return r, err
		}
		r.item = &item
		return r, nil
	}
	if st.ItemType != "" {
		return r, errors.New("item_type is only valid for lists")
	}
	return r, r.compileConstraints(st, s)
}

func (r *rule) compileConstraints(st *Setting, s *Schema) error {
	switch r.typ {
	case TypeBool, TypeString:
	case TypeInt, TypeFloat, TypeDuration:
		var err error
		if r.min, err = r.bound(st.Min); err != nil {
			return fmt.Errorf("min: %w", err)
		}
		if r.max, err = r.bound(st.Max); err != nil {
			return fmt.Errorf("max: %w", err)
		}
	case TypeEnum:
		if len(st.Enum) < 1 {
			return errors.New("enum needs at least one value")
		}
		r.enum = map[string]bool{}
		for _, v := range st.Enum {
			r.enum[v] = true
		}
	default:
		return fmt.Errorf("unknown type %q", r.typ)
	}
	if err := r.compileNotIn(st); err != nil {
		return err
	}
	if len(st.Enum) > 0 && r.typ != TypeEnum {
		return errors.New("enum values are only valid for enum settings")
	}
	if (st.Min != nil || st.Max != nil) && r.min == nil && r.max == nil {
		return fmt.Errorf("min/max are not valid for %s values", r.typ)
	}
	if st.Pattern != "" || st.Format != "" {
		if r.typ != TypeString {
			return fmt.Errorf("pattern/format are only valid for string values, not %s", r.typ)
		}
	}
	if st.Pattern != "" {
		re, err := regexp.Compile(st.Pattern)
		if err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
		r.pattern = re
	}
	if st.Format != "" {
		f, err := compileFormat(st.Format, s)
		if err != nil {
			return err
		}
		r.format = f
	}
	return nil
}

// compileNotIn takes the forbidden values of an int rule.
func (r *rule) compileNotIn(st *Setting) error {
	if len(st.NotIn) > 0 && r.typ != TypeInt {
		return errors.New("not_in is only valid for int values")
	}
	r.notIn = st.NotIn
	return nil
}

// bound converts a schema min/max into the rule's typed form (nil when absent).
func (r *rule) bound(raw json.RawMessage) (any, error) {
	if raw == nil {
		return nil, nil
	}
	plain := rule{key: r.key, typ: r.typ}
	return plain.fromJSON(raw)
}

func (r *rule) fromJSON(raw json.RawMessage) (any, error) {
	v, err := jsonx.DecodeOne(raw)
	if err != nil {
		return nil, err
	}
	return r.fromAny(v)
}

// fromAny converts a JSON-decoded value (decoded with UseNumber).
func (r *rule) fromAny(v any) (any, error) {
	if r.typ == TypeList {
		items, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("must be a list, got %s", jsonx.Kind(v))
		}
		return r.listFromAny(items)
	}
	out, err := r.scalarFromAny(v)
	if err != nil {
		return nil, err
	}
	return out, r.check(out)
}

func (r *rule) listFromAny(items []any) (any, error) {
	if r.item.typ == TypeInt {
		out := make([]int64, 0, len(items))
		for i, it := range items {
			v, err := r.item.fromAny(it)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			out = append(out, v.(int64))
		}
		return out, nil
	}
	out := make([]string, 0, len(items))
	for i, it := range items {
		v, err := r.item.fromAny(it)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		out = append(out, v.(string))
	}
	return out, nil
}

func (r *rule) scalarFromAny(v any) (any, error) {
	switch r.typ {
	case TypeBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("must be a boolean, got %s", jsonx.Kind(v))
	case TypeInt:
		i, ok := jsonx.Int(v)
		if !ok {
			return nil, fmt.Errorf("must be an integer, got %s", jsonx.Kind(v))
		}
		return i, nil
	case TypeFloat:
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("must be a number, got %s", jsonx.Kind(v))
		}
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("must be a finite number, got %s", n)
		}
		return f, nil
	case TypeDuration:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a duration string such as \"5s\", got %s", jsonx.Kind(v))
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("must be a duration such as \"5s\", got %q", s)
		}
		return d, nil
	default: // string, enum
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string, got %s", jsonx.Kind(v))
		}
		return s, nil
	}
}

func (r *rule) check(v any) error {
	switch x := v.(type) {
	case int64:
		if slices.Contains(r.notIn, x) {
			return fmt.Errorf("must not be any of %v, got %d", r.notIn, x)
		}
		return checkRange(x, r.min, r.max)
	case float64:
		return checkRange(x, r.min, r.max)
	case time.Duration:
		return checkRange(x, r.min, r.max)
	case string:
		if r.enum != nil && !r.enum[x] {
			return fmt.Errorf("must be one of %v, got %s", slices.Sorted(maps.Keys(r.enum)), r.show(x))
		}
		if r.pattern != nil && !r.pattern.MatchString(x) {
			return fmt.Errorf("must match %s, got %s", r.pattern, r.show(x))
		}
		if r.format != nil {
			return r.format(x)
		}
	}
	return nil
}

type ordered interface {
	~int64 | ~float64
}

func checkRange[T ordered](v T, minV, maxV any) error {
	if lo, ok := minV.(T); ok && v < lo {
		return fmt.Errorf("must be ≥ %v, got %v", lo, v)
	}
	if hi, ok := maxV.(T); ok && v > hi {
		return fmt.Errorf("must be ≤ %v, got %v", hi, v)
	}
	return nil
}

// fromEnv converts an environment string into the JSON-decoded form and
// validates it through the same path as settings.json values.
func (r *rule) fromEnv(s string) (any, error) {
	switch r.typ {
	case TypeBool:
		switch s {
		case "true":
			return r.fromAny(true)
		case "false":
			return r.fromAny(false)
		}
		return nil, fmt.Errorf("must be true or false, got %q", s)
	case TypeInt, TypeFloat:
		return r.fromAny(json.Number(s))
	case TypeList:
		if len(s) == 0 || s[0] != '[' {
			return nil, fmt.Errorf("must be a JSON array such as [\"a\",\"b\"], got %q", s)
		}
		return r.fromJSON(json.RawMessage(s))
	default:
		return r.fromAny(s)
	}
}

// show quotes a value for an error message unless the setting is secret.
func (r *rule) show(v string) string {
	if r.secret {
		return SecretMask
	}
	return fmt.Sprintf("%q", v)
}
