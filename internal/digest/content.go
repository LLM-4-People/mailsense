package digest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Content is the digest of what shipped JSON data files MEAN (Of over a
// canonical form of each part): every part is decoded, its top-level keys
// beginning with "_" (documentation and provenance: "_doc", "_source")
// are dropped, and the rest is encoded again with sorted keys, no spacing
// and numbers as written. Editing a description or reformatting a file
// changes nothing; changing any value does. A part that is not one JSON
// value panics: shipped data is checked by its package's tests, so this is
// a build defect, never a runtime input.
func Content(parts ...[]byte) string {
	canon := make([][]byte, len(parts))
	for i, p := range parts {
		c, err := canonical(p)
		if err != nil {
			panic(fmt.Sprintf("digest: data part %d: %v", i, err))
		}
		canon[i] = c
	}
	return Of(canon...)
}

func canonical(raw []byte) ([]byte, error) {
	// jsonx.DecodeOne rejects trailing data strictly. Decoder.More alone
	// accepted a stray closing bracket after the value.
	v, err := jsonx.DecodeOne(raw)
	if err != nil {
		return nil, err
	}
	if obj, ok := v.(map[string]any); ok {
		for k := range obj {
			if strings.HasPrefix(k, "_") {
				delete(obj, k)
			}
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // map keys sorted
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
