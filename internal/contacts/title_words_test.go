package contacts

import "testing"

// TestTitleWordsAreFoldIntoStopNameWords: the title table is the one role
// vocabulary; the extra stop table must not repeat it, and every title word is
// a stop word.
func TestTitleWordsAreFoldedIntoStopNameWords(t *testing.T) {
	for word := range titleWords {
		if !stopNameWords[word] {
			t.Errorf("the title word %q is not a stop word", word)
		}
	}
	for word := range extraStopNameWords {
		if titleWords[word] {
			t.Errorf("the extra stop word %q duplicates titleWords", word)
		}
	}
}
