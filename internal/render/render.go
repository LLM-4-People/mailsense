// Package render turns a stored message body into the one safe display
// fragment the API exposes. Raw HTML is never trusted: every read parses it
// again and rebuilds a small allowlisted fragment. Plain text is escaped and
// linkified, never parsed as markup.
//
// The package owns no storage and no network access. It is the single
// sanitizer of the application; the UI only inserts its output into a
// sandboxed, script-free iframe.
package render

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Body formats reported to clients.
const (
	FormatHTML = "html"
	FormatText = "text"
)

// Input is one message's stored bodies. HTML is the raw stored fragment
// (display only); Plain is the canonical learning/search body. Attachments
// supply only the inline-image identity (Content-ID and content address):
// the renderer never reads or holds image bytes.
type Input struct {
	HTML          string
	HTMLKnown     bool
	HTMLTruncated bool
	Plain         string
	Attachments   []mail.Attachment
}

// Result is the display fragment and the facts a client needs to describe it.
// CSS is the compiled per-message presentation sheet: the shell links it as a
// blob stylesheet, so sender CSS applies without an inline style attribute.
type Result struct {
	Format        string // FormatHTML or FormatText
	HTML          string // safe fragment, "" for an empty body
	CSS           string // compiled presentation sheet, "" when none applies
	Truncated     bool   // the rich body was cut or withheld
	RemoteImages  int    // remote images found in the HTML
	BlockedImages int    // remote images replaced by a placeholder
	InlineImages  int    // cid/location images resolved to a stored asset
	MissingInline int    // inline references the message has no bytes for
}

// Body returns the display fragment of one message. A known, non-empty HTML
// body is sanitized; anything else (no HTML, an unsanitizable body, a body
// over a safety cap) falls back to the escaped plain text. A sanitizer
// failure is never allowed to hide the message and never emits raw HTML.
func Body(in Input) Result {
	if in.HTMLKnown && strings.TrimSpace(in.HTML) != "" {
		fragment, err := build(in.HTML, options{attachments: in.Attachments})
		if err == nil && strings.TrimSpace(fragment.html) != "" {
			return Result{
				Format:        FormatHTML,
				HTML:          fragment.html,
				CSS:           fragment.css,
				Truncated:     in.HTMLTruncated,
				RemoteImages:  fragment.stats.RemoteImages,
				BlockedImages: fragment.stats.BlockedImages,
				InlineImages:  fragment.stats.InlineImages,
				MissingInline: fragment.stats.MissingInline,
			}
		}
		// A sanitizable body with no readable content (only scripts, say)
		// is not a truncation; a body the sanitizer had to refuse is.
		return Result{Format: FormatText, HTML: Text(in.Plain), Truncated: err != nil || in.HTMLTruncated}
	}
	return Result{Format: FormatText, HTML: Text(in.Plain), Truncated: in.HTMLTruncated}
}
