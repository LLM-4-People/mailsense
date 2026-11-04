package logx

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// Entry is one redacted log record as stored, displayed and published.
type Entry struct {
	Seq       uint64         `json:"seq"`
	Time      time.Time      `json:"time"`
	Level     string         `json:"level"`
	Component string         `json:"component"`
	Message   string         `json:"msg"`
	Attrs     map[string]any `json:"attrs,omitempty"`

	// wire is the entry's encoded JSON, cached once by Ring.push for the file
	// line and every SSE client. It is unexported so it never reaches the wire
	// and is nil for entries inserted by tests or without a JSON consumer.
	wire []byte
}

// addAttr flattens an attribute into dst with dotted group keys, converting
// every value into a JSON-safe, redacted form. A content key is denied before
// its value is read: the marker replaces it whatever the value's type. A
// content-named group becomes one marker instead of exposing its children.
// Non-primitive values are rendered to strings so no structured value can
// bypass redaction.
func addAttr(dst map[string]any, prefix string, a slog.Attr, r *redactor) {
	key := prefix + a.Key
	if a.Key != "" && isContentKey(key) {
		dst[key] = r.replacement
		return
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p = prefix + a.Key + "."
		}
		for _, g := range v.Group() {
			addAttr(dst, p, g, r)
		}
		return
	}
	if a.Key == "" {
		return
	}
	dst[key] = value(v, r)
}

func value(v slog.Value, r *redactor) any {
	switch v.Kind() {
	case slog.KindString:
		return r.redact(v.String())
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindFloat64:
		if f := v.Float64(); !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f
		}
		return fmt.Sprint(v.Float64())
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339Nano)
	}
	if err, ok := v.Any().(error); ok {
		return r.redact(err.Error())
	}
	return r.redact(fmt.Sprint(v.Any()))
}

// JSON returns the entry's wire JSON. Ring.push caches it when the pipeline
// has a JSON consumer; entries inserted elsewhere encode on demand. The
// returned bytes are read-only: every consumer shares one cached slice.
func (e Entry) JSON() ([]byte, error) {
	if e.wire != nil {
		return e.wire, nil
	}
	return json.Marshal(e)
}

// cacheWire encodes the entry once, after its Seq has been assigned. A
// marshal failure leaves wire nil and the consumers report it through JSON().
func (e *Entry) cacheWire() {
	b, err := json.Marshal(*e)
	if err != nil {
		return
	}
	// Clip to length: line() appends the newline, and spare capacity would let
	// that append write into the cached backing array.
	e.wire = slices.Clip(b)
}

// line is the JSON form written to the log file.
func (e Entry) line() ([]byte, error) {
	b, err := e.JSON()
	if err != nil {
		return nil, err
	}
	// Clip so appending the newline cannot write into the cached bytes.
	return append(slices.Clip(b), '\n'), nil
}

// text is the human form written to stderr.
func (e Entry) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-5s [%s] %s", e.Time.Format(time.RFC3339), e.Level, e.Component, e.Message)
	for _, k := range slices.Sorted(maps.Keys(e.Attrs)) {
		fmt.Fprintf(&b, " %s=%v", k, e.Attrs[k])
	}
	b.WriteByte('\n')
	return b.String()
}
