package mail

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// OptInt is an integer that may be absent (a property the source did not expose).
type OptInt struct {
	N     int64
	Valid bool
}

// Int returns a set OptInt.
func Int(v int64) OptInt { return OptInt{N: v, Valid: true} }

// ParseOptInt parses a decimal string; "" (after trimming) is unset.
func ParseOptInt(s string) (OptInt, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return OptInt{}, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return OptInt{}, fmt.Errorf("not an integer: %q", s)
	}
	return Int(v), nil
}

// Value implements driver.Valuer: unset is NULL.
func (o OptInt) Value() (driver.Value, error) {
	if !o.Valid {
		return nil, nil
	}
	return o.N, nil
}

// Scan implements sql.Scanner.
func (o *OptInt) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*o = OptInt{}
	case int64:
		*o = Int(v)
	default:
		return fmt.Errorf("mail: cannot scan %T into OptInt", src)
	}
	return nil
}

// MarshalJSON writes null when unset.
func (o OptInt) MarshalJSON() ([]byte, error) {
	if !o.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(o.N)
}

// UnmarshalJSON reads null or an integer.
func (o *OptInt) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*o = OptInt{}
		return nil
	}
	var v int64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*o = Int(v)
	return nil
}
