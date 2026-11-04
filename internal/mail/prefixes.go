package mail

import (
	_ "embed"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed subject_prefixes.json
var prefixesJSON []byte

// shippedPrefixes is subject_prefixes.json (generated from a pinned
// Wikipedia revision by scripts/update-prefixes.sh, plus hand-kept extras;
// a broken file is a build defect).
var shippedPrefixes = sync.OnceValue(func() []string {
	var doc struct {
		Doc       string   `json:"_doc"`
		Source    string   `json:"_source"`
		Wikipedia []string `json:"wikipedia"`
		Extras    []struct {
			Prefix string `json:"prefix"`
			Note   string `json:"note"`
		} `json:"extras"`
	}
	if err := jsonx.DecodeStrict(prefixesJSON, &doc); err != nil {
		panic("mail: subject_prefixes.json: " + err.Error())
	}
	out := slices.Clone(doc.Wikipedia)
	for _, e := range doc.Extras {
		out = append(out, e.Prefix)
	}
	return out
})

// SubjectPrefixes is the shipped reply/forward prefix list plus the
// user's additions (mail.subject.prefixes): additions never replace it.
func SubjectPrefixes(added []string) []string {
	return append(slices.Clone(shippedPrefixes()), added...)
}
