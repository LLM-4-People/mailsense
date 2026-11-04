package mail

// Details are the fields a connector may read in a second, slower pass
// (Outlook: one item open per message) after delivering a Partial message.
// This type is the one list of those fields.
//
// It also carries the fields a cheaper first pass may have cut (Outlook
// tables return at most 255 characters per string): the detail pass
// rewrites them in full.
//
// BodyHTML is raw, display-only HTML. Like Message's HTML fields it is
// excluded from JSON and sanitized by internal/render before a client sees
// it.
type Details struct {
	Body              string      `json:"body"`
	BodyTruncated     bool        `json:"body_truncated"`
	BodyHTML          string      `json:"-"`
	BodyHTMLKnown     bool        `json:"-"`
	BodyHTMLTruncated bool        `json:"-"`
	Headers           string      `json:"headers"`
	References        string      `json:"references"`
	LocalCommitTime   string      `json:"local_commit_time"` // Outlook tables cannot read it
	Recipients        []Recipient `json:"recipients"`
	// Attachments is the message's complete attachment list: a connector
	// that cannot read every part fails the detail read instead of
	// returning a prefix (the store replaces the stored rows with it).
	Attachments       []Attachment `json:"attachments"`
	Subject           string       `json:"subject"`
	SenderName        string       `json:"sender_name"`
	Categories        string       `json:"categories"`
	InternetMessageID string       `json:"internet_message_id"`
	InReplyTo         string       `json:"in_reply_to"`
	ConversationIndex string       `json:"conversation_index"`
	FlagRequest       string       `json:"flag_request"`
	// Alterations are the ones the detail read declared (a header block
	// cut at mail.headers.max_chars…): for a source that delivers Partial
	// messages they are the message's alterations, replaced by each read.
	Alterations []Alteration `json:"alterations"`
}

// Details returns the detail fields of m.
func (m *Message) Details() Details {
	return Details{Body: m.Body, BodyTruncated: m.BodyTruncated,
		BodyHTML: m.BodyHTML, BodyHTMLKnown: m.BodyHTMLKnown, BodyHTMLTruncated: m.BodyHTMLTruncated,
		Headers: m.Headers, References: m.References,
		LocalCommitTime: m.LocalCommitTime, Recipients: m.Recipients, Attachments: m.Attachments, Subject: m.Subject, SenderName: m.SenderName,
		Categories: m.Categories, InternetMessageID: m.InternetMessageID, InReplyTo: m.InReplyTo,
		ConversationIndex: m.ConversationIndex, FlagRequest: m.FlagRequest, Alterations: m.Alterations}
}

// SetDetails sets the detail fields of m. It does not change Partial.
func (m *Message) SetDetails(d Details) {
	m.Body, m.BodyTruncated, m.Headers, m.References = d.Body, d.BodyTruncated, d.Headers, d.References
	m.BodyHTML, m.BodyHTMLKnown, m.BodyHTMLTruncated = d.BodyHTML, d.BodyHTMLKnown, d.BodyHTMLTruncated
	m.LocalCommitTime, m.Recipients, m.Attachments, m.Subject, m.SenderName = d.LocalCommitTime, d.Recipients, d.Attachments, d.Subject, d.SenderName
	m.Categories, m.InternetMessageID, m.InReplyTo = d.Categories, d.InternetMessageID, d.InReplyTo
	m.ConversationIndex, m.FlagRequest, m.Alterations = d.ConversationIndex, d.FlagRequest, d.Alterations
}

// Complete sets the detail fields of m and marks it complete.
func (m *Message) Complete(d Details) {
	m.SetDetails(d)
	m.Partial = false
}

// MergeStored applies the write rule for a Partial message m whose key is
// already stored as old: the stored details stay visible, and the message
// stays complete when old was complete and the source's change stamp did
// not move (any change re-reads the details: cheap and never stale).
func (m *Message) MergeStored(old *Message) {
	if !m.Partial {
		return
	}
	m.SetDetails(old.Details())
	if !old.Partial && old.Stamp != "" && old.Stamp == m.Stamp {
		m.Partial = false
	}
}
