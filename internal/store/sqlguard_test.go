package store

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSQLAccepts(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":                  "SELECT 1",
		"  select 1  ":              "select 1",
		"SELECT 1;":                 "SELECT 1",
		"SELECT 1;;  ; ":            "SELECT 1",
		"-- lead\nSELECT 1 -- tail": "SELECT 1",
		"SELECT 1/*x*/FROM t":       "SELECT 1 FROM t",
		"WITH x AS (SELECT 1 AS a) SELECT * FROM x": "WITH x AS (SELECT 1 AS a) SELECT * FROM x",
	}
	for in, want := range cases {
		if got, err := ValidateSQL(in); err != nil || got != want {
			t.Errorf("ValidateSQL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Literals and quoted identifiers are data; keyword-containing names are fine.
	for _, sql := range []string{
		"SELECT ';' AS x",
		"SELECT id FROM messages WHERE body LIKE '%;%'",
		"SELECT 'drop table create delete pragma vacuum update x set y' AS x",
		`SELECT "a;b" AS x`,
		"SELECT [weird;name] FROM t",
		"SELECT COUNT(*) AS drop_count FROM messages",
		"SELECT id FROM messages ORDER BY create_month DESC",
		"SELECT id FROM messages WHERE subject LIKE 'DROP%'",
		"SELECT id, updates FROM messages",
		"WITH dropped AS (SELECT 1 AS x) SELECT count(*) FROM dropped",
		"WITH p AS (SELECT * FROM pragma_table_info('messages')) SELECT count(*) FROM p",
		"SELECT 'it''s' AS escaped",
	} {
		if got, err := ValidateSQL(sql); err != nil || got != sql {
			t.Errorf("ValidateSQL(%q) = %q, %v", sql, got, err)
		}
	}
}

func TestValidateSQLRejects(t *testing.T) {
	bad := []string{
		"", "   ", "-- nothing", "/* only */", ";",
		"SELECT 1; SELECT 2", "SELECT 1; DROP TABLE messages", "SELECT 1 /* x */ ; DELETE FROM messages",
		"INSERT INTO messages (id) VALUES ('x')", "UPDATE messages SET subject = 'x'", "DELETE FROM messages",
		"DROP TABLE messages", "PRAGMA table_info(messages)", "ATTACH DATABASE 'x.db' AS other", "VALUES (1)",
		"EXPLAIN SELECT 1", "REPLACE INTO messages VALUES (1)",
		"WITH x AS (SELECT 1) INSERT INTO messages SELECT * FROM x",
		"WITH x AS (SELECT 1) UPDATE messages SET subject = 'x'",
		"WITH x AS (SELECT 1) DELETE FROM messages",
		"SELECT * FROM (INSERT INTO messages VALUES (1))",
		"SELECT * FROM (DROP TABLE messages)", "SELECT * FROM (ALTER TABLE messages RENAME TO x)",
		"SELECT * FROM (CREATE TABLE x (a INT))", "SELECT 1 FROM (ATTACH DATABASE 'x' AS y)",
		"SELECT 1 FROM (DETACH y)", "SELECT 1 FROM (PRAGMA foo)", "SELECT 1 FROM (VACUUM)",
		"WITH i AS (insert/*c*/into messages (id) values ('z') returning id) SELECT count(*) FROM i",
		"WITH u AS (update/*c*/or ignore messages set subject='x' returning id) SELECT count(*) FROM u",
		"WITH u AS (UPDATE OR ABORT  messages SET subject='x' RETURNING id) SELECT count(*) FROM u",
		`WITH u AS (UPDATE "messages"    SET subject='x' RETURNING id) SELECT count(*) FROM u`,
		"WITH u AS (UPDATE main.messages SET subject='x' RETURNING id) SELECT count(*) FROM u",
		`WITH u AS (UPDATE "main"."messages" SET subject='x' RETURNING id) SELECT 1 FROM u`,
		"WITH u AS (UPDATE [messages] SET subject='x' RETURNING id) SELECT 1 FROM u",
		"WITH u AS (UPDATE messages AS m SET subject='x' RETURNING id) SELECT 1 FROM u",
		"WITH u AS (UPDATE messages INDEXED BY messages_sender SET subject='x' RETURNING id) SELECT 1 FROM u",
		"WITH u AS (UPDATE\nmessages\nSET subject='x' RETURNING id) SELECT 1 FROM u",
		"SELECT 'abc AS x", `SELECT "abc AS x`, "SELECT `abc AS x", "SELECT [abc AS x",
		"SELECT * FROM (DELETE FROM messages WHERE a = 'unclosed)",
		"SELECT 1 --\n; DROP TABLE x",
	}
	for _, v := range []string{"IGNORE", "REPLACE", "ROLLBACK", "ABORT", "FAIL", "ANYWORD"} {
		bad = append(bad, "SELECT * FROM (INSERT OR "+v+" INTO messages VALUES (1))")
	}
	for _, sql := range bad {
		if got, err := ValidateSQL(sql); !errors.Is(err, ErrUnsafeSQL) {
			t.Errorf("ValidateSQL(%q) accepted as %q (err %v)", sql, got, err)
		}
	}
	if _, err := ValidateSQL("SELECT 'x"); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("unterminated quote must say so: %v", err)
	}
}

func FuzzValidateSQLNeverPanics(f *testing.F) {
	for _, s := range []string{"SELECT 1", "WITH a AS (SELECT 1) SELECT * FROM a", "SELECT '", "/*", "--", "[", "UPDATE x SET"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		out, err := ValidateSQL(sql)
		if err == nil && (strings.Contains(out, ";") && !strings.ContainsAny(out, `'"`+"`[")) {
			t.Fatalf("accepted multi-statement %q", out)
		}
	})
}
