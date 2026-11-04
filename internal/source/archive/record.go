package archive

import (
	"errors"
	"fmt"
	"sort"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// record is one validated legacy record: every string field (absent → "")
// and every bool field (absent → false).
type record struct {
	str  map[string]string
	flag map[string]bool
}

// rawRecord parses one JSONL line into a JSON object.
func rawRecord(line []byte) (map[string]any, error) {
	v, err := jsonx.DecodeOne(line)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected an object, got %s", jsonx.Kind(v))
	}
	return obj, nil
}

// claimedFolder returns the legacy folder value a raw record claims
// (default inbox); ok is false when it is not a known folder value.
func claimedFolder(obj map[string]any) (string, bool) {
	v, present := obj["folder"]
	if !present || v == nil {
		return legacyInbox, true
	}
	s, isString := v.(string)
	switch {
	case !isString:
		return "", false
	case s == "":
		return legacyInbox, true
	case s == legacyInbox || s == legacySent:
		return s, true
	}
	return "", false
}

// validate applies from_record and reports every problem at once.
func validate(obj map[string]any) (record, error) {
	rec := record{str: map[string]string{}, flag: map[string]bool{}}
	var errs []error
	var unknown []string
	for name := range obj {
		if _, ok := legacyByName[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errs = append(errs, fmt.Errorf("unknown fields: %v", unknown))
	}
	for _, f := range legacyFields {
		v, present := obj[f.name]
		if !present {
			if f.required {
				errs = append(errs, fmt.Errorf("missing field %q", f.name))
			}
			continue
		}
		switch f.kind {
		case kindBool:
			b, ok := v.(bool)
			if !ok {
				errs = append(errs, fmt.Errorf("field %q must be a boolean, got %s", f.name, jsonx.Kind(v)))
			}
			rec.flag[f.name] = b
		case kindString:
			switch s := v.(type) {
			case nil:
				rec.str[f.name] = ""
			case string:
				rec.str[f.name] = s
			default:
				errs = append(errs, fmt.Errorf("field %q must be a string, got %s", f.name, jsonx.Kind(v)))
			}
		}
	}
	return rec, errors.Join(errs...)
}
