package mail

import "testing"

// Regression (review L0, rules plausible: Greek question marks): U+037E
// GREEK QUESTION MARK is canonically ";" (NFC writes it so, and Greek
// writers type it): a ";" after a Greek letter is a question mark, a ";"
// after any other letter (or after a digit or other punctuation) is not.
func TestASemicolonAfterAGreekLetterIsAQuestionMark(t *testing.T) {
	for text, want := range map[string]int{
		"Τι ώρα είναι;":          1,
		"Τι ώρα είναι;":          1,
		"Πότε θα έρθεις ; Αύριο": 1, // white space before the mark
		"Liste : a; b; c":        0,
		"Θέμα 5;":                0, // a digit: no Greek context
		"ναι?":                   1,
	} {
		if n := QuestionMarks(text); n != want {
			t.Errorf("%q: %d, want %d", text, n, want)
		}
	}
}
