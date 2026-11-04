package mail

import (
	_ "embed"
	"mime"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Kind is what an item is beyond "a message": a meeting request or
// response, a delivery report, a read receipt, an automatic reply…
// (kinds.json). Only ordinary messages are replies or wait for one.
type Kind string

// Kinds (the values of kinds.json).
const (
	KindOrdinary        Kind = "ordinary"
	KindMeetingRequest  Kind = "meeting_request"
	KindMeetingCancel   Kind = "meeting_cancel"
	KindMeetingResponse Kind = "meeting_response"
	KindDeliveryReport  Kind = "delivery_report"
	KindReadReceipt     Kind = "read_receipt"
	KindAutoReply       Kind = "auto_reply"
	KindRecall          Kind = "recall"
)

// Ordinary reports whether the kind is a plain message (the zero value
// is one: a message nothing marks otherwise).
func (k Kind) Ordinary() bool { return k == KindOrdinary || k == "" }

//go:embed kinds.json
var kindsJSON []byte

type kindRules struct {
	Doc             string          `json:"_doc"`
	ClassPrefixes   map[string]Kind `json:"message_class_prefixes"`
	ReportTypes     map[string]Kind `json:"report_types"`
	CalendarMethods map[string]Kind `json:"calendar_methods"`
	AutoSubmitted   map[string]Kind `json:"auto_submitted"`
	Automated       automatedRules  `json:"automated"`
}

// kinds decodes the embedded rules once (TestKindRulesAreValid guards the
// file; a broken file is a build defect).
var kinds = sync.OnceValue(func() kindRules {
	var r kindRules
	if err := jsonx.DecodeStrict(kindsJSON, &r); err != nil {
		panic("mail: kinds.json: " + err.Error())
	}
	return r
})

// KindOf classifies a message from its class, its headers and the METHOD
// of its calendar part (a multipart invitation carries it in the part).
func KindOf(m *Message) Kind {
	r := kinds()
	best, kind := -1, Kind("")
	for prefix, k := range r.ClassPrefixes {
		if len(prefix) > best && len(m.MessageClass) >= len(prefix) && strings.EqualFold(m.MessageClass[:len(prefix)], prefix) {
			best, kind = len(prefix), k
		}
	}
	if kind != "" && kind != KindOrdinary {
		return kind
	}
	if k := headerKind(m.Headers, r); k != "" {
		return k
	}
	if k, ok := r.CalendarMethods[strings.ToUpper(m.CalendarMethod)]; ok {
		return k
	}
	return KindOrdinary
}

func headerKind(headers string, r kindRules) Kind {
	if headers == "" {
		return ""
	}
	if mt, params, err := mime.ParseMediaType(HeaderValue(headers, "Content-Type")); err == nil {
		switch mt {
		case "multipart/report":
			if k, ok := r.ReportTypes[strings.ToLower(params["report-type"])]; ok {
				return k
			}
		case "text/calendar":
			if k, ok := r.CalendarMethods[strings.ToUpper(params["method"])]; ok {
				return k
			}
		}
	}
	return r.AutoSubmitted[autoSubmitted(headers)]
}

// autoSubmitted is the Auto-Submitted keyword of a header block.
func autoSubmitted(headers string) string {
	return AutoSubmittedKeyword(HeaderValue(headers, "Auto-Submitted"))
}

// AutoSubmittedKeyword is the keyword of an Auto-Submitted value, lower
// case: its first token (RFC 3834: parameters after ";", comments in
// parentheses); "" for none.
func AutoSubmittedKeyword(value string) string {
	tokens := strings.FieldsFunc(value, func(c rune) bool { return c == ';' || c == '(' || c == ' ' || c == '\t' })
	if len(tokens) == 0 {
		return ""
	}
	return strings.ToLower(tokens[0])
}

// DocumentedClass reports whether a message class is one the protocol
// documents (a message class of kinds.json, case-insensitive): any other
// (a custom form of an organisation or a vendor) is reported only as
// "custom" in aggregates, its name never shown.
func DocumentedClass(class string) bool {
	for prefix := range kinds().ClassPrefixes {
		if !strings.HasSuffix(prefix, ".") && strings.EqualFold(class, prefix) {
			return true
		}
	}
	return false
}
