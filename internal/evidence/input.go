package evidence

import (
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/locale"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Input is one message as the extractors see it: the message, what its
// source can report, its scope verdict, the owner of its store and the
// run's shared context; the derived views (headers, own text, language,
// dates, amounts) are computed once on first use. One goroutine uses an
// Input at a time.
type Input struct {
	Row     int64
	Msg     *mail.Message
	Caps    source.Capabilities
	Verdict scope.Verdict
	Reason  scope.Reason
	Owner   scope.Identity
	env     *Env

	headers  mail.Headers
	parsed   bool
	own      *string
	clean    *string
	sig      string
	guess    *locale.Guess
	mentions *locale.Mentions
	amounts  []locale.Amount
	amounted bool
	used     []string // pinned data the views read (tests check Signal.Data against it)
}

func newInput(r *store.MessageRow, env *Env) *Input {
	key := source.StoreKey(r.Message.SourceID, r.Message.StoreID)
	return &Input{Row: r.RowID, Msg: &r.Message, Caps: env.caps[r.Message.SourceID], Verdict: r.Verdict, Reason: r.Reason,
		Owner: env.owners[key], env: env}
}

// Env is the run's shared, read-only context.
func (in *Input) Env() *Env { return in.env }

// Header returns the values of a transport header, or a Missing value
// when the message carries no header block (a sent item: transport
// headers are added on the way, never to the sender's copy).
func (in *Input) Header(name string) ([]string, Value, bool) {
	if in.Msg.Headers == "" {
		return nil, Missing(ReasonNoHeaders), false
	}
	if !in.parsed {
		if in.HeadersCut() {
			in.headers = mail.ParseCutHeaders(in.Msg.Headers) // the cut line is no value
		} else {
			in.headers = mail.ParseHeaders(in.Msg.Headers)
		}
		in.parsed = true
	}
	return in.headers.Values(name), Value{}, true
}

// HeadersCut reports whether the source cut the header block (a header
// absent from it may lie past the cut).
func (in *Input) HeadersCut() bool {
	return slices.ContainsFunc(in.Msg.Alterations, func(a mail.Alteration) bool { return a.Code == mail.AltHeadersTruncated })
}

// Present is a header's presence: true when found; when absent, false,
// partial if the block was cut.
func (in *Input) Present(name string) Value {
	vals, missing, ok := in.Header(name)
	if !ok {
		return missing
	}
	if len(vals) > 0 {
		return Bool(true)
	}
	return Bool(false).Cut(in.HeadersCut())
}

// history is the causal history of the mailbox (threads.KnownHistory).
func (in *Input) history() *threads.History {
	in.uses(DataThreads)
	return in.env.hist
}

// uses records pinned data a view reads.
func (in *Input) uses(data string) {
	if !slices.Contains(in.used, data) {
		in.used = append(in.used, data)
	}
}

// Clean is the message's cleaned body (mail.CleanBody: quotes,
// forwarded headers and signatures removed).
func (in *Input) Clean() string {
	in.uses(DataMail)
	if in.clean == nil {
		s, sig := mail.CleanParts(in.Msg.Body)
		in.clean, in.sig = &s, sig
	}
	return *in.clean
}

// Own is the message's own text: its subject and cleaned body.
func (in *Input) Own() string {
	if in.own == nil {
		s := in.Msg.Subject + "\n" + in.Clean()
		in.own = &s
	}
	return *in.own
}

// Signature is how the own text's signature was found (mail.Signature).
func (in *Input) Signature() string {
	in.Clean()
	return in.sig
}

// BodyCut reports whether the body was cut by its source.
func (in *Input) BodyCut() bool { return in.Msg.BodyTruncated }

// Language is the language of the own text (its first
// evidence.language.max_chars characters).
func (in *Input) Language() locale.Guess {
	in.uses(DataLocale)
	if in.guess == nil {
		g := locale.Language(mail.Head(in.Own(), in.env.set.LangChars), in.env.set.MinLetters)
		in.guess = &g
	}
	return *in.guess
}

// Locale is the compiled locale of the own text's language.
func (in *Input) Locale() *locale.Locale {
	l, ok := locale.LocaleOf(in.Language().Tag)
	if !ok {
		l, _ = locale.LocaleOf(locale.Undetermined)
	}
	return l
}

// Mentions are the dates and times of the own text.
func (in *Input) Mentions() locale.Mentions {
	in.uses(DataLocale)
	if in.mentions == nil {
		m := in.Locale().Find(in.Own())
		in.mentions = &m
	}
	return *in.mentions
}

// Amounts are the amounts of money of the own text.
func (in *Input) Amounts() []locale.Amount {
	in.uses(DataLocale)
	if !in.amounted {
		in.amounts, in.amounted = in.Locale().Amounts(in.Own()), true
	}
	return in.amounts
}

// At is the message's instant (mail.Instant: sent items when they left).
func (in *Input) At() (time.Time, bool) { return in.Msg.Instant() }

// Day is the message's instant in the owner's time zone (time.zone): the
// day dates written in it are resolved against.
func (in *Input) Day() (time.Time, bool) {
	at, ok := in.At()
	return at.In(in.env.set.Zone), ok
}

// AuthorDay is the message's instant as its author wrote it: in the Date
// header's offset (mail.Message.AuthorInstant), else (the owner's own
// copy, no Date header) in the owner's zone. A weekday or a year-less date
// the author names is the author's: it is placed from this day.
func (in *Input) AuthorDay() (time.Time, bool) {
	if t, ok := in.Msg.AuthorInstant(); ok {
		return t, true
	}
	return in.Day()
}

// SentByOwner reports whether scope judged the owner its sender.
func (in *Input) SentByOwner() bool { return in.Reason == scope.ReasonSentByOwner }
