package api

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestGuardsHaveOneOwner: the held-removal availability check, the secret-name
// check, the invalid-parameter pair and every 404 constructor are each spelled
// once. A second copy is how the same wire answer drifts between two routes.
func TestGuardsHaveOneOwner(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
		once []string
		ban  []string
	}{
		{
			file: "routes_held.go",
			want: []string{"requireHeldRemovals(w, r)"},
			once: []string{"held removals are unavailable"},
		},
		{
			file: "routes_secrets.go",
			want: []string{"s.secretName(w, r)"},
			once: []string{"invalid secret name"},
		},
		{
			file: "routes_thread.go",
			want: []string{"invalidParam(name)"},
			ban:  []string{"&APIError{Status: http.StatusNotFound"},
		},
		{
			file: "errors.go",
			want: []string{"notFoundWith(\"unknown_thread\", \"unknown thread\")", "conversations.ErrInvalidState", "commitments.ErrInvalidKind"},
		},
		{
			file: "routes_jobs.go",
			want: []string{"notFoundWith(\"not_found\", \"no such job\")"},
			ban:  []string{"&APIError{Status: http.StatusNotFound"},
		},
		{
			file: "routes_logs.go",
			want: []string{"notFoundWith(\"log_not_found\", \"no such log entry\")"},
			ban:  []string{"&APIError{Status: http.StatusNotFound"},
		},
		{
			file: "routes_actions.go",
			want: []string{"notFoundWith(\"not_found\", \"no such action\")"},
			ban:  []string{"&APIError{Status: http.StatusNotFound"},
		},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, want := range c.want {
			if !strings.Contains(source, want) {
				t.Errorf("%s lacks %q", c.file, want)
			}
		}
		for _, once := range c.once {
			if got := strings.Count(source, once); got != 1 {
				t.Errorf("%s spells %q %d times, want once", c.file, once, got)
			}
		}
		for _, ban := range c.ban {
			if strings.Contains(source, ban) {
				t.Errorf("%s keeps %q; the shared owner is the only spelling", c.file, ban)
			}
		}
	}
}

// TestNotFoundCodesAreStable checks the messages the shell localizes: every
// route answers its documented code and message, from one constructor.
func TestNotFoundCodesAreStable(t *testing.T) {
	for _, c := range []struct{ code, message string }{
		{"not_found", "not found"},
		{"not_found", "no such job"},
		{"not_found", "no such action"},
		{"log_not_found", "no such log entry"},
		{"unknown_thread", "unknown thread"},
	} {
		got := notFoundWith(c.code, c.message)
		if got.Code != c.code || got.Message != c.message || got.Status != 404 {
			t.Errorf("notFoundWith(%q, %q) = %+v", c.code, c.message, got)
		}
	}
	if got := invalidBefore(); got.Code != "invalid_before" || got.Message != "invalid before" {
		t.Errorf("invalidBefore() = %+v", got)
	}
}

// TestServiceErrorsMapAtTheOneOwner: the sentinels the handlers used to map
// locally answer the same stable problem from mapError, so a new route cannot
// answer 500 for a validation failure.
func TestServiceErrorsMapAtTheOneOwner(t *testing.T) {
	srv := &Server{}
	for _, c := range []struct {
		err    error
		code   string
		status int
	}{
		{store.ErrInvalidRole, "invalid_role", 400},
		{store.ErrInvalidLimit, "invalid_limit", 400},
		{store.ErrInvalidStore, "invalid_store", 400},
		{store.ErrInvalidSender, "invalid_sender", 400},
		{store.ErrInvalidCursor, "invalid_cursor", 400},
		{store.ErrThreadNotFound, "unknown_thread", 404},
		{store.ErrUnknownCompany, "unknown_company", 404},
		{store.ErrInvalidConversationID, "invalid_thread", 400},
		{store.ErrInvalidThreadCursor, "invalid_before", 400},
		{conversations.ErrInvalidLimit, "invalid_limit", 400},
		{conversations.ErrInvalidCursor, "invalid_cursor", 400},
		{conversations.ErrInvalidState, "invalid_state", 400},
		{conversations.ErrInvalidQuery, "invalid_query", 400},
		{commitments.ErrInvalidKind, "invalid_kind", 400},
		{commitments.ErrInvalidDirection, "invalid_direction", 400},
		{commitments.ErrInvalidStatus, "invalid_status", 400},
		{commitments.ErrInvalidLimit, "invalid_limit", 400},
	} {
		got := srv.mapError(c.err)
		if got.Code != c.code || got.Status != c.status {
			t.Errorf("mapError(%v) = %d %q, want %d %q", c.err, got.Status, got.Code, c.status, c.code)
		}
	}
	for _, name := range []string{"writeMessageListError", "writeCommitmentsError"} {
		if strings.Contains(readAPISource(t, "routes_messages.go")+readAPISource(t, "routes_commitments.go"), "func (s *Server) "+name+"(") {
			t.Errorf("a per-handler error mapper %s is back; extend mapError", name)
		}
	}
	// The facet name is part of the wire contract the filter controls read.
	for name, err := range map[string]error{
		"kind":      commitments.ErrInvalidKind,
		"direction": commitments.ErrInvalidDirection,
		"status":    commitments.ErrInvalidStatus,
	} {
		if got := srv.mapError(err).Params["name"]; got != name {
			t.Errorf("mapError(%v) names %q, want %q", err, got, name)
		}
	}
}

// readAPISource reads one file of this package for a source guard.
func readAPISource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSettingExistenceHasOneOwner: the detail, write and reset routes ask the
// same helper, so an unknown key answers one 404 problem everywhere.
func TestSettingExistenceHasOneOwner(t *testing.T) {
	source := readAPISource(t, "routes_settings.go")
	if got := strings.Count(source, "s.services.Settings.Get("); got != 1 {
		t.Errorf("routes_settings.go checks the setting %d times, want once in requireSetting", got)
	}
	if got := strings.Count(source, "s.requireSetting(key)"); got != 3 {
		t.Errorf("routes_settings.go calls requireSetting %d times, want detail, write and reset", got)
	}
	if strings.Contains(source, "ErrUnknownSetting()") && strings.Count(source, "ErrUnknownSetting()") != 1 {
		t.Errorf("routes_settings.go constructs the unknown-setting problem outside requireSetting")
	}
}
