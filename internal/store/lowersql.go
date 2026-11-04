package store

import (
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// lowerSQL is mail.NormalizeAddress inside SQLite: the one comparison form
// of an address column. SQLite's built-in lower() folds ASCII only, while an
// address local part may carry any script, so a sender comparison uses this
// deterministic scalar and never lower(). No index depends on it: a sender
// filter scans the indexed page order and filters each row.
const lowerSQL = "mailsense_lower"

// domainSQL is mail.AddressDomain inside SQLite: the migration that backfills
// the stored sender domain and the writer that maintains it share the rule.
const domainSQL = "mailsense_address_domain"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(lowerSQL, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return mail.NormalizeAddress(v), nil
		case []byte:
			return mail.NormalizeAddress(string(v)), nil
		case nil:
			return "", nil
		}
		return nil, fmt.Errorf("%s: %T is not text", lowerSQL, args[0])
	})
	sqlite.MustRegisterDeterministicScalarFunction(domainSQL, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return mail.AddressDomain(v), nil
		case []byte:
			return mail.AddressDomain(string(v)), nil
		case nil:
			return "", nil
		}
		return nil, fmt.Errorf("%s: %T is not text", domainSQL, args[0])
	})
}
