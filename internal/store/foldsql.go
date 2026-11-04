package store

import (
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// foldSQL is mail.Fold inside SQLite (registered once with the driver's
// function registry): the scan of short substring terms compares the
// folded text with the folded term, as both indexes do.
const foldSQL = "mailsense_fold"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(foldSQL, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return mail.Fold(v), nil
		case []byte:
			return mail.Fold(string(v)), nil
		case nil:
			return "", nil
		}
		return nil, fmt.Errorf("%s: %T is not text", foldSQL, args[0])
	})
}

// scanTermSQL matches one folded term (bound four times) as a substring of
// the same fields the full-text indexes hold (ftsFields): subject, body,
// sender, recipients.
const scanTermSQL = `(instr(` + foldSQL + `(m.subject), ?) > 0 OR instr(` + foldSQL + `(m.body), ?) > 0
	OR instr(` + foldSQL + `(m.sender_name || ' ' || m.sender_address || ' ' || m.sent_representing_address), ?) > 0
	OR EXISTS (SELECT 1 FROM recipients r WHERE r.message_id = m.id AND instr(` + foldSQL + `(r.name || ' ' || r.address), ?) > 0))`
