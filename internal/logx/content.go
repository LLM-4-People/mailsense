package logx

import "strings"

// contentKeys is the one deny policy for attribute names that carry owner
// content: mail bodies and subjects, addresses, mailbox/store display names,
// calendar series subjects and Q&A text. A match replaces the value with the
// redaction marker whatever its type, so a content value cannot be logged by
// naming it as an attribute. Operational keys (ids, counts, status, paths,
// classes) are not listed and keep their values.
//
// The triage taxonomy uses question_key at its log sites: "question" itself
// is owner content (a Q&A question) and is denied.
var contentKeys = map[string]struct{}{
	"body":         {},
	"body_text":    {},
	"body_html":    {},
	"html_body":    {},
	"text_body":    {},
	"mail_body":    {},
	"raw_body":     {},
	"message_body": {},

	"subject":        {},
	"mail_subject":   {},
	"series_subject": {},
	"headers":        {},
	"raw_headers":    {},

	"sender":            {},
	"sender_name":       {},
	"sender_address":    {},
	"mail_sender":       {},
	"from":              {},
	"from_name":         {},
	"from_address":      {},
	"mail_from":         {},
	"to":                {},
	"to_name":           {},
	"to_address":        {},
	"mail_to":           {},
	"cc":                {},
	"bcc":               {},
	"mail_cc":           {},
	"mail_bcc":          {},
	"reply_to":          {},
	"recipient":         {},
	"recipient_name":    {},
	"recipient_address": {},
	"recipients":        {},
	"mail_recipient":    {},

	"address":       {},
	"addresses":     {},
	"mail_address":  {},
	"email":         {},
	"emails":        {},
	"email_address": {},
	"mail_email":    {},

	"snippet":       {},
	"mail_snippet":  {},
	"content":       {},
	"mail_content":  {},
	"question":      {},
	"mail_question": {},
	"answer":        {},
	"mail_answer":   {},
	"mailbox_name":  {},
	"store_name":    {},
	"display_name":  {},

	// Folder and collection display names are user-authored: a client name,
	// a project or a personal folder. They are not operational ids.
	"folder":      {},
	"folder_path": {},
	"collection":  {},
	"parent":      {},
}

// contentKeySeparators folds the spellings a key can take: a group prefix
// (a.b), a hyphenated or spaced alias (display-name, "Mail Subject").
var contentKeySeparators = strings.NewReplacer(".", "_", "-", "_", " ", "_")

// normalizeContentKey is the one key spelling used by the deny policy:
// lower case, separators folded to '_', empty runs collapsed.
func normalizeContentKey(key string) string {
	key = contentKeySeparators.Replace(strings.ToLower(key))
	key = strings.Trim(key, "_")
	for strings.Contains(key, "__") {
		key = strings.ReplaceAll(key, "__", "_")
	}
	return key
}

// isContentKey reports whether key names owner content. The whole normalized
// key is checked (so display.name, mailbox.name, store.name and mail-subject
// cannot split a compound), then every dot-separated segment and every
// adjacent pair of segments, so a deeper group prefix cannot hide one either.
// Operational compound keys such as address_count are not in the set and
// survive.
func isContentKey(key string) bool {
	if key == "" {
		return false
	}
	parts := strings.Split(key, ".")
	normalized := make([]string, len(parts))
	for i, part := range parts {
		normalized[i] = normalizeContentKey(part)
	}
	if _, ok := contentKeys[normalizeContentKey(key)]; ok {
		return true
	}
	for i, part := range normalized {
		if _, ok := contentKeys[part]; ok {
			return true
		}
		if i+1 < len(normalized) {
			if _, ok := contentKeys[part+"_"+normalized[i+1]]; ok {
				return true
			}
		}
	}
	return false
}
