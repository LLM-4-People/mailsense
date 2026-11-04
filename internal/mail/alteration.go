package mail

import "slices"

// Alteration is a declared change a connector made to a message's data
// while reading it: a decoding it had to choose, a part it could not read
// whole, a value it took from another field. Nothing is changed or dropped
// silently: every alteration is stored with the message, counted per code
// in the sync run (store.RunTotals.Altered) and logged by the syncer.
type Alteration struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"` // e.g. the charset that was assumed
}

// Alteration codes: one vocabulary for every connector and the UI.
const (
	// AltHeaderBytes: header bytes that are not UTF-8 (RFC 6532) and not
	// RFC 2047 words were decoded with the charset in Detail.
	AltHeaderBytes = "header_bytes_decoded"
	// AltCharsetUnknown: a text part names a charset no decoder knows
	// (Detail); its bytes that are not UTF-8 became U+FFFD.
	AltCharsetUnknown = "charset_unknown"
	// AltInvalidBytes: a text part's bytes were invalid in its charset
	// (Detail) and became U+FFFD.
	AltInvalidBytes = "invalid_bytes_replaced"
	// AltMIMEUnterminated: a multipart body ends without its closing
	// boundary; the parts read up to there are kept.
	AltMIMEUnterminated = "mime_unterminated"
	// AltTransferDecoding: a part's base64 or quoted-printable data was
	// malformed (Detail: the encoding); the decodable part is kept.
	AltTransferDecoding = "transfer_decoding_failed"
	// AltAddressUnparsed: an address header (Detail: its name) does not
	// parse; its addresses were taken from the angle brackets, or the
	// value kept as written.
	AltAddressUnparsed = "address_unparsed"
	// AltReceivedFromDate: no Received header dates the arrival; the
	// arrival is the Date header (the author's clock).
	AltReceivedFromDate = "received_at_from_date"
	// AltMIMEDepth: MIME nesting deeper than the walk follows; the deeper
	// parts are not read.
	AltMIMEDepth = "mime_too_deep"
	// AltMboxUnsplit: the message holds a line shaped like an mbox
	// separator that no blank line precedes (RFC 4155 requires one), kept
	// as content; Detail counts them. Two messages may have been joined.
	AltMboxUnsplit = "mbox_separator_in_message"
	// AltHeadersTruncated: the transport-header block was longer than
	// mail.headers.max_chars (or the legacy exporter's cap,
	// archive.legacy_header_chars) and was cut; Detail is its length in
	// UTF-16 units when the connector knows it. A header absent from a cut
	// block may lie past the cut (the body has BodyTruncated).
	AltHeadersTruncated = "headers_truncated"
	// AltBodyHTMLTruncated: the raw HTML display body was longer than
	// mail.body.html_max_chars and was cut at a token boundary (the body
	// has BodyHTMLTruncated; the plain Body has its own cap). Detail is
	// its length in UTF-16 units when the connector knows it.
	AltBodyHTMLTruncated = "body_html_truncated"
	// AltInlineAssetDropped: an inline image part was larger than the
	// per-part or per-message inline-asset budget. Its metadata is kept;
	// its bytes are not, so the body renders the inline placeholder.
	AltInlineAssetDropped = "inline_asset_dropped"
)

// Alter records an alteration once (same code and detail).
func (m *Message) Alter(code, detail string) {
	a := Alteration{Code: code, Detail: detail}
	if !slices.Contains(m.Alterations, a) {
		m.Alterations = append(m.Alterations, a)
	}
}
