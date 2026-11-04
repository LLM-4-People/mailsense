// Package archive imports the legacy Python export (JSONL, optionally
// gzip-compressed) — the development fixture and the migration path from the
// old app. Records are validated exactly like the legacy
// EmailMessage.from_record: unknown fields and missing required fields are
// rejected, booleans must be JSON booleans, null becomes "".
package archive

// fieldKind is the JSON type of a legacy field.
type fieldKind int

const (
	kindString fieldKind = iota
	kindBool
)

type legacyField struct {
	name     string
	kind     fieldKind
	required bool
}

// legacyFields is the legacy EmailMessage dataclass (mailbox.py:20-65):
// required = no default in the dataclass.
var legacyFields = []legacyField{
	{"id", kindString, true}, {"subject", kindString, true}, {"sender_name", kindString, true},
	{"sender_address", kindString, true}, {"received_at", kindString, true}, {"body_preview", kindString, true},
	{"is_read", kindBool, true}, {"importance", kindString, true}, {"source", kindString, true},
	{"folder", kindString, false}, {"conversation_id", kindString, false},
	{"to_addresses", kindString, false}, {"cc_addresses", kindString, false}, {"bcc_addresses", kindString, false},
	{"has_attachments", kindBool, false}, {"addressed_to_me", kindString, false},
	{"sent_representing_address", kindString, false}, {"sent_on", kindString, false},
	{"creation_time", kindString, false}, {"last_modification_time", kindString, false},
	{"last_verb_execution_time", kindString, false}, {"last_verb_executed", kindString, false},
	{"message_flags", kindString, false}, {"local_commit_time", kindString, false},
	{"flag_status", kindString, false}, {"flag_request", kindString, false},
	{"task_due_date", kindString, false}, {"task_start_date", kindString, false},
	{"reminder_set", kindBool, false}, {"reminder_time", kindString, false},
	{"message_class", kindString, false}, {"internet_message_id", kindString, false},
	{"headers", kindString, false}, {"size", kindString, false}, {"sensitivity", kindString, false},
	{"categories", kindString, false}, {"read_receipt_requested", kindBool, false},
	{"voting_options", kindString, false}, {"voting_response", kindString, false},
	{"spam_confidence", kindString, false},
}

var legacyByName = func() map[string]legacyField {
	m := make(map[string]legacyField, len(legacyFields))
	for _, f := range legacyFields {
		m[f.name] = f
	}
	return m
}()

// Legacy folder values (the dataclass default is inbox).
const (
	legacyInbox = "inbox"
	legacySent  = "sent"
)
