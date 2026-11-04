// Package threads turns stored mail into conversations: header-first
// threading (Message-ID / In-Reply-To / References, then ConversationID /
// ConversationIndex, then the normalized subject only when it is not
// empty), reply matching in both directions (the owner's replies use the
// sent item's sent_on), and the owner's behaviour profile per sender and
// domain computed in the owner's time zone. Pure except the store read in
// load.go.
package threads

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// threadFields is the mail.Message projection of Load: exactly the columns
// FromMail and the helpers it calls read, plus recipients. KindOf reads the
// message class, headers and calendar method; ThreadIDs reads the
// message-id headers and falls back to the headers; Instant reads received
// and sent times. The identity EachMessage always loads (row id, revision,
// source_id, store_id, folder role, store kind, verdict and reason) is
// outside the projection.
var threadFields = store.MessageFieldsOf(
	"sender_address", "subject", "conversation_id", "conversation_index", "body", "recipients",
	"message_class", "headers", "calendar_method", "received_at", "sent_on",
	"internet_message_id", "in_reply_to", "references",
)

// Message is the thread-relevant view of one owner-scope message.
type Message struct {
	RowID int64  `json:"row_id"`
	Store string `json:"store"` // threads never cross mailbox stores
	// Role is the direction, wherever the message is filed: RoleSent for
	// the owner's own message (scope: sent by the owner), RoleInbox for
	// mail the owner received.
	Role       mail.FolderRole `json:"role"`
	Kind       mail.Kind       `json:"kind"` // only ordinary messages reply or wait for one
	Sender     string          `json:"sender"`
	Domain     string          `json:"domain"`     // organisation key of the sender (mail.Domains; "" = none or shared)
	Recipients []string        `json:"recipients"` // To and Cc, normalized
	Subject    string          `json:"subject"`
	// At is when the message arrived (inbox: received_at) or left (sent:
	// sent_on, else received_at); zero when unknown.
	At                time.Time `json:"at"`
	ConversationID    string    `json:"conversation_id"`
	ConversationIndex string    `json:"conversation_index"`
	SelfID            string    `json:"self_id"`
	Parents           []string  `json:"parents"`
	Excerpt           string    `json:"excerpt"`
	Words             int       `json:"words"` // full body (Python \S+)
}

// FromMail extracts the view of a stored message; outgoing is true for
// the owner's own message.
func FromMail(rowID int64, m *mail.Message, outgoing bool, excerptChars int, domains mail.Domains) Message {
	role := mail.RoleInbox
	if outgoing {
		role = mail.RoleSent
	}
	out := Message{RowID: rowID, Store: m.SourceID + "\x00" + m.StoreID, Role: role, Kind: mail.KindOf(m),
		Sender: mail.NormalizeAddress(m.SenderAddress), Domain: domains.Of(m.SenderAddress), Subject: m.Subject,
		ConversationID:    m.ConversationID,
		ConversationIndex: m.ConversationIndex, Excerpt: mail.Excerpt(m.Body, excerptChars), Words: mail.WordCount(m.Body)}
	for _, r := range m.Recipients {
		if r.Kind != mail.Bcc {
			if a := mail.NormalizeAddress(r.Address); a != "" {
				out.Recipients = append(out.Recipients, a)
			}
		}
	}
	if t, ok := m.Instant(); ok {
		out.At = t
	}
	out.SelfID, out.Parents = mail.ThreadIDs(m)
	return out
}

// Dated reports whether the message has a usable time.
func (m *Message) Dated() bool { return !m.At.IsZero() }

func (m *Message) addressedTo(addr string) bool {
	for _, r := range m.Recipients {
		if r == addr {
			return true
		}
	}
	return false
}
