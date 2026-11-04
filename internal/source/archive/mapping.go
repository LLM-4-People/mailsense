package archive

import (
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// mapper turns validated legacy records into messages.
type mapper struct {
	sourceID string
	times    mail.Times
	bodyCap  int // legacy exporter's body cap in UTF-16 units
	headCap  int // legacy exporter's header cap in UTF-16 units
}

var legacyRoles = map[string]mail.FolderRole{legacyInbox: mail.RoleInbox, legacySent: mail.RoleSent}

func (mp mapper) message(rec record, folder string) (mail.Message, error) {
	s := rec.str
	m := mail.Message{
		SourceID: mp.sourceID, FolderPath: folder, FolderRole: legacyRoles[folder],
		ID: s["id"], EntryID: s["id"], Origin: s["source"],
		Subject: s["subject"], SenderName: s["sender_name"], SenderAddress: s["sender_address"],
		SentRepresentingAddress: s["sent_representing_address"], AddressedToMe: s["addressed_to_me"],
		IsRead: rec.flag["is_read"], HasAttachments: rec.flag["has_attachments"], ReminderSet: rec.flag["reminder_set"],
		ReadReceiptRequested: rec.flag["read_receipt_requested"],
		Categories:           s["categories"], FlagRequest: s["flag_request"], VotingOptions: s["voting_options"],
		VotingResponse: s["voting_response"], MessageClass: s["message_class"],
		InternetMessageID: s["internet_message_id"], ConversationID: s["conversation_id"], Headers: s["headers"],
		Body: s["body_preview"], BodyTruncated: mail.UTF16Len(s["body_preview"]) >= mp.bodyCap,
	}
	if mail.UTF16Len(m.Headers) >= mp.headCap { // the exporter cut the block there: declared, never silent
		m.Alter(mail.AltHeadersTruncated, "")
	}
	for _, r := range []struct {
		field string
		kind  mail.RecipientKind
	}{{"to_addresses", mail.To}, {"cc_addresses", mail.Cc}, {"bcc_addresses", mail.Bcc}} {
		for _, a := range mail.SplitAddresses(s[r.field]) {
			m.Recipients = append(m.Recipients, mail.Recipient{Kind: r.kind, Address: a})
		}
	}
	var errs []error
	// Legacy timestamp fields carry the same names as mail.Message's.
	for _, f := range m.TimestampFields() {
		v, err := mp.times.Normalize(s[f.Name])
		if err != nil {
			errs = append(errs, fmt.Errorf("field %q: %w", f.Name, err))
		}
		*f.Value = v
	}
	for _, f := range []struct {
		name string
		dst  *mail.OptInt
	}{
		{"last_verb_executed", &m.LastVerbExecuted}, {"message_flags", &m.MessageFlags}, {"importance", &m.Importance},
		{"sensitivity", &m.Sensitivity}, {"size", &m.Size}, {"spam_confidence", &m.SpamConfidence}, {"flag_status", &m.FlagStatus},
	} {
		v, err := mail.ParseOptInt(s[f.name])
		if err != nil {
			errs = append(errs, fmt.Errorf("field %q: %w", f.name, err))
		}
		*f.dst = v
	}
	// Message invariants (ids, roles, stored forms) are checked by the syncer.
	return m, errors.Join(errs...)
}
