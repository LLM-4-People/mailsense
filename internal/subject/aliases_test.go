package subject

import (
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// phrasePath is one row's stable identity for the error messages.
func phrasePath(row Phrase) string {
	if row.Subtopic == "" {
		return row.Topic
	}
	return row.Topic + "/" + row.Subtopic
}

// TestPhraseTableMatchesTheCatalogue is the one validation of the recognition
// table against the catalogue: every topic has a whole-topic row with both
// languages, every subtopic is a real catalogue path, rows keep catalogue
// order, aliases are already folded token runs, and no exact alias maps two
// different paths.
func TestPhraseTableMatchesTheCatalogue(t *testing.T) {
	rows := Phrases()
	if len(rows) == 0 {
		t.Fatal("the phrase table is empty")
	}
	topics := Topics()
	whole := map[string]bool{}
	seenAlias := map[string]string{}
	topicAt, subAt := -1, -1
	for _, row := range rows {
		for _, alias := range append(append([]string(nil), row.EN...), row.FR...) {
			if alias == "" {
				t.Fatalf("%s: empty alias", phrasePath(row))
			}
			if mail.Fold(alias) != alias {
				t.Fatalf("%s: alias %q is not folded", phrasePath(row), alias)
			}
			if alias != strings.Join(strings.Fields(alias), " ") {
				t.Fatalf("%s: alias %q is not a single-spaced token run", phrasePath(row), alias)
			}
			for _, r := range alias {
				if r != ' ' && !mail.IsWordRune(r) {
					t.Fatalf("%s: alias %q contains a non-word rune %q", phrasePath(row), alias, r)
				}
			}
			if prior, ok := seenAlias[alias]; ok && prior != phrasePath(row) {
				t.Fatalf("alias %q maps to both %s and %s", alias, prior, phrasePath(row))
			}
			seenAlias[alias] = phrasePath(row)
		}

		ti := slices.Index(topics, row.Topic)
		if ti < 0 {
			t.Fatalf("%s: unknown topic", phrasePath(row))
		}
		if ti < topicAt {
			t.Fatalf("%s: rows are out of catalogue order", phrasePath(row))
		}
		if ti > topicAt {
			if ti != topicAt+1 {
				t.Fatalf("%s: topic %q is out of catalogue order", phrasePath(row), row.Topic)
			}
			topicAt, subAt = ti, -1
		}

		if row.Subtopic == "" {
			if whole[row.Topic] {
				t.Fatalf("%s: duplicate whole-topic row", row.Topic)
			}
			whole[row.Topic] = true
			if len(row.EN) == 0 || len(row.FR) == 0 {
				t.Fatalf("%s: a whole-topic row needs EN and FR aliases", row.Topic)
			}
			continue
		}
		if !whole[row.Topic] {
			t.Fatalf("%s: subtopic row before its whole-topic row", phrasePath(row))
		}
		if !HasSubtopic(row.Topic, row.Subtopic) {
			t.Fatalf("%s: unknown subtopic", phrasePath(row))
		}
		si := slices.Index(Subtopics(row.Topic), row.Subtopic)
		if si <= subAt {
			t.Fatalf("%s: subtopic rows are out of display order", phrasePath(row))
		}
		subAt = si
	}
	for _, topic := range topics {
		if !whole[topic] {
			t.Errorf("%s: no whole-topic row with EN and FR aliases", topic)
		}
	}
}

// TestMatchPicksTheLongestAlias is the recognition contract: the longest run
// wins inside one question, two different paths at the same length are
// ambiguous, and the matched run is returned for subtraction.
func TestMatchPicksTheLongestAlias(t *testing.T) {
	cases := []struct {
		name  string
		words []string
		topic string
		sub   string
		alias []string
		want  bool
	}{
		{"status updates beat the news word", foldWords("How many status updates?"),
			"project_work", "status_update", []string{"status", "updates"}, true},
		{"product update in French", foldWords("Combien de mises à jour produit ?"),
			"news_updates", "product_update", []string{"mises", "a", "jour", "produit"}, true},
		{"proposal is sales only", foldWords("proposal"),
			"sales_inquiry", "proposal", []string{"proposal"}, true},
		{"invoices are the invoice subtopic", foldWords("invoices"),
			"billing_account", "invoice", []string{"invoices"}, true},
		{"billing is the whole topic", foldWords("billing"),
			"billing_account", "", []string{"billing"}, true},
		{"support requests are the topic", foldWords("support requests"),
			"support_issue", "", []string{"support", "requests"}, true},
		{"projet is the topic", foldWords("projet"),
			"project_work", "", []string{"projet"}, true},
		{"two equal paths are ambiguous", foldWords("billing invoices"), "", "", nil, false},
		{"no alias", foldWords("hello there"), "", "", nil, false},
		{"empty words", nil, "", "", nil, false},
	}
	for _, tc := range cases {
		phrase, alias, ok := Match(tc.words)
		if ok != tc.want {
			t.Errorf("%s: ok = %v, want %v (phrase %s)", tc.name, ok, tc.want, phrasePath(phrase))
			continue
		}
		if !ok {
			continue
		}
		if phrase.Topic != tc.topic || phrase.Subtopic != tc.sub {
			t.Errorf("%s: path = %s, want %s/%s", tc.name, phrasePath(phrase), tc.topic, tc.sub)
		}
		if strings.Join(alias, " ") != strings.Join(tc.alias, " ") {
			t.Errorf("%s: alias = %v, want %v", tc.name, alias, tc.alias)
		}
	}
}

// TestPhrasesReturnsCopies proves a caller cannot mutate the table the
// matcher reads.
func TestPhrasesReturnsCopies(t *testing.T) {
	rows := Phrases()
	if len(rows) == 0 || len(rows[0].EN) == 0 {
		t.Fatal("empty table")
	}
	rows[0].EN[0] = "mutated"
	rows[0].Topic = "mutated"
	again := Phrases()
	if again[0].EN[0] == "mutated" || again[0].Topic == "mutated" {
		t.Fatal("Phrases handed out the live table")
	}
}

// foldWords is the folded token form the question parser passes to Match.
func foldWords(question string) []string {
	return strings.FieldsFunc(mail.Fold(question), func(r rune) bool { return !mail.IsWordRune(r) })
}
