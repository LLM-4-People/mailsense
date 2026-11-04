package rfc5322

import (
	"bytes"
	"io"
	"mime"
	netmail "net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// limits are the per-message rules (mail.* and rfc5322.* settings).
type limits struct {
	bodyChars, headerChars, htmlChars int
	maxDepth                          int
	inlinePartBytes                   int
	inlineTotalBytes                  int
	inlineParts                       int
	rawCharset                        encoding.Encoding // decodes raw 8-bit header lines
	times                             mail.Times
}

// parse maps one RFC 5322 message (RFC 6532 UTF-8 headers, RFC 2047
// encoded words, MIME bodies in any registered charset) to the model.
// Addressing follows RFC 5322: From is the author (Outlook's "sent
// representing"), Sender the agent who sent it when present. Every change
// it has to make to the data is recorded on the message (mail.Alteration).
// The full header block is returned too: the stored one may be cut
// (mail.headers.max_chars), and what the connector reads from the headers
// (labels, client status) is read whole.
func parse(data []byte, lim limits) (mail.Message, netmail.Header, error) {
	var m mail.Message
	data = decodeHeaderBytes(data, lim.rawCharset, &m)
	msg, err := netmail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return mail.Message{}, nil, err
	}
	dec := newWordDecoder(&m)
	h := msg.Header
	m.Subject, m.InternetMessageID = decodeWords(dec, h.Get("Subject")), strings.TrimSpace(h.Get("Message-Id"))
	m.InReplyTo, m.References = strings.TrimSpace(h.Get("In-Reply-To")), strings.TrimSpace(h.Get("References"))
	m.Size = mail.Int(int64(len(data)))
	parseAddressing(&m, h, dec)
	parseTimes(&m, h, lim.times)
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return mail.Message{}, nil, err
	}
	w := &walker{dec: dec, maxDepth: lim.maxDepth, m: &m,
		inlinePartMax: lim.inlinePartBytes, inlineTotalMax: lim.inlineTotalBytes, inlineMaxParts: lim.inlineParts}
	w.entity(textproto.MIMEHeader(h), body, 0)
	rawHTML := strings.Join(w.html, "\n")
	// The plain body stays the learning/search body. The HTML alternative is
	// captured separately for display: a non-blank text stub never hides it.
	m.BodyHTMLKnown = true
	if rawHTML != "" {
		m.BodyHTML, m.BodyHTMLTruncated = mail.TruncateHTML(rawHTML, lim.htmlChars)
		if m.BodyHTMLTruncated {
			m.Alter(mail.AltBodyHTMLTruncated, strconv.Itoa(mail.UTF16Len(rawHTML)))
		}
	}
	text := strings.Join(w.plain, "\n")
	if strings.TrimSpace(text) == "" {
		text = htmlText(rawHTML)
	}
	m.Body, m.BodyTruncated = mail.TruncateUTF16(text, lim.bodyChars)
	var cut bool
	if m.Headers, cut = mail.TruncateUTF16(string(bytes.TrimRight(data[:headerEnd(data)], "\r\n")), lim.headerChars); cut {
		m.Alter(mail.AltHeadersTruncated, "")
	}
	m.Attachments = w.attachments
	for _, a := range w.attachments {
		m.HasAttachments = m.HasAttachments || !a.Inline
	}
	return m, h, nil
}

// parseAddressing sets the author, the sender and the recipients.
func parseAddressing(m *mail.Message, h netmail.Header, dec *mime.WordDecoder) {
	ap := &netmail.AddressParser{WordDecoder: dec}
	if from := addresses(ap, "From", h.Get("From"), mail.To, m); len(from) > 0 {
		m.SenderName, m.SenderAddress, m.SentRepresentingAddress = from[0].Name, from[0].Address, from[0].Address
	}
	if sender := addresses(ap, "Sender", h.Get("Sender"), mail.To, m); len(sender) > 0 {
		m.SenderAddress = sender[0].Address
	}
	for _, k := range []struct {
		header string
		kind   mail.RecipientKind
	}{{"To", mail.To}, {"Cc", mail.Cc}, {"Bcc", mail.Bcc}} {
		m.Recipients = append(m.Recipients, addresses(ap, k.header, h.Get(k.header), k.kind, m)...)
	}
}

// parseTimes sets the sending time (Date) and the arrival (the topmost
// Received header's date). Without a Received date the arrival is the
// Date header, recorded as an alteration.
func parseTimes(m *mail.Message, h netmail.Header, times mail.Times) {
	if t, err := h.Date(); err == nil {
		m.SentOn = times.Format(t)
	}
	if t, ok := receivedAt(h["Received"]); ok {
		m.ReceivedAt = times.Format(t)
	} else if m.SentOn != "" {
		m.ReceivedAt = m.SentOn
		m.Alter(mail.AltReceivedFromDate, "")
	}
}

// receivedAt is the time the receiving server stamped (the date after the
// last ";" of the topmost Received header).
func receivedAt(received []string) (time.Time, bool) {
	if len(received) == 0 {
		return time.Time{}, false
	}
	i := strings.LastIndexByte(received[0], ';')
	if i < 0 {
		return time.Time{}, false
	}
	t, err := netmail.ParseDate(strings.TrimSpace(received[0][i+1:]))
	return t, err == nil
}
