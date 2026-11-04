package api

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// nilListSettings is a settings service whose List answers nil, the way a
// fresh projection can: the handler's nonNilSlice is what keeps the wire
// contract ([] not null). The embedded fake provides every other method.
type nilListSettings struct{ *fakeSettings }

func (nilListSettings) List() ([]SettingView, []string) { return nil, nil }

// emptyLogs answers a zero snapshot, whose Entries is nil.
type emptyLogs struct{ *fakeLogs }

func (emptyLogs) Snapshot(LogQuery) (LogSnapshot, error) { return LogSnapshot{}, nil }

// valueAt walks a decoded JSON object by key; a missing key fails the test.
func valueAt(t *testing.T, body map[string]any, path ...string) any {
	t.Helper()
	var cur any = body
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v: %q is not inside an object", path, key)
		}
		cur, ok = obj[key]
		if !ok {
			t.Fatalf("%v: %q is missing", path, key)
		}
	}
	return cur
}

// assertArray requires an array of any length: used where the point is the
// shape (never null), not emptiness.
func assertArray(t *testing.T, body map[string]any, path ...string) []any {
	t.Helper()
	value := valueAt(t, body, path...)
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%v = %#v, want an array (null is not a list)", path, value)
	}
	return items
}

func assertEmptyArray(t *testing.T, body map[string]any, path ...string) {
	t.Helper()
	if items := assertArray(t, body, path...); len(items) != 0 {
		t.Fatalf("%v = %v, want []", path, items)
	}
}

func assertEmptyObject(t *testing.T, body map[string]any, path ...string) {
	t.Helper()
	value := valueAt(t, body, path...)
	fields, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%v = %#v, want an object (null is not an empty object)", path, value)
	}
	if len(fields) != 0 {
		t.Fatalf("%v = %v, want {}", path, fields)
	}
}

// TestListEndpointsNeverAnswerNull drives every endpoint that serves a list
// (and the two objects beside them) with a service that returns nothing at all.
// nonNilSlice/nonNilMap are the one rule that turns that into [] and {}, so the
// shell can always map over a field instead of testing it for null.
func TestListEndpointsNeverAnswerNull(t *testing.T) {
	w := newWorld()
	w.status.view = StatusView{}
	w.jobs.list = nil
	w.held.folders = nil
	w.usage.rows = nil
	w.mailboxes.list, w.mailboxes.boxes = nil, nil
	w.secrets.view = SecretsView{Kind: "dpapi"}
	w.providers.list, w.providers.roles = nil, nil
	w.sidecar.status = SidecarStatus{}
	w.triage.questions = QuestionsView{}
	w.boot.views = nil
	w.commitments.res = commitments.Result{}
	w.brief.res = brief.Result{Must: brief.Section{Items: []brief.Item{{ID: "m1", Kind: "message"}}}}
	w.threads.view = ThreadView{ConversationID: "C1"}
	w.messages.list = MessageListView{}
	w.ask.history = nil
	w.settings.learned = nil
	w.services.Settings = nilListSettings{w.settings}
	w.services.Logs = emptyLogs{w.logs}

	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	for _, tc := range []struct {
		name     string
		path     string
		arrays   [][]string
		nonEmpty [][]string
		objects  [][]string
	}{
		{name: "ask history", path: "/api/ask/history", arrays: [][]string{{"items"}}},
		{name: "boot", path: "/api/boot", arrays: [][]string{{"views"}}},
		{name: "brief", path: "/api/brief", nonEmpty: [][]string{{"must", "items"}}, arrays: [][]string{
			{"waiting", "items"}, {"changed", "items"}, {"relationships", "items"}, {"topics", "items"},
		}},
		{name: "commitments", path: "/api/commitments", arrays: [][]string{{"items"}}, objects: [][]string{{"counts"}}},
		{name: "held removals", path: "/api/held-removals", arrays: [][]string{{"folders"}}},
		{name: "jobs", path: "/api/jobs", arrays: [][]string{{"jobs"}}},
		{name: "logs", path: "/api/logs", arrays: [][]string{{"entries"}}},
		{name: "mailboxes", path: "/api/mailboxes", arrays: [][]string{{"mailboxes"}}},
		{name: "all mail", path: "/api/messages", arrays: [][]string{{"messages"}}},
		{name: "providers", path: "/api/providers", arrays: [][]string{{"providers"}, {"roles"}}},
		{name: "questions", path: "/api/questions", arrays: [][]string{{"questions"}}},
		{name: "secrets", path: "/api/secrets", arrays: [][]string{{"names"}, {"expected"}}},
		{name: "settings", path: "/api/settings", arrays: [][]string{{"settings"}, {"restart_pending"}}},
		{name: "learned settings", path: "/api/settings/learned", arrays: [][]string{{"learned"}}},
		{name: "sidecar", path: "/api/sidecar", arrays: [][]string{{"builds"}, {"runtimes"}}},
		{name: "status", path: "/api/status", arrays: [][]string{{"sources"}, {"restart_pending"}}, objects: [][]string{{"store_meta"}}},
		{name: "usage", path: "/api/usage", arrays: [][]string{{"rows"}}},
		{name: "thread", path: "/api/threads/C1", arrays: [][]string{{"messages"}, {"participants"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			if code != 200 {
				t.Fatalf("%s: status %d: %s", tc.path, code, data)
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			for _, path := range tc.arrays {
				assertEmptyArray(t, body, path...)
			}
			for _, path := range tc.nonEmpty {
				assertArray(t, body, path...)
			}
			for _, path := range tc.objects {
				assertEmptyObject(t, body, path...)
			}
			if tc.name == "brief" {
				// The one scripted row proves the nested evidence list is
				// normalized too, not only the section's items.
				items := assertArray(t, body, "must", "items")
				if len(items) != 1 {
					t.Fatalf("must.items = %v, want the one scripted row", items)
				}
				row, ok := items[0].(map[string]any)
				if !ok {
					t.Fatalf("must.items[0] = %#v", items[0])
				}
				assertEmptyArray(t, row, "evidence")
			}
		})
	}
}

// TestWireMessageAlwaysCarriesAnAttachmentsArray: the message detail is the one
// place the wire projection itself owns the array, so a message with no
// attachments still carries [] instead of null.
func TestWireMessageAlwaysCarriesAnAttachmentsArray(t *testing.T) {
	view := WireMessage(&mail.Message{ID: "m1", Subject: "no files"})
	if view == nil {
		t.Fatal("a message produced no view")
	}
	if view.Attachments == nil || len(view.Attachments) != 0 {
		t.Fatalf("attachments = %#v, want []", view.Attachments)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	assertEmptyArray(t, body, "attachments")
}
