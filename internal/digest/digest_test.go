package digest

import "testing"

func TestHex(t *testing.T) {
	if Hex([]byte("abc"), 12) != "ba7816bf8f01" || len(Hex(nil, 0)) != 64 || len(Hex(nil, 99)) != 64 {
		t.Fatal(Hex([]byte("abc"), 12))
	}
}

// Review of the crash-safety round, #9: any byte of a data file (its
// _doc too) changed the data digest, so a documentation edit recomputed
// every view versioned with it — and the triage states built on them.
// Content digests the meaning only.
func TestContentDigestsTheMeaningOnly(t *testing.T) {
	base := Content([]byte(`{"_doc": "limits", "_source": "pin 1", "a": 1, "b": [1, 2], "c": {"_x": 1}}`))
	for name, same := range map[string]string{
		"doc edited":  `{"_doc": "limits, explained better", "_source": "pin 1", "a": 1, "b": [1, 2], "c": {"_x": 1}}`,
		"provenance":  `{"_doc": "limits", "_source": "pin 2", "a": 1, "b": [1, 2], "c": {"_x": 1}}`,
		"reformatted": "{\n  \"b\": [1,2],\n  \"a\": 1,\n  \"c\": {\"_x\": 1},\n  \"_doc\": \"limits\"\n}\n",
		"doc removed": `{"a": 1, "b": [1, 2], "c": {"_x": 1}}`,
	} {
		if got := Content([]byte(same)); got != base {
			t.Errorf("%s changed the digest", name)
		}
	}
	for name, other := range map[string]string{
		"a value":            `{"_doc": "limits", "a": 2, "b": [1, 2], "c": {"_x": 1}}`,
		"a nested _ key":     `{"_doc": "limits", "a": 1, "b": [1, 2], "c": {"_x": 2}}`,
		"an order in a list": `{"_doc": "limits", "a": 1, "b": [2, 1], "c": {"_x": 1}}`,
		"a key added":        `{"_doc": "limits", "a": 1, "b": [1, 2], "c": {"_x": 1}, "d": true}`,
	} {
		if Content([]byte(other)) == base {
			t.Errorf("%s did not change the digest", name)
		}
	}
	if Content([]byte(`{"a":1}`), []byte(`{"b":2}`)) == Content([]byte(`{"a":1,"b":2}`)) {
		t.Error("parts must stay apart")
	}
	for _, bad := range []string{"", "{", `{"a":1} {"b":2}`, `{"a":1}]`, "not json"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q must panic (a build defect)", bad)
				}
			}()
			Content([]byte(bad))
		}()
	}
}
