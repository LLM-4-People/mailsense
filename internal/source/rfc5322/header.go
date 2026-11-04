package rfc5322

import (
	"bytes"
	"io"
	"mime"
	netmail "net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// headerEnd is the length of the header section of data, its last line
// break included (the blank line after it excluded).
func headerEnd(data []byte) int {
	end := len(data)
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		end = i + 1
	}
	if i := bytes.Index(data, []byte("\r\n\r\n")); i >= 0 && i+2 < end {
		end = i + 2
	}
	return end
}

// decodeHeaderBytes is the declared decoding step of raw 8-bit headers:
// every header line that is not UTF-8 (RFC 6532) is decoded with the
// message's declared charset when one is known, else with raw (the
// setting rfc5322.headers.raw_charset); the charset is recorded on m.
// UTF-8 lines are never touched.
func decodeHeaderBytes(data []byte, raw encoding.Encoding, m *mail.Message) []byte {
	end := headerEnd(data)
	head := data[:end]
	if utf8.Valid(head) {
		return data
	}
	enc := declaredEncoding(head, raw)
	name, _ := htmlindex.Name(enc)
	var b bytes.Buffer
	b.Grow(len(data) + len(head))
	for line := range bytes.Lines(head) {
		if !utf8.Valid(line) {
			out, err := enc.NewDecoder().Bytes(line)
			if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
				m.Alter(mail.AltInvalidBytes, name)
			}
			line = bytes.ToValidUTF8(out, []byte(string(utf8.RuneError)))
		}
		b.Write(line)
	}
	m.Alter(mail.AltHeaderBytes, name)
	b.Write(data[end:])
	return b.Bytes()
}

// declaredEncoding is the charset the message's Content-Type declares,
// when it is a known single-byte-compatible one (not UTF-8: its bytes
// were not UTF-8; not UTF-16), else raw.
func declaredEncoding(head []byte, raw encoding.Encoding) encoding.Encoding {
	_, params, err := mime.ParseMediaType(mail.HeaderValue(string(bytes.ToValidUTF8(head, nil)), "Content-Type"))
	if err != nil || params["charset"] == "" {
		return raw
	}
	enc, err := htmlindex.Get(params["charset"])
	if err != nil {
		return raw
	}
	if name, _ := htmlindex.Name(enc); name == "utf-8" || strings.HasPrefix(name, "utf-16") {
		return raw
	}
	return enc
}

// newWordDecoder decodes RFC 2047 words in any registered charset; an
// unknown charset leaves the word as written and is recorded on m.
func newWordDecoder(m *mail.Message) *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(charset)
		if err != nil {
			m.Alter(mail.AltCharsetUnknown, strings.ToLower(charset))
			return nil, err
		}
		return enc.NewDecoder().Reader(input), nil
	}}
}

func decodeWords(dec *mime.WordDecoder, s string) string {
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// angleAddr is an address in angle brackets (RFC 5322 angle-addr).
var angleAddr = regexp.MustCompile(`<([^<>\s@]+@[^<>\s]+)>`)

// addresses parses the address list of header name. A list that does not
// parse yields the addresses in its angle brackets, else the value as
// written (one recipient); either way the step is recorded on m.
func addresses(ap *netmail.AddressParser, name, v string, kind mail.RecipientKind, m *mail.Message) []mail.Recipient {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	list, err := ap.ParseList(v)
	if err == nil {
		out := make([]mail.Recipient, 0, len(list))
		for _, a := range list {
			out = append(out, mail.Recipient{Kind: kind, Name: a.Name, Address: a.Address})
		}
		return out
	}
	m.Alter(mail.AltAddressUnparsed, name)
	matches := angleAddr.FindAllStringSubmatchIndex(v, -1)
	if len(matches) == 0 {
		return []mail.Recipient{{Kind: kind, Address: strings.TrimSpace(v)}}
	}
	out := make([]mail.Recipient, 0, len(matches))
	prev := 0
	for _, mt := range matches {
		display := v[prev:mt[0]]
		if i := strings.LastIndexByte(display, ','); i >= 0 {
			display = display[i+1:]
		}
		out = append(out, mail.Recipient{Kind: kind, Name: strings.Trim(display, " \t\"<>"), Address: v[mt[2]:mt[3]]})
		prev = mt[1]
	}
	return out
}
