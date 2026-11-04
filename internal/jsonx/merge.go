package jsonx

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MergeObject overlays over onto base key by key: a null value removes the
// key, any other value replaces it in place (new keys are appended). Deep
// lists the keys whose object values merge one level further the same way
// instead of being replaced; a deep entry may be a dotted path (each
// segment a key or "*" for any key) to merge further down, e.g.
// "providers", "providers.*", "providers.*.retry": providers per name,
// each provider per field, its retry per field. base is not modified.
func MergeObject(base, over Object, deep ...string) Object {
	out := append(Object(nil), base...)
	for _, m := range over {
		if m.Value == nil {
			out = out.Delete(m.Key)
			continue
		}
		cur, has := out.Get(m.Key)
		curObj, curOK := cur.(Object)
		overObj, overOK := m.Value.(Object)
		if sub, ok := deeper(deep, m.Key); has && curOK && overOK && ok {
			out = out.Set(m.Key, MergeObject(curObj, overObj, sub...))
			continue
		}
		out = out.Set(m.Key, m.Value)
	}
	return out
}

// deeper is whether key merges deeper under the deep paths, and the paths
// below it.
func deeper(deep []string, key string) ([]string, bool) {
	var sub []string
	ok := false
	for _, d := range deep {
		head, rest, more := strings.Cut(d, ".")
		if head != key && head != "*" {
			continue
		}
		ok = true
		if more {
			sub = append(sub, rest)
		}
	}
	return sub, ok
}

// ErrNotObject marks a document that is not a JSON object.
var ErrNotObject = errors.New("must be a JSON object")

// Overlay is shipped data with an override document merged over it
// (MergeObject with deep), both JSON objects; the result is compact JSON.
// The one implementation of "shipped data + the user's override"
// (config.Overlay reads the user's file into it).
func Overlay(shipped, over []byte, deep ...string) ([]byte, error) {
	base, err := DecodeOrdered(shipped)
	bo, ok := base.(Object)
	if err != nil || !ok {
		return nil, fmt.Errorf("the shipped data %w (%v)", ErrNotObject, err)
	}
	o, err := DecodeOrdered(over)
	ob, ok := o.(Object)
	if err != nil || !ok {
		return nil, fmt.Errorf("the override %w (%v)", ErrNotObject, err)
	}
	return json.Marshal(MergeObject(bo, ob, deep...))
}
