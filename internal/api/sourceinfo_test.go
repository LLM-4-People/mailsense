package api

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// plainSource is a connector with a chosen capability set and no owner action.
type plainSource struct {
	id   string
	kind string
	caps source.Capabilities
}

func (s plainSource) ID() string                                      { return s.id }
func (s plainSource) Kind() string                                    { return s.kind }
func (s plainSource) Capabilities() source.Capabilities               { return s.caps }
func (s plainSource) Discover(context.Context) ([]mail.Folder, error) { return nil, nil }
func (s plainSource) Mailboxes() []source.Mailbox                     { return nil }
func (s plainSource) Sync(context.Context, mail.Folder, source.Cursor, source.Sink) (source.Cursor, error) {
	return "", nil
}

// actionSource implements every optional owner action.
type actionSource struct{ plainSource }

func (actionSource) StartOutlook(context.Context) error { return nil }
func (actionSource) Resume() error                      { return nil }
func (actionSource) ReadDetails(context.Context, source.ItemRef) (mail.Details, error) {
	return mail.Details{}, nil
}

// namedSource has a configured owner-facing name.
type namedSource struct {
	plainSource
	name string
}

func (s namedSource) DisplayName() string { return s.name }

// probeSource reports a chosen live readiness.
type probeSource struct {
	plainSource
	ready source.Readiness
}

func (s probeSource) Probe(context.Context) source.Readiness { return s.ready }

// The source projection carries the capabilities and actions the connector
// actually declares, both always arrays.
func TestSourceInfoAdvertisesCapabilitiesAndActions(t *testing.T) {
	full := actionSource{plainSource{id: "outlook", kind: "outlook", caps: source.Capabilities{
		Incremental: true, Bodies: true, ReadState: true, Calendar: true}}}
	info := SourceInfoOf(full)
	if info.Capabilities == nil || info.Actions == nil {
		t.Fatal("capabilities and actions are always arrays")
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeMap(t, data)
	want := map[string]any{
		"id": "outlook", "kind": "outlook", "display_name": "outlook",
		"capabilities": []any{"incremental", "bodies", "read_state", "calendar"},
		"actions":      []any{"start", "resume", "retry-details"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("source %#v, want %#v", body, want)
	}

	plain := plainSource{id: "archive", kind: "archive"}
	plainData, err := json.Marshal(SourceInfoOf(plain))
	if err != nil {
		t.Fatal(err)
	}
	plainBody := decodeMap(t, plainData)
	plainWant := map[string]any{
		"id": "archive", "kind": "archive", "display_name": "archive",
		"capabilities": []any{}, "actions": []any{},
	}
	if !reflect.DeepEqual(plainBody, plainWant) {
		t.Fatalf("source %#v, want %#v", plainBody, plainWant)
	}
}

// The configured name is the label, not a humanized id.
func TestSourceInfoUsesTheConfiguredDisplayName(t *testing.T) {
	full := namedSource{plainSource{id: "outlook", kind: "outlook"}, "Work Outlook"}
	info := SourceInfoOf(full)
	if info.DisplayName != "Work Outlook" {
		t.Fatalf("display name %q, want Work Outlook", info.DisplayName)
	}
}

// The sources route carries the optional probe result; a connector without a
// probe remains available.
func TestSourceViewCarriesReadiness(t *testing.T) {
	ready := SourceViewOf(context.Background(), probeSource{
		plainSource: plainSource{id: "outlook", kind: "outlook"},
		ready: source.Readiness{Available: false, Cause: "outlook_wrong_profile",
			Detail: "Outlook is running with profile A", Profile: "A"},
	})
	if ready.Available || ready.Cause != "outlook_wrong_profile" || ready.Profile != "A" {
		t.Fatalf("ready %+v", ready)
	}
	plain := SourceViewOf(context.Background(), plainSource{id: "archive", kind: "archive"})
	if !plain.Available || plain.Cause != "" {
		t.Fatalf("plain %+v", plain)
	}
}

func TestSourcesRouteCarriesReadiness(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.sources.list = []SourceView{{
		SourceInfo: SourceInfo{ID: "outlook", Kind: "outlook", DisplayName: "Work Outlook",
			Capabilities: source.CapabilityList{}, Actions: []source.OwnerAction{source.ActionStart}},
		Available: false, Cause: "outlook_not_running", Detail: "classic Outlook is not running",
	}}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/sources"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	list, ok := body["sources"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("sources %#v", body["sources"])
	}
	got, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("source %#v", list[0])
	}
	if got["display_name"] != "Work Outlook" || got["available"] != false ||
		got["cause"] != "outlook_not_running" || got["detail"] != "classic Outlook is not running" {
		t.Fatalf("source %#v", got)
	}
}

// /api/status serves the same projection: the console reads capabilities and
// actions there instead of guessing from the kind.
func TestStatusRouteCarriesSourceCapabilities(t *testing.T) {
	srv, ts, w := newTestServer(t)
	full := actionSource{plainSource{id: "outlook", kind: "outlook", caps: source.Capabilities{Bodies: true, ReadState: true}}}
	w.status.view.Sources = []SourceInfo{SourceInfoOf(full)}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/status"})
	mustStatus(t, status, 200, data)
	sources, ok := decodeMap(t, data)["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("sources %#v", decodeMap(t, data)["sources"])
	}
	sourceBody, ok := sources[0].(map[string]any)
	if !ok {
		t.Fatalf("source %#v", sources[0])
	}
	if !reflect.DeepEqual(sourceBody["capabilities"], []any{"bodies", "read_state"}) {
		t.Fatalf("capabilities %#v", sourceBody["capabilities"])
	}
	if !reflect.DeepEqual(sourceBody["actions"], []any{"start", "resume", "retry-details"}) {
		t.Fatalf("actions %#v", sourceBody["actions"])
	}
}
