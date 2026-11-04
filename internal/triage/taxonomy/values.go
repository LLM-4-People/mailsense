package taxonomy

import (
	"encoding/json"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// The helpers read untyped document values with the legacy type rules
// (a boolean is never a number; an integer has no fraction or exponent).

func numOf(v any) (float64, bool) { return jsonx.Finite(v) }

func objectOf(doc jsonx.Object, key string) (jsonx.Object, bool) {
	v, ok := doc.Get(key)
	if !ok {
		return jsonx.Object{}, true // absent sections are empty
	}
	o, isObj := v.(jsonx.Object)
	return o, isObj
}

func get(o jsonx.Object, key string) any {
	v, _ := o.Get(key)
	return v
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return jsonx.Kind(v)
	}
	return string(b)
}
