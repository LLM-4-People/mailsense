package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// messageColumns maps mail.Message fields to messages columns. The struct is
// the source of truth: every json-tagged field becomes the column of the same
// name, except the key/folder fields stored elsewhere and two renames.
//
// BodyHTML is deliberately json:"-" and therefore not part of this mapping:
// it is display-only, loaded by the single-message read (or by EachMessage
// with WithBodyHTML) and never selected by list, triage or search reads.
type columnMap struct {
	names  []string // SQL column
	tags   []string // mail.Message json field it maps (references, not message_references)
	fields []int
	json   []bool // a slice field, stored as a JSON array
}

var (
	// Stored as key columns or in folders/recipients/attachments/
	// message_folders, not as plain columns.
	columnSkip = map[string]bool{
		"source_id": true, "store_id": true, "id": true, "store_name": true,
		"folder_path": true, "folder_role": true, "recipients": true, "attachments": true, "folders": true,
	}
	// "references" is an SQL keyword.
	columnRename = map[string]string{"references": "message_references"}

	messageColumns = buildColumns()
)

func buildColumns() columnMap {
	var c columnMap
	t := reflect.TypeFor[mail.Message]()
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag == "" || tag == "-" || columnSkip[tag] {
			continue
		}
		logical := tag
		if r, ok := columnRename[tag]; ok {
			tag = r
		}
		c.names = append(c.names, tag)
		c.tags = append(c.tags, logical)
		c.fields = append(c.fields, i)
		c.json = append(c.json, t.Field(i).Type.Kind() == reflect.Slice)
	}
	return c
}

// args returns the column values of m (booleans as 0/1, slices as JSON
// arrays: an empty one is "[]").
func (c columnMap) args(m *mail.Message) ([]any, error) {
	v := reflect.ValueOf(m).Elem()
	out := make([]any, len(c.fields))
	for i, f := range c.fields {
		fv := v.Field(f).Interface()
		switch {
		case c.json[i]:
			if v.Field(f).Len() == 0 {
				fv = "[]"
				break
			}
			b, err := json.Marshal(fv)
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", c.names[i], err)
			}
			fv = string(b)
		default:
			if b, ok := fv.(bool); ok {
				fv = boolInt(b)
			}
		}
		out[i] = fv
	}
	return out, nil
}

// dests returns scan destinations into m, in column order.
func (c columnMap) dests(m *mail.Message) []any {
	v := reflect.ValueOf(m).Elem()
	out := make([]any, len(c.fields))
	for i, f := range c.fields {
		out[i] = v.Field(f).Addr().Interface()
		if c.json[i] {
			out[i] = jsonColumn{out[i]}
		}
	}
	return out
}

// jsonColumn scans a JSON array column into the slice it points to.
type jsonColumn struct{ dest any }

var _ sql.Scanner = jsonColumn{}

func (j jsonColumn) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("store: JSON column holds %T", src)
	}
	if string(b) == "[]" {
		return nil
	}
	return json.Unmarshal(b, j.dest)
}

func (c columnMap) list(prefix string) string {
	parts := make([]string, len(c.names))
	for i, n := range c.names {
		parts[i] = prefix + n
	}
	return strings.Join(parts, ", ")
}

// detailColumnNames are the columns mail.Details owns: Message.Complete
// overwrites every one of them from the detail read. A detail pass therefore
// never needs their stored value, and reading them back was the largest part
// of the per-message read (body and headers alone are most of the row).
var detailColumnNames = map[string]bool{
	"subject": true, "sender_name": true, "local_commit_time": true,
	"categories": true, "flag_request": true, "internet_message_id": true,
	"in_reply_to": true, "message_references": true, "conversation_index": true,
	"headers": true, "body": true, "body_truncated": true, "alterations": true,
}

// detailColumns is messageColumns without the detail-owned fields: the one
// projection of a message that only lacks its details.
var detailColumns = dropColumns(messageColumns, detailColumnNames)

// dropColumns returns c without the named columns, keeping the logical tag,
// struct field and JSON-slice mapping of every kept column.
func dropColumns(c columnMap, skip map[string]bool) columnMap {
	var out columnMap
	for i, name := range c.names {
		if skip[name] {
			continue
		}
		out.names = append(out.names, name)
		out.tags = append(out.tags, c.tags[i])
		out.fields = append(out.fields, c.fields[i])
		out.json = append(out.json, c.json[i])
	}
	return out
}

// MessageFields is one projection of mail.Message for EachMessage. The zero
// value reads every mapped column and the recipients and item subqueries,
// so a filter that does not set it keeps the full read.
type MessageFields struct {
	p *messageFields
}

// messageFields is a resolved projection: the message columns in mapping
// order and whether the out-of-line subqueries run.
type messageFields struct {
	columns    columnMap
	recipients bool // recipientsSQL
	items      bool // itemsSQL (attachments and further folders)
}

// allMessageFields is the zero projection: the whole message.
var allMessageFields = &messageFields{columns: messageColumns, recipients: true, items: true}

// projection resolves the zero value to the full read.
func (f MessageFields) projection() *messageFields {
	if f.p == nil {
		return allMessageFields
	}
	return f.p
}

// MessageFieldsOf projects the named mail.Message json fields. A message
// column is picked from messageColumns (its one mapping), never listed by
// hand, so a renamed or added field cannot drift; "references" therefore
// resolves to the stored message_references. "recipients" selects
// recipientsSQL; "attachments" and "folders" select itemsSQL, which reads
// both. An unknown name is a programming defect and panics when the
// projection is built, before any read runs.
//
// The identity EachMessage always reads (row id, revision, source_id,
// store_id, id, store_name, folder_path, folder_role, store kind, verdict
// and reason) is outside a projection: a caller never has to name it.
func MessageFieldsOf(names ...string) MessageFields {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	p := &messageFields{}
	for _, n := range []string{"recipients", "attachments", "folders"} {
		if !want[n] {
			continue
		}
		delete(want, n)
		if n == "recipients" {
			p.recipients = true
		} else {
			p.items = true
		}
	}
	for i, tag := range messageColumns.tags {
		if !want[tag] {
			continue
		}
		delete(want, tag)
		p.columns.names = append(p.columns.names, messageColumns.names[i])
		p.columns.tags = append(p.columns.tags, tag)
		p.columns.fields = append(p.columns.fields, messageColumns.fields[i])
		p.columns.json = append(p.columns.json, messageColumns.json[i])
	}
	if len(want) > 0 {
		panic("store: unknown message field(s): " + strings.Join(slices.Sorted(maps.Keys(want)), ", "))
	}
	return MessageFields{p: p}
}

// Select is the projection's SQL select list for EachMessage, in scan
// order: recipientsSQL and itemsSQL when read, then the selected message
// columns with the query's m alias. EachMessage is its one production
// caller; a consumer's test uses it to assert which columns a read selects.
// A zero MessageFields returns the full read.
func (f MessageFields) Select() string {
	p := f.projection()
	parts := make([]string, 0, len(p.columns.names)+2)
	if p.recipients {
		parts = append(parts, recipientsSQL)
	}
	if p.items {
		parts = append(parts, itemsSQL)
	}
	if list := p.columns.list("m."); list != "" {
		parts = append(parts, list)
	}
	return strings.Join(parts, ", ")
}

// bodyHTMLColumns are the display-only body columns, declared once. They are
// appended to the message upsert and read only by the paths that render a
// message.
var bodyHTMLColumns = []string{"body_html", "body_html_known", "body_html_truncated"}

// instantColumn is the materialized mail.Instant of a message (migration
// 0021). Like the HTML columns it is derived and therefore not part of the
// mail.Message JSON mapping; the one write path computes it explicitly.
const instantColumn = "instant"

// instantArgs returns the stored instant of m from mail.Instant. There is
// no second timing rule: the list reads the column, every write recomputes
// it here, and the migration backfill applies the same rule once.
func instantArgs(m *mail.Message) []any {
	t, ok := m.Instant()
	if !ok {
		return []any{""}
	}
	return []any{t.UTC().Format(mail.TimeLayout)}
}

// senderColumns are the materialized sender keys of a message: the whole
// normalized address and its exact domain. Like instant they are derived and
// therefore not part of the mail.Message JSON mapping; the one write path
// computes them explicitly and the migration backfilled them once.
var senderColumns = []string{"sender_norm", "sender_domain"}

// senderArgs returns the stored sender keys of m. mail.NormalizeAddress and
// mail.AddressDomain are the same rules the sender filter compares with.
func senderArgs(m *mail.Message) []any {
	norm := mail.NormalizeAddress(m.SenderAddress)
	return []any{norm, mail.AddressDomain(norm)}
}

// bodyHTMLArgs returns the values of the body columns.
func bodyHTMLArgs(m *mail.Message) []any {
	return []any{m.BodyHTML, boolInt(m.BodyHTMLKnown), boolInt(m.BodyHTMLTruncated)}
}

// bodyHTMLList is the SELECT list of the body columns.
func bodyHTMLList(prefix string) string {
	parts := make([]string, len(bodyHTMLColumns))
	for i, n := range bodyHTMLColumns {
		parts[i] = prefix + n
	}
	return strings.Join(parts, ", ")
}

// bodyHTMLDests returns scan destinations; the two integer flags are read
// into ints because SQLite stores booleans as 0/1.
func bodyHTMLDests(m *mail.Message, known, truncated *int) []any {
	return []any{&m.BodyHTML, known, truncated}
}

// setBodyHTMLFlags applies the scanned body flags.
func setBodyHTMLFlags(m *mail.Message, known, truncated int) {
	m.BodyHTMLKnown, m.BodyHTMLTruncated = known != 0, truncated != 0
}
