package rfc5322

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/htmlindex"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// walker visits a MIME tree and collects its text parts, the metadata and
// bounded bytes of its inline images, the METHOD of its calendar part, and
// every alteration it had to make (recorded on m).
type walker struct {
	dec         *mime.WordDecoder
	maxDepth    int
	m           *mail.Message
	plain, html []string
	attachments []mail.Attachment

	inlinePartMax  int
	inlineTotalMax int
	inlineMaxParts int
	inlineBytes    int
	inlineCount    int
}

// entity visits one MIME entity (header h, raw body) at depth.
func (w *walker) entity(h textproto.MIMEHeader, body []byte, depth int) {
	ct := h.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if ct == "" || err != nil {
		mt, params = "text/plain", map[string]string{}
	}
	if strings.HasPrefix(mt, "multipart/") {
		if depth >= w.maxDepth {
			w.m.Alter(mail.AltMIMEDepth, mt)
			return
		}
		w.multipart(body, params["boundary"], depth)
		return
	}
	disp, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	data := w.transfer(h.Get("Content-Transfer-Encoding"), body)
	name := attachmentName(dparams, params, w.dec)
	calendar := mt == "text/calendar"
	cid := mail.NormalizeContentID(h.Get("Content-Id"))
	location := strings.TrimSpace(h.Get("Content-Location"))
	inline := disp == "inline" || cid != "" || location != "" || (calendar && disp != "attachment")
	if calendar {
		w.calendarMethod(params["method"], data)
	}
	if disp == "attachment" || name != "" || calendar || !strings.HasPrefix(mt, "text/") {
		if name == "" && mt == "message/rfc822" {
			name = "message.eml"
		}
		att := mail.Attachment{Name: name, ContentType: mt, Size: int64(len(data)),
			Inline: inline, ContentID: cid, ContentLocation: location}
		if imageType := mail.InlineImageType(mt, name); imageType != "" && len(data) > 0 {
			if w.inlinePartMax <= 0 || w.inlineTotalMax <= 0 || w.inlineMaxParts <= 0 ||
				len(data) > w.inlinePartMax || w.inlineBytes+len(data) > w.inlineTotalMax || w.inlineCount >= w.inlineMaxParts {
				w.m.Alter(mail.AltInlineAssetDropped, name)
			} else {
				att.ContentType = imageType
				att.Data = append([]byte(nil), data...)
				w.inlineBytes += len(data)
				w.inlineCount++
			}
		}
		w.attachments = append(w.attachments, att)
		return
	}
	if mt == "text/html" {
		w.html = append(w.html, w.htmlPart(params["charset"], data))
	} else {
		w.plain = append(w.plain, w.text(params["charset"], data))
	}
}

// multipart visits the parts of a multipart body. A body without its
// closing boundary keeps the parts (and the part) read up to its end; a
// body without any part is kept as text; both are recorded.
func (w *walker) multipart(body []byte, boundary string, depth int) {
	if boundary == "" {
		w.m.Alter(mail.AltMIMEUnterminated, "no boundary")
		w.plain = append(w.plain, w.text("", body))
		return
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for parts := 0; ; parts++ {
		p, err := mr.NextRawPart()
		if err == io.EOF && parts > 0 {
			return
		}
		if err != nil {
			w.m.Alter(mail.AltMIMEUnterminated, "no part")
			if parts == 0 {
				w.plain = append(w.plain, w.text("", body))
			}
			return
		}
		data, err := io.ReadAll(p)
		w.entity(p.Header, data, depth+1)
		if err != nil {
			w.m.Alter(mail.AltMIMEUnterminated, "")
			return
		}
	}
}

// calendarMethod records the iCalendar METHOD of a calendar part: its
// Content-Type parameter, else its METHOD property (RFC 5546).
func (w *walker) calendarMethod(param string, data []byte) {
	if w.m.CalendarMethod != "" {
		return
	}
	if param == "" {
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.EqualFold(strings.TrimSpace(k), "METHOD") {
				param = v
				break
			}
		}
	}
	w.m.CalendarMethod = strings.ToUpper(strings.TrimSpace(param))
}

// attachmentName is the decoded file name (RFC 2231 parameters are
// decoded by mime.ParseMediaType; RFC 2047 words here).
func attachmentName(dparams, params map[string]string, dec *mime.WordDecoder) string {
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	return decodeWords(dec, name)
}

// transfer undoes the Content-Transfer-Encoding; malformed data keeps
// what decoded and is recorded.
func (w *walker) transfer(cte string, body []byte) []byte {
	enc := strings.ToLower(strings.TrimSpace(cte))
	var out []byte
	var err error
	switch enc {
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, body)
		out = make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		var n int
		n, err = base64.StdEncoding.Decode(out, clean)
		out = out[:n]
	case "quoted-printable":
		out, err = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
	default:
		return body
	}
	if err != nil {
		w.m.Alter(mail.AltTransferDecoding, enc)
	}
	return out
}

// text turns a text part of any registered charset (WHATWG names and
// aliases: ISO-8859-*, windows-125*, GB18030/GBK, Big5, Shift_JIS,
// EUC-JP, ISO-2022-JP, EUC-KR, KOI8-R…) into UTF-8. Bytes invalid in
// their charset become U+FFFD; a charset no decoder knows is read as
// UTF-8 when its bytes are not plain ASCII; both are recorded.
func (w *walker) text(charset string, data []byte) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	if cs != "" && cs != "utf-8" && cs != "us-ascii" {
		enc, err := htmlindex.Get(cs)
		if err == nil {
			out, err := enc.NewDecoder().Bytes(data)
			if err != nil || (bytes.ContainsRune(out, utf8.RuneError) && !bytes.ContainsRune(data, utf8.RuneError)) {
				w.m.Alter(mail.AltInvalidBytes, cs)
			}
			if err == nil {
				return string(bytes.ToValidUTF8(out, []byte(string(utf8.RuneError))))
			}
		} else if !ascii(data) {
			w.m.Alter(mail.AltCharsetUnknown, cs)
		}
	}
	if utf8.Valid(data) {
		return string(data)
	}
	w.m.Alter(mail.AltInvalidBytes, "utf-8")
	return strings.ToValidUTF8(string(data), string(utf8.RuneError))
}

// htmlPart decodes an HTML part through the WHATWG sniffing rules: a BOM,
// then the part header's charset, then <meta charset>, then UTF-8 when the
// bytes are valid, then the HTML default (windows-1252). The header charset
// stays authoritative; a missing one no longer turns a correctly tagged
// document into U+FFFD.
func (w *walker) htmlPart(label string, data []byte) string {
	cs := strings.ToLower(strings.TrimSpace(label))
	contentType := "text/html"
	if cs != "" {
		contentType += "; charset=" + cs
		if _, err := htmlindex.Get(cs); err != nil && !ascii(data) {
			w.m.Alter(mail.AltCharsetUnknown, cs)
		}
	}
	r, err := charset.NewReader(bytes.NewReader(data), contentType)
	if err != nil {
		w.m.Alter(mail.AltInvalidBytes, htmlDetail(cs))
		return strings.ToValidUTF8(string(data), string(utf8.RuneError))
	}
	out, err := io.ReadAll(r)
	if err != nil {
		w.m.Alter(mail.AltInvalidBytes, htmlDetail(cs))
		return strings.ToValidUTF8(string(data), string(utf8.RuneError))
	}
	if !utf8.Valid(out) || (bytes.ContainsRune(out, utf8.RuneError) && !bytes.ContainsRune(data, utf8.RuneError)) {
		w.m.Alter(mail.AltInvalidBytes, htmlDetail(cs))
		out = bytes.ToValidUTF8(out, []byte(string(utf8.RuneError)))
	}
	return string(out)
}

func htmlDetail(charset string) string {
	if charset == "" {
		return "html"
	}
	return charset
}

func ascii(data []byte) bool {
	for _, c := range data {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
