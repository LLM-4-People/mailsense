//go:build fixture

package store

// BenchmarkRouteCoverage measures the triage summary's one aggregate read
// over a prepared fixture store (scripts/bench-layers.sh: PERF_STORE_DIR).
// One iteration scans the whole mailbox, so run with -benchtime=1x or 3x.

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// benchCoverageInput is the coverage input the triage summary uses: the
// route key, the triage folder roles and the profile's questions, read from
// one stored answer so the benchmark measures the live shape.
func benchCoverageInput(b testing.TB, s *Store) (string, []mail.FolderRole, []QuestionKey) {
	b.Helper()
	var fingerprint string
	if err := s.db.QueryRowContext(b.Context(), `SELECT fingerprint FROM route_answers LIMIT 1`).Scan(&fingerprint); err != nil {
		b.Fatalf("no route answers in the prepared store: %v", err)
	}
	var raw string
	if err := s.db.QueryRowContext(b.Context(), `SELECT answers FROM route_answers WHERE fingerprint = ? LIMIT 1`, fingerprint).Scan(&raw); err != nil {
		b.Fatal(err)
	}
	var answers map[string]struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &answers); err != nil {
		b.Fatal(err)
	}
	questions := make([]QuestionKey, 0, len(answers))
	for name, a := range answers {
		questions = append(questions, QuestionKey{Name: name, Type: a.Type})
	}
	var roles []mail.FolderRole
	for _, r := range s.cfg.Strings("triage.folder_roles") {
		roles = append(roles, mail.FolderRole(r))
	}
	return fingerprint, roles, questions
}

func BenchmarkRouteCoverage(b *testing.B) {
	s := benchStore(b)
	fingerprint, roles, questions := benchCoverageInput(b, s)
	b.ResetTimer()
	for range b.N {
		// The prepared fixture has no triage failures; the failure key only
		// drives the primary-key lookup this benchmark measures.
		c, err := s.RouteCoverage(b.Context(), fingerprint, fingerprint, "benchmark", MessageFilter{Roles: roles, OwnerOnly: true}, questions)
		if err != nil {
			b.Fatal(err)
		}
		if c.Total == 0 {
			b.Fatal("no covered messages")
		}
	}
}
