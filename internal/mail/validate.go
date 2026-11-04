package mail

import (
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// Validate checks the invariants every stored message must satisfy; it
// reports all violations at once.
func (m *Message) Validate() error {
	var errs []error
	if m.SourceID == "" {
		errs = append(errs, errors.New("empty source id"))
	}
	if m.ID == "" {
		errs = append(errs, errors.New("empty message id"))
	}
	if _, err := ParseRole(string(m.FolderRole)); err != nil {
		errs = append(errs, err)
	}
	if m.FolderPath == "" {
		errs = append(errs, errors.New("empty folder path"))
	}
	for i, r := range m.Recipients {
		if r.Kind != To && r.Kind != Cc && r.Kind != Bcc {
			errs = append(errs, fmt.Errorf("recipient %d: unknown kind %q", i, r.Kind))
		}
	}
	for _, f := range m.TimestampFields() {
		if ts := *f.Value; ts != "" {
			if _, ok := Parse(ts); !ok {
				errs = append(errs, fmt.Errorf("%s: not in stored form %s: %q", f.Name, TimeLayout, ts))
			}
		}
	}
	if !m.BodyHTMLKnown {
		if m.BodyHTML != "" || m.BodyHTMLTruncated {
			errs = append(errs, errors.New("body html is set without body_html_known"))
		}
	}
	errs = append(errs, invalidText(m)...)
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("mail: invalid message %q: %w", m.ID, err)
	}
	return nil
}

// invalidText reports every text field that is not valid UTF-8: a
// connector decodes what it reads (declaring what it had to assume, see
// Alteration), so raw bytes never reach the store.
func invalidText(m *Message) []error {
	var errs []error
	v := reflect.ValueOf(m).Elem()
	for i := range v.NumField() {
		if f := v.Field(i); f.Kind() == reflect.String && !utf8.ValidString(f.String()) {
			errs = append(errs, fmt.Errorf("%s: not valid UTF-8", v.Type().Field(i).Name))
		}
	}
	for i, r := range m.Recipients {
		if !utf8.ValidString(r.Name) || !utf8.ValidString(r.Address) {
			errs = append(errs, fmt.Errorf("recipient %d: not valid UTF-8", i))
		}
	}
	for i, a := range m.Attachments {
		if !utf8.ValidString(a.Name) || !utf8.ValidString(a.ContentType) {
			errs = append(errs, fmt.Errorf("attachment %d: not valid UTF-8", i))
		}
	}
	return errs
}
