package outlook

import (
	"errors"
	"fmt"
)

// Object is one late-bound Outlook object: a COM IDispatch on Windows, a
// fake in tests. Values are nil (VT_EMPTY/VT_NULL), string, int64, bool,
// float64, Date, []byte, PropError (VT_ERROR, e.g. a property that is not
// set), []any (1-D array), [][]any (2-D array, rows first) or Object. Every
// Object obtained must be released exactly once.
type Object interface {
	Get(name string, args ...any) (any, error)  // property get
	Call(name string, args ...any) (any, error) // method call
	// Put sets the one writable member (Items.IncludeRecurrences), an
	// in-memory view flag. It never changes stored Outlook data.
	Put(name string, value any) error
	Release()
}

// classed is implemented by objects that can share per-class state (the
// Windows DISPID cache) once the guard tells them what they are.
type classed interface{ SetClass(class string) }

// ComError is a failed COM call with its HRESULT (for DISP_E_EXCEPTION, the
// SCODE Outlook put in the exception info).
type ComError struct {
	Code   uint32
	Member string
	Desc   string
}

func (e *ComError) Error() string {
	if e.Desc != "" {
		return fmt.Sprintf("outlook: %s: 0x%08X (%s)", e.Member, e.Code, e.Desc)
	}
	return fmt.Sprintf("outlook: %s: 0x%08X", e.Member, e.Code)
}

// PropError is a VT_ERROR value: the SCODE a property read returned in place
// of a value (MAPI_E_NOT_FOUND for a property that is not set).
type PropError uint32

// HResult returns the HRESULT carried by err, if any.
func HResult(err error) (uint32, bool) {
	var ce *ComError
	if errors.As(err, &ce) {
		return ce.Code, true
	}
	return 0, false
}

// Code is the counting label of an error: its HRESULT in hex, or "other".
func Code(err error) string {
	if hr, ok := HResult(err); ok {
		return fmt.Sprintf("0x%08X", hr)
	}
	return "other"
}
