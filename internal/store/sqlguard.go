package store

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnsafeSQL wraps every rejection of the read-only SQL validator.
var ErrUnsafeSQL = errors.New("store: SQL rejected")

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUnsafeSQL}, args...)...)
}

// scanSQL is the one quote-aware scanner. Comments become a SPACE, never
// nothing ("insert/*c*/into" must not become "insertinto", and
// "SELECT 1/*x*/FROM t" must stay valid). With blank set, literal and quoted
// identifier CONTENT becomes spaces so data can never trip the checks.
// An unterminated quote is an error, never a silent blank-to-end.
func scanSQL(sql string, blank bool) (string, error) {
	var out strings.Builder
	var closer byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if closer != 0 {
			if c == closer {
				closer = 0
				out.WriteByte(c)
			} else if blank {
				out.WriteByte(' ')
			} else {
				out.WriteByte(c)
			}
			continue
		}
		switch {
		case c == '\'' || c == '"' || c == '`':
			closer = c
			out.WriteByte(c)
		case c == '[':
			closer = ']'
			out.WriteByte(c)
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			out.WriteByte(' ')
			nl := strings.IndexByte(sql[i:], '\n')
			if nl < 0 {
				return out.String(), nil
			}
			out.WriteByte('\n')
			i += nl
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			out.WriteByte(' ')
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return out.String(), nil
			}
			i += 2 + end + 1
		default:
			out.WriteByte(c)
		}
	}
	if closer != 0 {
		return "", rejected("unterminated %c quote", closer)
	}
	return out.String(), nil
}

// mutationRE is the read-only guard, run on the literal-blanked statement.
// UPDATE is matched up to its SET however the table is written — with or
// without whitespace (UPDATE"t"SET, UPDATE[t]SET), OR <action>, quoted,
// bracketed, schema-qualified, AS alias, INDEXED BY …. Anchoring the other
// keywords on the statement shape keeps "drop_count" or LIKE 'DROP%' from
// being rejected.
var mutationRE = regexp.MustCompile(`(?i)\b(insert\s+(or\s+\w+\s+)?into` +
	`|update\b[\s\S]*?\bset` +
	`|delete\s+from|replace\s+into` +
	`|drop|alter|create|attach|detach|pragma|vacuum)\b`)

// paramRE finds bind-parameter syntax outside literals. The runner never
// binds parameters, and SQLite's tokenizer reads $name(...) suffixes that may
// contain quotes, so any parameter makes the quote scan unreliable.
var paramRE = regexp.MustCompile(`[?:@$#]`)

var readOnlyStartRE = regexp.MustCompile(`(?is)^(select|with)\b`)

// ValidateSQL allows exactly one read-only statement (SELECT/WITH) and
// returns it without comments or a trailing semicolon. It is the first line
// of defence; the runner also uses a query_only, read-only connection.
func ValidateSQL(sql string) (string, error) {
	stripped, err := scanSQL(sql, false)
	if err != nil {
		return "", err
	}
	stmt := strings.TrimSpace(stripped)
	for strings.HasSuffix(stmt, ";") {
		stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	}
	if stmt == "" {
		return "", rejected("empty statement")
	}
	blanked, err := scanSQL(stmt, true)
	if err != nil {
		return "", err
	}
	if p := paramRE.FindString(blanked); p != "" {
		return "", rejected("bind parameters (%q) are not allowed", p)
	}
	if strings.Contains(blanked, ";") {
		return "", rejected("only a single statement is allowed")
	}
	if !readOnlyStartRE.MatchString(stmt) {
		return "", rejected("only SELECT/WITH queries are allowed")
	}
	if m := mutationRE.FindString(blanked); m != "" {
		return "", rejected("data-modifying or schema statement %q is not allowed", strings.Join(strings.Fields(m), " "))
	}
	return stmt, nil
}
