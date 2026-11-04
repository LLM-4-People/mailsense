package jsonx

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeOne(t *testing.T) {
	v, err := DecodeOne([]byte(` {"a": 1.5} `))
	if err != nil || v.(map[string]any)["a"] != json.Number("1.5") {
		t.Fatalf("%v %v", v, err)
	}
	for _, bad := range []string{"", "{", "{} {}", "1 2", "nul"} {
		if _, err := DecodeOne([]byte(bad)); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	for v, want := range map[any]string{nil: "null", true: "boolean", json.Number("1"): "number", 1.0: "number", "s": "string", 3: "int"} {
		if Kind(v) != want {
			t.Errorf("Kind(%v) = %s", v, Kind(v))
		}
	}
	if Kind([]any{}) != "list" || Kind(map[string]any{}) != "object" {
		t.Fatal("containers")
	}
}

// Regression (phase 2a): json.Decoder.More is false before a stray '}' or
// ']', so `{} }` decoded as a valid document.
func TestTrailingClosingBracketIsRejected(t *testing.T) {
	for _, bad := range []string{"{} }", "[] ]", `{"a":1}]`} {
		if _, err := DecodeOne([]byte(bad)); err == nil {
			t.Errorf("DecodeOne(%q) must fail", bad)
		}
		if _, err := DecodeOrdered([]byte(bad)); err == nil {
			t.Errorf("DecodeOrdered(%q) must fail", bad)
		}
	}
}

func TestDecodeOrderedKeepsOrderAndRejectsDuplicates(t *testing.T) {
	v, err := DecodeOrdered([]byte(`{"z": 1, "a": {"y": null, "b": [true, "s"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	o := v.(Object)
	if got := o.Keys(); len(got) != 2 || got[0] != "z" || got[1] != "a" {
		t.Fatalf("order lost: %v", got)
	}
	inner, _ := o.Get("a")
	if keys := inner.(Object).Keys(); keys[0] != "y" || keys[1] != "b" {
		t.Fatalf("inner order lost: %v", keys)
	}
	b, _ := json.Marshal(o)
	if string(b) != `{"z":1,"a":{"y":null,"b":[true,"s"]}}` {
		t.Fatalf("marshal: %s", b)
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{`, `[1,`, ``} {
		if _, err := DecodeOrdered([]byte(bad)); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	if Kind(Object{}) != "object" {
		t.Fatal("Object kind")
	}
}

func TestMergeObject(t *testing.T) {
	base, _ := DecodeOrdered([]byte(`{"a": 1, "q": {"x": 1, "y": 2}, "r": {"k": 1}}`))
	over, _ := DecodeOrdered([]byte(`{"q": {"y": null, "z": 3}, "r": {"j": 2}, "a": null, "n": 4}`))
	got := MergeObject(base.(Object), over.(Object), "q")
	b, _ := json.Marshal(got)
	if string(b) != `{"q":{"x":1,"z":3},"r":{"j":2},"n":4}` {
		t.Fatalf("merge: %s", b)
	}
	if b, _ := json.Marshal(base); string(b) != `{"a":1,"q":{"x":1,"y":2},"r":{"k":1}}` {
		t.Fatalf("base modified: %s", b)
	}
}

func TestPyDumpsMatchesPython(t *testing.T) {
	// Expected strings produced by CPython 3 json.dumps(v, sort_keys=True, ensure_ascii=True).
	v, _ := DecodeOrdered([]byte(`{"b": ["é — x", null, true], "a": {"\u007f": "tab\there \"q\" \\ /", "Z": "😀\u0001"}}`))
	got, err := PyDumps(v)
	want := strings.ReplaceAll(`{"a": {"Z": "%ud83d%ude00%u0001", "%u007f": "tab\there \"q\" \\ /"}, "b": ["%u00e9 %u2014 x", null, true]}`, "%u", `\u`)
	if err != nil || string(got) != want {
		t.Fatalf("got  %s\nwant %s (%v)", got, want, err)
	}
	if _, err := PyDumps(map[string]any{}); err == nil {
		t.Fatal("unordered maps must be refused (key order is part of the hash)")
	}
	if b, _ := PyDumps([]any{}); string(b) != "[]" {
		t.Fatal(string(b))
	}
}

func TestFinite(t *testing.T) {
	for _, c := range []struct {
		v  any
		ok bool
	}{{json.Number("0.5"), true}, {json.Number("1e400"), false}, {1.0, true}, {true, false}, {"0.5", false}, {nil, false}, {json.Number("x"), false}} {
		if _, ok := Finite(c.v); ok != c.ok {
			t.Errorf("Finite(%#v) ok=%v", c.v, ok)
		}
	}
}

func TestIntAndDecodeStrict(t *testing.T) {
	for v, ok := range map[any]bool{json.Number("3"): true, json.Number("-2"): true, json.Number("1.0"): false, json.Number("1e3"): false,
		3.0: false, true: false, "3": false} {
		if _, got := Int(v); got != ok {
			t.Errorf("Int(%#v) = %v", v, got)
		}
	}
	var dst struct{ A int }
	if err := DecodeStrict([]byte(`{"A": 1}`), &dst); err != nil || dst.A != 1 {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"A": 1} }`, `{"B": 1}`, `{"A": 1}{}`, ``} {
		if err := DecodeStrict([]byte(bad), &dst); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMergeObjectDeepPaths(t *testing.T) {
	base, _ := DecodeOrdered([]byte(`{"providers": {"a": {"x": 1, "retry": {"n": 1, "m": 2}}, "b": {"x": 2}}, "probe": {"s": 1}}`))
	over, _ := DecodeOrdered([]byte(`{"providers": {"a": {"retry": {"n": 5}}, "b": null, "c": {"x": 3}}, "probe": {"t": 2}}`))
	got := MergeObject(base.(Object), over.(Object), "providers", "providers.*", "providers.*.retry")
	b, _ := json.Marshal(got)
	if string(b) != `{"providers":{"a":{"x":1,"retry":{"n":5,"m":2}},"c":{"x":3}},"probe":{"t":2}}` {
		t.Fatalf("%s", b)
	}
	if _, err := Overlay([]byte(`[1]`), []byte(`{}`)); err == nil {
		t.Fatal("shipped data not an object")
	}
	if _, err := Overlay([]byte(`{}`), []byte(`"x"`)); err == nil {
		t.Fatal("override not an object")
	}
	if b, err := Overlay([]byte(`{"a": {"x": 1}}`), []byte(`{"a": {"y": 2}}`), "a"); err != nil || string(b) != `{"a":{"x":1,"y":2}}` {
		t.Fatalf("%s %v", b, err)
	}
}
