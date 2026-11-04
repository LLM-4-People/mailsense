package logx

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// TestContentKeysAreDeniedByDefault: an attribute name that carries owner
// content is denied whatever its value type, through a group prefix and
// through a case or dash variant. Operational keys keep their values.
func TestContentKeysAreDeniedByDefault(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	denied := []string{
		"body", "subject", "sender", "sender_name", "sender_address",
		"recipient", "address", "email", "snippet", "content", "question",
		"answer", "mailbox_name", "store_name", "series_subject",
		"folder", "folder_path", "collection", "parent",
	}
	args := make([]any, 0, len(denied)*2+12)
	for _, k := range denied {
		args = append(args, k, "owner content for "+k)
	}
	safe := map[string]any{
		"view": "mail", "duration_ms": int64(12), "status": "ok", "method": "GET",
		"path": "/api/messages", "count": int64(3), "total": int64(7),
		"code": "none", "component": "syncer", "level": "info", "msg": "hello",
		"question_key": "category", "store_key": "store-abc", "series_id": "0123456789ab",
	}
	for _, k := range []string{"view", "duration_ms", "status", "method", "path", "count", "total", "code", "component", "level", "msg",
		"question_key", "store_key", "series_id"} {
		args = append(args, k, safe[k])
	}
	For("test.content").Info("safe message", args...)
	e := last(t, svc)

	for _, k := range denied {
		if e.Attrs[k] != config.SecretMask {
			t.Errorf("content key %q = %#v, want %q", k, e.Attrs[k], config.SecretMask)
		}
	}
	for k, want := range safe {
		if e.Attrs[k] != want {
			t.Errorf("safe key %q = %#v, want %#v", k, e.Attrs[k], want)
		}
	}

	// A content value under an integer, a group prefix, a flat dotted key, a
	// dash variant, a split compound and an uppercase key is denied too; a
	// safe sibling in the same group survives.
	For("test.content").Info("variants",
		"subject", 42,
		slog.Group("mail", "body", "secret body", "subject", "secret subject", "id", 9),
		"mail.body", "flat body",
		"sender-address", "alice@example.com",
		"mail-subject", "flat mail subject",
		"mail.subject", "dotted mail subject",
		"Mail Subject", "spaced mail subject",
		slog.Group("display", "name", "display name", "id", 8),
		slog.Group("mailbox", "name", "mailbox name"),
		slog.Group("store", "name", "store name"),
		slog.Group("DISPLAY", "NAME", "upper display name"),
		"SUBJECT", "upper subject")
	e = last(t, svc)
	for _, k := range []string{"subject", "mail.body", "mail.subject", "sender-address", "mail-subject",
		"Mail Subject", "display.name", "mailbox.name", "store.name", "DISPLAY.NAME", "SUBJECT"} {
		if e.Attrs[k] != config.SecretMask {
			t.Errorf("variant %q = %#v, want %q", k, e.Attrs[k], config.SecretMask)
		}
	}
	if e.Attrs["mail.id"] != int64(9) || e.Attrs["display.id"] != int64(8) {
		t.Errorf("safe group siblings changed: %#v", e.Attrs)
	}
}

// TestAddressCountSurvivesContentPolicy is the logx pipeline contract for the
// operational count that the scope and Outlook discovery logs emit: the count
// is not content, while mailbox/store/display names are.
func TestAddressCountSurvivesContentPolicy(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	For("test.count").Info("scope", "address_count", 5, "address", "alice@example.com",
		"mailbox_name", "Amélie", "store_name", "Alex's mailbox", "display.name", "Owner Name")
	e := last(t, svc)
	if e.Attrs["address_count"] != int64(5) {
		t.Errorf("address_count = %#v, want 5", e.Attrs["address_count"])
	}
	for _, k := range []string{"address", "mailbox_name", "store_name", "display.name"} {
		if e.Attrs[k] != config.SecretMask {
			t.Errorf("content key %q = %#v, want %q", k, e.Attrs[k], config.SecretMask)
		}
	}
}

// TestEmailAddressesAreRedactedByDefault: addresses in a message and in a
// safe attribute value are replaced whole; non-address text that merely
// contains "noreply" or "@" is not mangled.
func TestEmailAddressesAreRedactedByDefault(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	For("test.email").Info(
		"write to alice@example.com or bob@example.org now",
		"actor", "carol@example.net",
		"unicode_actor", "李娜@例子.中国",
		"host_actor", "user@host",
		"note", "noreply",
		"at", "@",
		"spaced", "noreply @ example",
		"count", 3,
		"token_count", 12,
	)
	e := last(t, svc)
	if e.Message != "write to "+config.SecretMask+" or "+config.SecretMask+" now" {
		t.Errorf("message = %q", e.Message)
	}
	for _, k := range []string{"actor", "unicode_actor", "host_actor"} {
		if e.Attrs[k] != config.SecretMask {
			t.Errorf("%s = %#v, want %q", k, e.Attrs[k], config.SecretMask)
		}
	}
	for k, want := range map[string]any{
		"note": "noreply", "at": "@", "spaced": "noreply @ example",
		"count": int64(3), "token_count": int64(12),
	} {
		if e.Attrs[k] != want {
			t.Errorf("edge %s = %#v, want %#v", k, e.Attrs[k], want)
		}
	}

	For("test.email").Info("alice@example.com")
	e = last(t, svc)
	if e.Message != config.SecretMask {
		t.Errorf("a whole address became %q, want one marker", e.Message)
	}
	if strings.Contains(e.Message, "alice") || strings.Contains(e.Message, "example.com") {
		t.Errorf("address leaked partially: %q", e.Message)
	}
}
