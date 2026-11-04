package taxonomy

import (
	"fmt"
	"strings"
)

// Template is a parsed Python str.format template restricted to plain
// {name} fields ("{{" and "}}" are literal braces).
type Template struct {
	parts  []string // literal, field, literal, field, …, literal
	Fields []string
}

// ParseTemplate parses s; a conversion (!r), format spec (:x), attribute,
// index, positional field or unmatched brace is an error.
func ParseTemplate(s string) (*Template, error) {
	t := &Template{}
	var lit strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c == '{' || c == '}') && i+1 < len(s) && s[i+1] == c:
			lit.WriteByte(c)
			i++
		case c == '}':
			return nil, fmt.Errorf("single '}' at offset %d", i)
		case c == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unmatched '{' at offset %d", i)
			}
			name := s[i+1 : i+end]
			if name == "" || strings.ContainsAny(name, "{!:.[]") || name[0] >= '0' && name[0] <= '9' {
				return nil, fmt.Errorf("unsupported field {%s} (only {name} placeholders)", name)
			}
			t.parts = append(t.parts, lit.String(), name)
			t.Fields = append(t.Fields, name)
			lit.Reset()
			i += end
		default:
			lit.WriteByte(c)
		}
	}
	t.parts = append(t.parts, lit.String())
	return t, nil
}

// Render fills the fields from values (missing ones render empty).
func (t *Template) Render(values map[string]string) string {
	var b strings.Builder
	for i, p := range t.parts {
		if i%2 == 0 {
			b.WriteString(p)
		} else {
			b.WriteString(values[p])
		}
	}
	return b.String()
}
