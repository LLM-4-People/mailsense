package triage

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	msg "github.com/LLM-4-People/mailsense/internal/mail"
)

// Facts are structural measurements of a message's own text (the cleaned
// body: quotes, forwarded headers and signatures removed): never intent,
// never word lists. Script-aware: in scripts written without spaces a
// word count means nothing, so the length is in characters; without
// cased letters there are no all-caps words. Inapplicable facts are
// omitted, never guessed.
type Facts struct {
	IsReply        bool   `json:"is_reply"`
	To             int    `json:"to"`
	Cc             int    `json:"cc"`
	HasAttachments bool   `json:"has_attachments"`
	Links          int    `json:"links"`
	QuestionMarks  int    `json:"question_marks"`
	Chars          int    `json:"chars"`
	Words          int    `json:"words"` // -1: not applicable (spaceless script)
	Sentences      int    `json:"sentences"`
	CapsWords      int    `json:"caps_words"`        // -1: not applicable (uncased script)
	Arrived        string `json:"arrived,omitempty"` // in the sender's own offset (Date header); "" when unknown
	weekday, hour  string
	offset         string
}

// measure computes the facts of a message from its own text (body: the
// cleaned body, already cut to the state's length).
func measure(m *msg.Message, body string, norm *msg.SubjectNormalizer, r *Rules) Facts {
	f := Facts{IsReply: norm.HasPrefix(m.Subject), HasAttachments: m.HasAttachments, Links: msg.Links(body),
		QuestionMarks: msg.QuestionMarks(m.Subject + "\n" + body), Chars: msg.Runes(body), Sentences: sentences(body)}
	p := msg.Scripts(body)
	f.Words, f.CapsWords = len(msg.Words(body)), capsWords(body, r.Facts.CapsMinLetters)
	if p.SpacelessShare() > r.Facts.SpacelessShare {
		f.Words = -1
	}
	if p.Letters > 0 && float64(p.Cased) < r.Facts.CasedShare*float64(p.Letters) {
		f.CapsWords = -1
	}
	for _, rc := range m.Recipients {
		switch rc.Kind {
		case msg.To:
			f.To++
		case msg.Cc:
			f.Cc++
		}
	}
	if t, ok := arrival(m); ok {
		f.Arrived = t.Format(time.RFC3339)
		f.weekday, f.hour, f.offset = t.Weekday().String(), t.Format("15"), t.Format("-07:00")
	}
	return f
}

// arrival is the message's instant in its sender's own UTC offset (the
// Date header's, mail.Message.AuthorInstant): stable (no setting changes
// it) and causal (the message carries it). Unknown without a Date header.
func arrival(m *msg.Message) (time.Time, bool) { return m.AuthorInstant() }

// sentences counts runs of text ended by Unicode sentence terminals
// (property Sentence_Terminal: . ! ? 。 ！ ？ । ؟ …), closing punctuation
// and spaces after a terminal belonging to it.
func sentences(s string) int {
	n, inText, ended := 0, false, false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Sentence_Terminal, r):
			if inText {
				ended = true
			}
		case ended && (msg.IsSpace(r) || unicode.In(r, unicode.Pe, unicode.Pf, unicode.Po) && !unicode.Is(unicode.Sentence_Terminal, r)):
		case msg.IsSpace(r):
		default:
			if !inText || ended {
				n++
				inText, ended = true, false
			}
		}
	}
	return n
}

// capsWords counts words of at least minLetters letters (rules.json
// facts.caps_min_letters) written entirely in upper case, in any cased
// script (Latin, Greek, Cyrillic, Armenian…).
func capsWords(s string, minLetters int) int {
	n := 0
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !msg.IsWordRune(r) }) {
		letters, upper := 0, 0
		for _, r := range w {
			if unicode.IsLetter(r) {
				letters++
				if unicode.IsUpper(r) {
					upper++
				}
			}
		}
		if letters >= minLetters && upper == letters {
			n++
		}
	}
	return n
}

// render writes the facts block. With evidence (triage.facts.evidence
// on), the facts the evidence engine owns — reply, recipients,
// attachments, question marks, links, characters — come from it alone
// (Composer appends its lines): only the shape of the text (words,
// sentences, capitals) and the arrival are written here.
func (f Facts) render(r *Rules, evidence bool) string {
	var lines []string
	add := func(name string, v map[string]string) { lines = append(lines, "- "+r.phrase(name, v)) }
	if !evidence {
		if f.IsReply {
			add("fact_thread", nil)
		}
		if f.To+f.Cc > 1 {
			add("fact_recipients", map[string]string{"to": strconv.Itoa(f.To), "cc": strconv.Itoa(f.Cc)})
		}
		if f.HasAttachments {
			add("fact_attachments", nil)
		}
		if f.QuestionMarks > 0 {
			add("fact_questions", map[string]string{"n": strconv.Itoa(f.QuestionMarks)})
		}
		if f.Links > 0 {
			add("fact_links", map[string]string{"n": strconv.Itoa(f.Links)})
		}
	}
	if f.CapsWords > 0 {
		add("fact_caps", map[string]string{"n": strconv.Itoa(f.CapsWords)})
	}
	shape := map[string]string{"words": strconv.Itoa(f.Words), "sentences": strconv.Itoa(f.Sentences), "chars": strconv.Itoa(f.Chars)}
	switch {
	case evidence && f.Words >= 0:
		add("fact_shape", shape)
	case evidence:
		add("fact_shape_sentences", shape)
	case f.Words >= 0:
		add("fact_length", shape)
	default:
		add("fact_length_chars", shape)
	}
	if f.weekday != "" {
		add("fact_arrival", map[string]string{"weekday": f.weekday, "hour": f.hour, "offset": f.offset})
	}
	return r.phrase("facts", map[string]string{"lines": strings.Join(lines, "\n")})
}
