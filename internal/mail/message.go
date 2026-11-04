// Package mail is the source-neutral message model and its pure helpers
// (addresses, subjects, timestamps). Every connector maps into Message;
// everything downstream sees only this shape.
package mail

// Message is one mail item. Timestamps are UTC RFC3339 with Z, or "" when
// unknown (see Times). Raw numeric MAPI/OOM values are OptInt (unset ≠ 0).
type Message struct {
	// Where it lives. (SourceID, StoreID, ID) is the unique key.
	SourceID   string     `json:"source_id"`
	StoreID    string     `json:"store_id"`
	StoreName  string     `json:"store_name"`
	FolderPath string     `json:"folder_path"`
	FolderRole FolderRole `json:"folder_role"`
	// ID is the connector's stable id within the store (Outlook: the
	// long-term EntryID; legacy archive: the exported EntryID).
	ID              string `json:"id"`
	EntryID         string `json:"entry_id"`
	LongTermEntryID string `json:"long_term_entry_id"`
	SearchKey       string `json:"search_key"`
	Origin          string `json:"origin"` // system that produced the data (legacy "source": "outlook")

	Subject                 string      `json:"subject"`
	SenderName              string      `json:"sender_name"`
	SenderAddress           string      `json:"sender_address"`
	SentRepresentingAddress string      `json:"sent_representing_address"`
	Recipients              []Recipient `json:"recipients"`
	// AddressedToMe is AddressedTo/Cc/Bcc/No from the MAPI recipient-me
	// flags, "" when unread (kept as the source wrote it).
	AddressedToMe string `json:"addressed_to_me"`

	ReceivedAt            string `json:"received_at"`
	SentOn                string `json:"sent_on"`
	CreationTime          string `json:"creation_time"`
	LastModificationTime  string `json:"last_modification_time"`
	LastVerbExecutionTime string `json:"last_verb_execution_time"`
	LastVerbExecuted      OptInt `json:"last_verb_executed"` // 102 reply, 103 reply-all, 104 forward
	LocalCommitTime       string `json:"local_commit_time"`
	MessageFlags          OptInt `json:"message_flags"` // raw PidTagMessageFlags; mfEverRead = 0x400

	IsRead         bool   `json:"is_read"`
	HasAttachments bool   `json:"has_attachments"`
	Importance     OptInt `json:"importance"`  // 0–2, evidence only
	Sensitivity    OptInt `json:"sensitivity"` // 0–3
	Size           OptInt `json:"size"`
	SpamConfidence OptInt `json:"spam_confidence"` // Exchange SCL
	Categories     string `json:"categories"`
	FlagStatus     OptInt `json:"flag_status"` // 0 none, 1 complete, 2 marked
	FlagRequest    string `json:"flag_request"`
	// TaskDueDate/TaskStartDate are DATES in the user's local time zone
	// (MS-OXOTASK), stored as that date at 00:00 in the stored form; they
	// are not instants and are never converted between zones.
	TaskDueDate          string `json:"task_due_date"`
	TaskStartDate        string `json:"task_start_date"`
	ReminderSet          bool   `json:"reminder_set"`
	ReminderTime         string `json:"reminder_time"`
	ReadReceiptRequested bool   `json:"read_receipt_requested"`
	VotingOptions        string `json:"voting_options"`
	VotingResponse       string `json:"voting_response"`
	MessageClass         string `json:"message_class"`

	InternetMessageID string `json:"internet_message_id"`
	InReplyTo         string `json:"in_reply_to"`
	References        string `json:"references"`
	ConversationID    string `json:"conversation_id"`
	ConversationIndex string `json:"conversation_index"` // hex
	Headers           string `json:"headers"`

	// Body is the canonical plain text: learning, search and the model read
	// only this field. BodyHTML is the raw display alternative, captured
	// when the source has one; it is never serialized to a client (the API
	// sanitizes it through internal/render) and never indexed.
	Body              string `json:"body"`
	BodyTruncated     bool   `json:"body_truncated"`
	BodyHTML          string `json:"-"`
	BodyHTMLKnown     bool   `json:"-"`
	BodyHTMLTruncated bool   `json:"-"`
	// Attachments are the attachments' metadata (never their content);
	// sources without the capability leave it empty and set HasAttachments.
	Attachments []Attachment `json:"attachments"`

	// Folders are the further folders holding this same message (a Gmail
	// label, a copy the source reports as one item), beyond FolderPath.
	Folders []FolderRef `json:"folders"`

	// Stamp is the source's full-precision change stamp (opaque; Outlook:
	// modification time to the millisecond + message flags, because a
	// read flip moves no time). Snapshot diffs compare it; "" for sources
	// that do not snapshot.
	Stamp string `json:"stamp"`

	// Partial is true while only summary fields are known: the detail
	// fields (see Details) are still to be read from the source. A partial
	// message keeps the details already stored for it (store write path).
	Partial bool `json:"partial"`

	// CalendarMethod is the iCalendar METHOD of the message's calendar
	// part (RFC 5546: REQUEST, REPLY, CANCEL…; "" without one), for sources
	// without message classes.
	CalendarMethod string `json:"calendar_method"`
	// Alterations are the declared changes the connector made while
	// reading the message (Alteration).
	Alterations []Alteration `json:"alterations"`
}

// Attachment is one attachment. The identity and bytes of an inline part
// are display-only: they are never serialized to a client, never indexed and
// never enter learning. The bytes are kept only for image parts the message
// body can reference (see mail.inline_assets.*); every other attachment
// keeps metadata only.
type Attachment struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`   // decoded bytes, -1 when unknown
	Inline      bool   `json:"inline"` // shown in the body (an image of a signature, a calendar part)

	// ContentID and ContentLocation are the MIME identity of an inline part:
	// the HTML references it as cid:<ContentID> or as ContentLocation.
	ContentID       string `json:"-"`
	ContentLocation string `json:"-"`
	// SHA256 is the content address of Data in attachment_blobs. A read
	// attachment carries the address, never the bytes.
	SHA256 string `json:"-"`
	// Data is the decoded part at ingest time. The write path hashes it,
	// stores it once in attachment_blobs and clears it on read.
	Data []byte `json:"-"`
}

// FolderRef names one more folder of a message.
type FolderRef struct {
	Path string     `json:"path"`
	Role FolderRole `json:"role"`
}

// RecipientKind is to, cc or bcc.
type RecipientKind string

// Recipient kinds.
const (
	To  RecipientKind = "to"
	Cc  RecipientKind = "cc"
	Bcc RecipientKind = "bcc"
)

// Recipient is one addressee as recorded by the source.
type Recipient struct {
	Kind    RecipientKind `json:"kind"`
	Name    string        `json:"name"`
	Address string        `json:"address"`
}

// Values of Message.AddressedToMe (PidTagMessageToMe/CcMe/RecipientMe).
const (
	AddressedTo  = "to"
	AddressedCc  = "cc"
	AddressedBcc = "bcc"
	AddressedNo  = "no"
)

// TimestampField is one timestamp of a message, in declaration order.
type TimestampField struct {
	Name  string
	Value *string
}

// TimestampFields lists every timestamp field of m — the one list used by
// validation and by connectors that normalize source dates.
func (m *Message) TimestampFields() []TimestampField {
	return []TimestampField{
		{"received_at", &m.ReceivedAt}, {"sent_on", &m.SentOn}, {"creation_time", &m.CreationTime},
		{"last_modification_time", &m.LastModificationTime}, {"last_verb_execution_time", &m.LastVerbExecutionTime},
		{"local_commit_time", &m.LocalCommitTime}, {"task_due_date", &m.TaskDueDate},
		{"task_start_date", &m.TaskStartDate}, {"reminder_time", &m.ReminderTime},
	}
}
