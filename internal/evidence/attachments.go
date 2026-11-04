package evidence

import (
	"mime"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// realAttachments are the attachments shown as files (not inline in the body).
func realAttachments(m *mail.Message) []mail.Attachment {
	var out []mail.Attachment
	for _, a := range m.Attachments {
		if !a.Inline {
			out = append(out, a)
		}
	}
	return out
}

// mediaType is an attachment's MIME type, lower case, without parameters
// ("" when it has none or it does not parse).
func mediaType(a mail.Attachment) string {
	t, _, err := mime.ParseMediaType(a.ContentType)
	if err != nil {
		return ""
	}
	return strings.ToLower(t)
}

// calendarTypes are the MIME types of iCalendar data (RFC 5545 §8.1;
// application/ics is its common alias).
var calendarTypes = []string{"text/calendar", "application/ics"}

func hasCalendar(m *mail.Message) bool {
	return m.CalendarMethod != "" || slices.ContainsFunc(m.Attachments, func(a mail.Attachment) bool {
		return slices.Contains(calendarTypes, mediaType(a))
	})
}
