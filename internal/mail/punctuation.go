package mail

import (
	_ "embed"
	"slices"
	"sync"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed punctuation.json
var punctuationJSON []byte

// questionMarkData is punctuation.json (generated from the pinned UCD by
// scripts/update-unicode-data.sh; a broken file is a build defect).
type questionMarkData struct {
	marks       map[rune]bool
	afterScript map[rune][]string // a question mark after a letter of these scripts (U+003B after Greek)
}

var questionMarks = sync.OnceValue(func() questionMarkData {
	var doc struct {
		Doc           string              `json:"_doc"`
		Source        string              `json:"_source"`
		QuestionMarks []string            `json:"question_marks"`
		AfterScript   map[string][]string `json:"question_marks_after_script"`
	}
	if err := jsonx.DecodeStrict(punctuationJSON, &doc); err != nil {
		panic("mail: punctuation.json: " + err.Error())
	}
	one := func(q string) rune {
		r := []rune(q)
		if len(r) != 1 {
			panic("mail: punctuation.json: " + q + " is not one character")
		}
		return r[0]
	}
	out := questionMarkData{marks: map[rune]bool{}, afterScript: map[rune][]string{}}
	for _, q := range doc.QuestionMarks {
		out.marks[one(q)] = true
	}
	for q, scripts := range doc.AfterScript {
		out.afterScript[one(q)] = scripts
	}
	return out
})

// QuestionMarks counts the question marks of every script in s (the
// characters named QUESTION MARK with Terminal_Punctuation: ? ？ ؟ ; ፧ …),
// and the characters that are one after a letter of a given script (the
// Greek question mark is canonically a semicolon: ";" after a Greek
// letter), the letter read past white space and marks.
func QuestionMarks(s string) int {
	q, n := questionMarks(), 0
	var last rune // the last letter seen
	for _, r := range s {
		switch {
		case q.marks[r]:
			n++
		case len(q.afterScript[r]) > 0 && last != 0 && slices.Contains(q.afterScript[r], ScriptOf(last)):
			n++
		}
		if unicode.IsLetter(r) {
			last = r
		} else if !unicode.IsMark(r) && !IsSpace(r) {
			last = 0 // other punctuation, a digit: the context is lost
		}
	}
	return n
}
