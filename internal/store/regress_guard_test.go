package store

import (
	"errors"
	"testing"
)

// Review 1a bug 1: the guard needed whitespace after UPDATE, and its quote
// scanner diverged from SQLite on $name(...) parameters.
func TestGuardRejectsWhitespacelessUpdateAndParameters(t *testing.T) {
	for _, sql := range []string{
		`WITH a AS (SELECT 1) UPDATE"messages"SET subject='pwn'`,
		`WITH a AS (SELECT 1) UPDATE[messages]SET subject='pwn'`,
		"WITH a AS (SELECT 1) UPDATE`messages`SET subject='pwn'",
		`SELECT $a(') ) ; DELETE FROM messages ; SELECT ($b(')`,
		`SELECT $a(') ) ; PRAGMA query_only=0 ; SELECT ($b(')`,
		`SELECT ?`, `SELECT :name`, `SELECT @p`, `SELECT ?1 + 1`, `SELECT #x`,
	} {
		if out, err := ValidateSQL(sql); !errors.Is(err, ErrUnsafeSQL) {
			t.Errorf("ValidateSQL(%q) accepted as %q", sql, out)
		}
	}
	for _, sql := range []string{`SELECT 'a:b?c@d$e#f' AS x`, `SELECT "a$b" FROM t`, `SELECT x FROM t WHERE subject LIKE '%?%'`} {
		if _, err := ValidateSQL(sql); err != nil {
			t.Errorf("parameter characters inside literals are data: %q: %v", sql, err)
		}
	}
}
