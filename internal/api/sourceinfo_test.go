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
		"id": "outlook", "kind": "outlook",
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
		"id": "archive", "kind": "archive",
		"capabilities": []any{}, "actions": []any{},
	}
	if !reflect.DeepEqual(plainBody, plainWant) {
		t.Fatalf("source %#v, want %#v", plainBody, plainWant)
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
