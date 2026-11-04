package api

import (
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// invalidRequest is the one malformed-input error of a handler: a missing
// or invalid field of a parsed body.
func invalidRequest(message string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_request", Message: message}
}

// invalidRange is the one bounded-integer query error: the same message shape
// and the same name/min/max params whether the value was malformed or outside
// the range (page sizes, offsets, horizons, month counts). Callers keep their
// wire code.
func invalidRange(status int, code, name string, min, max int) *APIError {
	return rangeError(status, code, name, strconv.Itoa(min), strconv.Itoa(max))
}

// rangeError is the one bounded-parameter error shape, shared by the integer
// and floating-point readers so a client composes one message per code no
// matter how the value was spelled.
func rangeError(status int, code, name, min, max string) *APIError {
	return &APIError{Status: status, Code: code,
		Message: name + " must be between " + min + " and " + max,
		Params:  map[string]string{"name": name, "min": min, "max": max}}
}

// invalidBool is the one boolean-filter error: the parameter name only, since
// every such filter accepts the same spellings.
func invalidBool(name string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_" + name,
		Message: name + " must be true or false", Params: map[string]string{"name": name}}
}

// invalidLimit is the one bounded page-size error (fourteen routes built it
// by hand, and one had already drifted to another status).
func invalidLimit(status int, name string, min, max int) *APIError {
	return invalidRange(status, "invalid_limit", name, min, max)
}

// invalidOffset is the one offset error: the parameter name and lower bound,
// plus the upper bound when the route bounds the offset. A negative maxValue
// means the route has no upper bound, so the params never advertise a limit
// the route does not enforce.
func invalidOffset(name string, minValue, maxValue int) *APIError {
	if maxValue < 0 {
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_offset",
			Message: name + " must be " + strconv.Itoa(minValue) + " or more",
			Params:  map[string]string{"name": name, "min": strconv.Itoa(minValue)}}
	}
	return invalidRange(http.StatusBadRequest, "invalid_offset", name, minValue, maxValue)
}

// notFound is the one "no such resource" answer of the router, the middleware
// and the message handlers. The code and message are part of the wire contract
// the shell localizes, so they are spelled once.
func notFound() *APIError { return notFoundWith("not_found", "not found") }

// notFoundWith is the one "no such resource" answer with a route's own code
// and message. The wire pairs are a contract the shell localizes, so each
// route names its pair here instead of building the struct by hand.
func notFoundWith(code, message string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: code, Message: message}
}

// invalidCursor is the one cursor error of the conversation, all-mail and
// answer pages: empty, malformed, rejected by the store, or two exclusive
// cursors named at once.
func invalidCursor() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_cursor", Message: "invalid cursor"}
}

// invalidStore is the one mailbox-store filter error of search and the
// all-mail page.
func invalidStore() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_store",
		Message: "store must be a mailbox store key", Params: map[string]string{"name": "store"}}
}

// invalidRole is the one folder-role filter error of search and the all-mail
// page. "all" is a page scope, not a folder role, so the message names only
// the value both routes accept when they show it.
func invalidRole() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_role",
		Message: "role must be a mail folder role", Params: map[string]string{"name": "role"}}
}

func invalidJSON() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "the request body is not valid JSON"}
}

func unsupportedMediaType() *APIError {
	return &APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "the request body must be application/json"}
}

func payloadTooLarge() *APIError {
	return &APIError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "the request body is too large"}
}

// readBody applies the content-type rule to a non-empty body. The body is
// already bounded by http.MaxBytesReader in the middleware.
func (s *Server) readBody(r *http.Request, optional bool) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		if optional {
			return nil, nil
		}
		return nil, invalidJSON()
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, payloadTooLarge()
		}
		if errors.Is(err, r.Context().Err()) && r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return nil, invalidJSON()
	}
	if len(data) == 0 {
		if optional {
			return nil, nil
		}
		return nil, invalidJSON()
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return nil, unsupportedMediaType()
	}
	media, _, err := mime.ParseMediaType(ct)
	if err != nil || media != "application/json" {
		return nil, unsupportedMediaType()
	}
	// JSON is UTF-8 (RFC 8259). encoding/json silently replaces invalid bytes
	// with U+FFFD, which would save a mangled question the user never typed;
	// reject it at the one boundary every request body crosses instead.
	if !utf8.Valid(data) {
		return nil, &APIError{Status: http.StatusBadRequest, Code: "invalid_encoding",
			Message: "the request body is not valid UTF-8"}
	}
	return data, nil
}

// decodeJSON decodes one required JSON document strictly.
func (s *Server) decodeJSON(r *http.Request, dst any) error {
	data, err := s.readBody(r, false)
	if err != nil {
		return err
	}
	if err := jsonx.DecodeStrict(data, dst); err != nil {
		return invalidJSON()
	}
	return nil
}

// decodeOptional allows an empty body but still rejects unknown fields
// and trailing data when one is present.
func (s *Server) decodeOptional(r *http.Request, dst any) error {
	data, err := s.readBody(r, true)
	if err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	if err := jsonx.DecodeStrict(data, dst); err != nil {
		return invalidJSON()
	}
	return nil
}

// queryUint reads an optional non-negative integer query parameter. The caller
// may name its own problem (the SSE cursor has a longer message); without one
// the parameter's own invalid_<name> pair is used.
func queryUint(r *http.Request, name string, invalid func() *APIError) (uint64, bool, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return 0, false, nil
	}
	if invalid == nil {
		invalid = func() *APIError { return invalidParam(name) }
	}
	if len(values) == 0 || values[0] == "" {
		return 0, true, invalid()
	}
	v, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil {
		return 0, true, invalid()
	}
	return v, true, nil
}

// invalidSenderError is the one wire shape of a bad sender filter, shared by
// search and the all-mail page.
func invalidSenderError() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_sender",
		Message: "sender must be a non-empty address or domain",
		Params:  map[string]string{"name": "sender", "max": strconv.Itoa(mail.MaxSenderQueryRunes)}}
}

// querySender reads the optional sender filter of search and All mail. A
// present value is validated and normalized by the mail package; a value the
// mail package refuses never reaches the service.
func querySender(r *http.Request) (string, bool, error) {
	values, present := r.URL.Query()["sender"]
	if !present {
		return "", false, nil
	}
	raw := ""
	if len(values) > 0 {
		raw = values[0]
	}
	sender, err := mail.NormalizeSenderQuery(raw)
	if err != nil {
		return "", true, invalidSenderError()
	}
	return sender, true, nil
}

// queryStore reads the optional mailbox-store filter of search and All mail:
// a present value must name a registered source.
func queryStore(r *http.Request) (string, bool, error) {
	values, present := r.URL.Query()["store"]
	if !present {
		return "", false, nil
	}
	if len(values) == 0 || !source.ValidStoreKey(values[0]) {
		return "", true, invalidStore()
	}
	return values[0], true, nil
}

// RoleGrammar says which role vocabulary a page speaks: the All-mail page also
// accepts the synthetic "all" population and treats an empty value as no
// filter; search accepts neither.
type RoleGrammar struct {
	AllowAll   bool
	AllowEmpty bool
}

// queryRole reads the optional folder-role filter of search and All mail.
func queryRole(r *http.Request, grammar RoleGrammar) (mail.FolderRole, bool, error) {
	values, present := r.URL.Query()["role"]
	if !present {
		return "", false, nil
	}
	raw := ""
	if len(values) > 0 {
		raw = values[0]
	}
	if raw == "" {
		if grammar.AllowEmpty {
			return "", true, nil
		}
		return "", true, invalidRole()
	}
	role := mail.FolderRole(raw)
	if grammar.AllowAll && role == store.MessageListAll {
		return role, true, nil
	}
	if _, err := mail.ParseRole(raw); err != nil {
		return "", true, invalidRole()
	}
	return role, true, nil
}

// queryBoolValue reads an optional strict boolean query parameter with the
// grammar shared by counts and unread: 1/true and 0/false, case-insensitive.
// present reports the parameter's presence; ok is false for an empty,
// blank or unrecognized value. The caller owns the wire code and message,
// so each parameter keeps its stable error shape.
func queryBoolValue(r *http.Request, name string) (value, present, ok bool) {
	values, present := r.URL.Query()[name]
	if !present {
		return false, false, true
	}
	if len(values) == 0 {
		return false, true, false
	}
	switch strings.ToLower(values[0]) {
	case "1", "true":
		return true, true, true
	case "0", "false":
		return false, true, true
	default:
		return false, true, false
	}
}

// queryIntValue is the one parser behind queryInt and the bounded helpers: it
// distinguishes absent from malformed and leaves the error shape to the
// caller that knows the parameter's bounds.
func queryIntValue(r *http.Request, name string) (value int, present, ok bool) {
	values, present := r.URL.Query()[name]
	if !present {
		return 0, false, true
	}
	if len(values) == 0 {
		return 0, true, false
	}
	v, err := strconv.Atoi(values[0])
	if err != nil {
		return 0, true, false
	}
	return v, true, true
}

// queryInt reads an optional integer query parameter. A malformed value gets
// the bare invalid_<name> shape because this parser knows no bounds;
// boundedQueryInt and boundedQueryOffset use queryIntValue directly to emit
// the ranged shape the client needs.
func queryInt(r *http.Request, name string) (int, bool, error) {
	value, present, ok := queryIntValue(r, name)
	if !ok {
		return 0, true, &APIError{Status: http.StatusBadRequest, Code: "invalid_" + name, Message: "invalid " + name}
	}
	return value, present, nil
}

// boundedQueryInt reads an optional integer with one inclusive range. The
// default applies when the parameter is absent. Every misuse — empty,
// malformed or outside the range — is the same invalid_<name> error with the
// name/min/max params the client composes its localized message from.
func boundedQueryInt(r *http.Request, name string, def, minValue, maxValue int) (int, error) {
	value, present, ok := queryIntValue(r, name)
	if !ok {
		return 0, invalidRange(http.StatusBadRequest, "invalid_"+name, name, minValue, maxValue)
	}
	if !present {
		return def, nil
	}
	if value < minValue || value > maxValue {
		return 0, invalidRange(http.StatusBadRequest, "invalid_"+name, name, minValue, maxValue)
	}
	return value, nil
}

// boundedQueryOffset reads an optional offset of at least zero. A negative
// maxValue means the route has no upper bound. Every misuse is the one
// invalid_offset shape: name and min always, max when the route has one.
func boundedQueryOffset(r *http.Request, name string, def, maxValue int) (int, error) {
	value, present, ok := queryIntValue(r, name)
	if !ok {
		return 0, invalidOffset(name, 0, maxValue)
	}
	if !present {
		return def, nil
	}
	if value < 0 || (maxValue >= 0 && value > maxValue) {
		return 0, invalidOffset(name, 0, maxValue)
	}
	return value, nil
}

// boundedQueryFloat reads an optional floating-point query parameter with one
// inclusive range. The default applies when the parameter is absent. Every
// misuse — empty, malformed, NaN/Inf or outside the range — is the same
// invalid_<name> error with the name/min/max params boundedQueryInt emits, so
// the horizon's two failure paths cannot drift apart.
func boundedQueryFloat(r *http.Request, name string, def, minValue, maxValue float64) (float64, error) {
	values, present := r.URL.Query()[name]
	if !present {
		return def, nil
	}
	invalid := rangeError(http.StatusBadRequest, "invalid_"+name, name,
		strconv.FormatFloat(minValue, 'f', -1, 64), strconv.FormatFloat(maxValue, 'f', -1, 64))
	if len(values) == 0 {
		return 0, invalid
	}
	v, err := strconv.ParseFloat(values[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < minValue || v > maxValue {
		return 0, invalid
	}
	return v, nil
}

// pathID reads one positive integer path parameter. Every route that takes
// such an id answers the same 400 invalid_id for a malformed or non-positive
// value, so the client composes one message; what the id names stays in the
// route, which is why the parameter is named here.
func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, &APIError{Status: http.StatusBadRequest, Code: "invalid_id",
			Message: name + " must be a positive integer"}
	}
	return id, nil
}
