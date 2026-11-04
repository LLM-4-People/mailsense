package evidence

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/goldset"
)

// Test support shared by the fixture and the live measurements of the
// rules on the gold sample (train and dev splits only).

// messageKey is the key a gold label follows a message by across stores:
// a digest of its Internet Message-ID ("" without one).
func messageKey(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	return digest.Hex([]byte(id), 16)
}

// goldLabels reads the train and dev labels of a sealed gold sample
// (goldset: the one loader; a directory whose test split is not sealed is
// refused) and the labelled ids per split.
func goldLabels(t *testing.T, dir string) (map[string]goldset.Answers, map[string]int) {
	s, err := goldset.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s.Labels, s.Used()
}

func TestMessageKeys(t *testing.T) {
	if messageKey(" <A@x> ") != messageKey("<a@x>") || messageKey("") != "" {
		t.Error("message keys")
	}
}
