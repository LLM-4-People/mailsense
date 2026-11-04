package outlook

import (
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// addressRef is how a table row names a sender: the SMTP property when
// Exchange filled it, else the e-mail property (a legacy DN for internal
// senders) and the address-book EntryID to resolve it with.
type addressRef struct {
	smtp, email, entryID string
}

// tableRow is one decoded table row before addresses are resolved.
type tableRow struct {
	msg                 mail.Message
	sender, sentRepr    addressRef
	toMe, ccMe, recipMe bool
	meKnown             bool   // PidTagMessageRecipientMe was readable
	modified            string // full-precision modification time (stamp)
	flags               mail.OptInt
}

// column is one table column and where its value goes.
type column struct {
	name string
	set  func(r *tableRow, v any, t mail.Times)
}

func str(dst func(*tableRow) *string) func(*tableRow, any, mail.Times) {
	return func(r *tableRow, v any, _ mail.Times) { *dst(r) = asString(v) }
}

func num(dst func(*tableRow) *mail.OptInt) func(*tableRow, any, mail.Times) {
	return func(r *tableRow, v any, _ mail.Times) { *dst(r) = asInt(v) }
}

func when(dst func(*tableRow) *string) func(*tableRow, any, mail.Times) {
	return func(r *tableRow, v any, t mail.Times) { *dst(r) = asTime(v, t) }
}

func day(dst func(*tableRow) *string) func(*tableRow, any, mail.Times) {
	return func(r *tableRow, v any, t mail.Times) { *dst(r) = asDate(v, t) }
}

func bin(dst func(*tableRow) *string) func(*tableRow, any, mail.Times) {
	return func(r *tableRow, v any, _ mail.Times) { *dst(r) = asHex(v) }
}

func boolean(dst func(*tableRow) *bool) func(*tableRow, any, mail.Times) {
	return func(r *tableRow, v any, _ mail.Times) { *dst(r), _ = asBool(v) }
}

// stampColumn is the UTC modification time (stored form + full precision).
var stampColumn = column{propLastModified, func(r *tableRow, v any, t mail.Times) {
	r.msg.LastModificationTime, r.modified = asTime(v, t), asExact(v)
}}

// flagsColumn is PidTagMessageFlags (read state, mfEverRead…).
var flagsColumn = column{propMessageFlags, num(func(r *tableRow) *mail.OptInt { return &r.flags })}

// idColumns are the snapshot columns: the long-term EntryID (the message
// id) and what the change stamp (rowStamp) needs. They lead the full list.
var idColumns = []column{
	{propLongTermEntryID, func(r *tableRow, v any, _ mail.Times) {
		r.msg.ID = asHex(v)
		r.msg.LongTermEntryID = r.msg.ID
	}},
	stampColumn, flagsColumn,
}

// summaryColumns are read in the full table pass (after idColumns).
var summaryColumns = []column{
	{builtinEntryID, str(func(r *tableRow) *string { return &r.msg.EntryID })},
	{propSearchKey, bin(func(r *tableRow) *string { return &r.msg.SearchKey })},
	{propMessageClass, str(func(r *tableRow) *string { return &r.msg.MessageClass })},
	{propSubject, str(func(r *tableRow) *string { return &r.msg.Subject })},
	{propSenderName, str(func(r *tableRow) *string { return &r.msg.SenderName })},
	{propSenderSMTP, str(func(r *tableRow) *string { return &r.sender.smtp })},
	{propSenderEmail, str(func(r *tableRow) *string { return &r.sender.email })},
	{propSenderEntryID, bin(func(r *tableRow) *string { return &r.sender.entryID })},
	{propSentReprSMTP, str(func(r *tableRow) *string { return &r.sentRepr.smtp })},
	{propSentReprEmail, str(func(r *tableRow) *string { return &r.sentRepr.email })},
	{propSentReprEntryID, bin(func(r *tableRow) *string { return &r.sentRepr.entryID })},
	{propToMe, boolean(func(r *tableRow) *bool { return &r.toMe })},
	{propCcMe, boolean(func(r *tableRow) *bool { return &r.ccMe })},
	{propRecipientMe, func(r *tableRow, v any, _ mail.Times) { r.recipMe, r.meKnown = asBool(v) }},
	{propDeliveryTime, when(func(r *tableRow) *string { return &r.msg.ReceivedAt })},
	{propSubmitTime, when(func(r *tableRow) *string { return &r.msg.SentOn })},
	{propCreationTime, when(func(r *tableRow) *string { return &r.msg.CreationTime })},
	{propLastVerbTime, when(func(r *tableRow) *string { return &r.msg.LastVerbExecutionTime })},
	{propLastVerb, num(func(r *tableRow) *mail.OptInt { return &r.msg.LastVerbExecuted })},
	{propHasAttach, boolean(func(r *tableRow) *bool { return &r.msg.HasAttachments })},
	{propImportance, num(func(r *tableRow) *mail.OptInt { return &r.msg.Importance })},
	{propSensitivity, num(func(r *tableRow) *mail.OptInt { return &r.msg.Sensitivity })},
	{propSize, num(func(r *tableRow) *mail.OptInt { return &r.msg.Size })},
	{propSpamConfidence, num(func(r *tableRow) *mail.OptInt { return &r.msg.SpamConfidence })},
	{propFlagStatus, num(func(r *tableRow) *mail.OptInt { return &r.msg.FlagStatus })},
	{propReadReceipt, boolean(func(r *tableRow) *bool { return &r.msg.ReadReceiptRequested })},
	{propInternetMessageID, str(func(r *tableRow) *string { return &r.msg.InternetMessageID })},
	{propInReplyTo, str(func(r *tableRow) *string { return &r.msg.InReplyTo })},
	{propConversationID, bin(func(r *tableRow) *string { return &r.msg.ConversationID })},
	{propConversationIndex, bin(func(r *tableRow) *string { return &r.msg.ConversationIndex })},
	{builtinCategories, str(func(r *tableRow) *string { return &r.msg.Categories })},
	{propReminderSet, boolean(func(r *tableRow) *bool { return &r.msg.ReminderSet })},
	{propReminderTime, when(func(r *tableRow) *string { return &r.msg.ReminderTime })},
	{propFlagRequest, str(func(r *tableRow) *string { return &r.msg.FlagRequest })},
	{propTaskDueDate, day(func(r *tableRow) *string { return &r.msg.TaskDueDate })},
	{propTaskStartDate, day(func(r *tableRow) *string { return &r.msg.TaskStartDate })},
}

// previewColumn is the table's body preview (Outlook caps table strings),
// shown until the detail pass replaces it (outlook.table.body_preview).
var previewColumn = column{propBody, str(func(r *tableRow) *string { return &r.msg.Body })}

// fullColumns is the column list of the full table pass.
func fullColumns(preview bool) []column {
	cols := append(append([]column{}, idColumns...), summaryColumns...)
	if preview {
		cols = append(cols, previewColumn)
	}
	return cols
}

// decode applies one row's values to a fresh tableRow.
func decode(cols []column, values []any, t mail.Times) tableRow {
	var r tableRow
	for i, c := range cols {
		if i < len(values) && !unset(values[i]) {
			c.set(&r, values[i], t)
		}
	}
	return r
}

// stamp is the row's change stamp (mail.Message.Stamp): the modification
// time to the millisecond and the message flags, because a read flip moves
// no modification time.
func (r *tableRow) stamp() string {
	return r.modified + " " + fmt.Sprintf("%x", r.flags.N)
}

// isRead is the read bit of PidTagMessageFlags.
func (r *tableRow) isRead() bool { return r.flags.Valid && r.flags.N&msgFlagRead != 0 }

// addressedToMe applies the legacy rule (PORTING §1): recipient-me false →
// no; to-me → to; cc-me → cc; otherwise bcc; unreadable → "".
func (r *tableRow) addressedToMe() string {
	switch {
	case !r.meKnown:
		return ""
	case !r.recipMe:
		return mail.AddressedNo
	case r.toMe:
		return mail.AddressedTo
	case r.ccMe:
		return mail.AddressedCc
	}
	return mail.AddressedBcc
}
