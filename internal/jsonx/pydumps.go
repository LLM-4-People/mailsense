package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf16"
)

// PyDumps renders v exactly as Python's json.dumps(v, sort_keys=True,
// ensure_ascii=True) does with the default separators (", " and ": "),
// so a hash over it equals the legacy one byte for byte. Every
// non-printable-ASCII character is escaped as \uXXXX (lower-case hex,
// surrogate pairs above U+FFFF). Numbers are written as decoded.
func PyDumps(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := pyValue(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func pyValue(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(map[bool]string{true: "true", false: "false"}[x])
	case json.Number:
		b.WriteString(x.String())
	case string:
		pyString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case Object:
		return pyObject(b, x)
	default:
		return fmt.Errorf("jsonx: PyDumps cannot encode %T", v)
	}
	return nil
}

func pyObject(b *bytes.Buffer, o Object) error {
	sorted := append(Object(nil), o...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key }) // UTF-8 byte order = code point order
	b.WriteByte('{')
	for i, m := range sorted {
		if i > 0 {
			b.WriteString(", ")
		}
		pyString(b, m.Key)
		b.WriteString(": ")
		if err := pyValue(b, m.Value); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

// pyString follows Python's ESCAPE_ASCII: `\` and `"` and the short escapes
// \n \r \t \b \f, every other character outside ' '..'~' as \uXXXX.
func pyString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	short := map[rune]string{'\\': `\\`, '"': `\"`, '\n': `\n`, '\r': `\r`, '\t': `\t`, '\b': `\b`, '\f': `\f`}
	unit := func(u uint16) {
		b.WriteString(`\u`)
		b.WriteByte(hex[u>>12&0xf])
		b.WriteByte(hex[u>>8&0xf])
		b.WriteByte(hex[u>>4&0xf])
		b.WriteByte(hex[u&0xf])
	}
	b.WriteByte('"')
	for _, r := range s {
		switch esc, ok := short[r]; {
		case ok:
			b.WriteString(esc)
		case r >= ' ' && r <= '~':
			b.WriteRune(r)
		case r > 0xffff:
			hi, lo := utf16.EncodeRune(r)
			unit(uint16(hi))
			unit(uint16(lo))
		default:
			unit(uint16(r))
		}
	}
	b.WriteByte('"')
}
