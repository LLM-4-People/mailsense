package priority

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// TestReplyOwedReadsTheOneVocabulary: an FYI never opens a reply obligation,
// even when the reply answer disagrees, and the veto reads the ask through
// threadintel.ClassifyAsk, so a differently-cased profile value is still an
// FYI instead of silently creating an obligation.
func TestReplyOwedReadsTheOneVocabulary(t *testing.T) {
	for _, c := range []struct {
		name string
		sig  Signals
		want bool
	}{
		{"reply needed", Signals{ReplyNeeded: 1, AsksFor: "answer_question"}, true},
		{"reply needed, other ask", Signals{ReplyNeeded: 1, AsksFor: "payment_money"}, true},
		{"fyi vetoes the reply answer", Signals{ReplyNeeded: 1, AsksFor: "nothing_fyi"}, false},
		{"fyi veto is case-insensitive", Signals{ReplyNeeded: 1, AsksFor: "Nothing_FYI"}, false},
		{"fyi with spaces", Signals{ReplyNeeded: 1, AsksFor: " nothing_fyi "}, false},
		{"no reply answer", Signals{ReplyNeeded: 0, AsksFor: "answer_question"}, false},
		{"nothing", Signals{}, false},
	} {
		if got := replyOwed(c.sig); got != c.want {
			t.Errorf("%s: replyOwed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAskVocabularyLivesInThreadintel: the asks_for options are spelled once,
// in ClassifyAsk. A package that compares the raw value is how one rule starts
// treating an FYI as a question.
func TestAskVocabularyLivesInThreadintel(t *testing.T) {
	for _, option := range []string{"answer_question", "nothing_fyi"} {
		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"`+option+`"`) {
				t.Errorf("%s spells %q; compare threadintel.ClassifyAsk instead", file, option)
			}
		}
	}
	data, err := os.ReadFile("../threadintel/threadintel.go")
	if err != nil {
		t.Fatal(err)
	}
	owner := string(data)
	for _, option := range []string{"answer_question", "nothing_fyi"} {
		if got := strings.Count(owner, `"`+option+`"`); got != 1 {
			t.Errorf("threadintel spells %q %d times, want once in ClassifyAsk", option, got)
		}
	}
	// The classifier is the owner the priority rule reads.
	if !strings.Contains(owner, "func ClassifyAsk(asksFor string) Ask") {
		t.Fatal("threadintel.ClassifyAsk is gone")
	}
	_ = threadintel.ClassifyAsk
}
