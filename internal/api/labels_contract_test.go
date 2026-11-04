package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// labelerService is the real label batch service behind the API. The
// contract test exercises the one validation owner instead of a fake that
// can only echo the handler's copy of the rules.
type labelerService struct {
	*fakeLabels
	labeler *feedback.Labeler
	profile *taxonomy.Profile
}

func (s labelerService) PutLabels(ctx context.Context, items []feedback.PutItem) (feedback.PutResult, error) {
	return s.labeler.PutBatch(ctx, s.profile, items)
}

func (s labelerService) DeleteLabel(ctx context.Context, messageID int64, question string) (feedback.PutResult, error) {
	return s.labeler.Delete(ctx, s.profile, messageID, question)
}

// labelWorld builds the API over a real store and labeler, with n visible
// messages, and returns the message row ids.
func labelWorld(t *testing.T, n int) (*Server, *httptest.Server, *world, *store.Store, []int64) {
	t.Helper()
	cfg, st := fixturetest.OpenStore(t, nil)
	msgs := make([]mail.Message, n)
	for i := range n {
		msgs[i] = mailtest.Message("src", fmt.Sprintf("m%d", i))
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err := st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		ids = append(ids, r.RowID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := taxonomy.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld()
	w.services.Labels = labelerService{fakeLabels: w.labels, labeler: feedback.NewLabeler(st, nil), profile: p}
	opts := testOptions(t)
	opts.MaxBytes = 1 << 16 // an oversized batch must reach the batch bound, not the body bound
	srv, ts, w := newTestServerWith(t, opts, w)
	return srv, ts, w, st, ids
}

func labelCount(t *testing.T, st *store.Store) int {
	t.Helper()
	rows, err := st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// The wire contract of a label batch: the handler refuses only the request's
// shape and bound (400 invalid_request), and every per-item rule is the
// labeler's typed refusal (422 invalid_value, one problem per item). A
// rejected batch, mixed or not, stores nothing.
func TestLabelBatchStatusContract(t *testing.T) {
	srv, ts, w, st, ids := labelWorld(t, 2)
	item := func(id int64, question string, value any) map[string]any {
		return map[string]any{"message_id": id, "question": question, "value": value}
	}
	batch := func(items []map[string]any) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{"items": items})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	oversized := make([]map[string]any, 0, feedback.MaxLabelBatch+1)
	for range feedback.MaxLabelBatch + 1 {
		oversized = append(oversized, item(ids[0], "category", "work"))
	}
	cases := []struct {
		name   string
		items  []map[string]any
		status int
		code   string
	}{
		{"empty items", []map[string]any{}, 400, "invalid_request"},
		{"oversized batch", oversized, 400, "invalid_request"},
		{"zero message id", []map[string]any{item(0, "category", "work")}, 422, "invalid_value"},
		{"empty question", []map[string]any{item(ids[0], "", "work")}, 422, "invalid_value"},
		{"unknown question", []map[string]any{item(ids[0], "not_a_question", "work")}, 422, "invalid_value"},
		{"missing value", []map[string]any{{"message_id": ids[0], "question": "category"}}, 422, "invalid_value"},
		{"whitespace-only value", []map[string]any{item(ids[0], "category", "   ")}, 422, "invalid_value"},
		{"duplicate pair", []map[string]any{
			item(ids[0], "category", "work"),
			item(ids[0], "category", "spam"),
		}, 422, "invalid_value"},
		{"invalid value", []map[string]any{item(ids[0], "category", map[string]any{"category": "work"})}, 422, "invalid_value"},
		{"invisible message id", []map[string]any{item(999999, "category", "work")}, 422, "invalid_value"},
		{"mixed valid and invalid", []map[string]any{
			item(ids[0], "category", "work"),
			item(ids[1], "not_a_question", true),
		}, 422, "invalid_value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/labels", body: batch(tc.items)})
			mustStatus(t, status, tc.status, data)
			if got := errorCode(t, data); got != tc.code {
				t.Fatalf("code %q, want %q: %s", got, tc.code, data)
			}
			envelope := decodeMap(t, data)
			apiErr, _ := envelope["error"].(map[string]any)
			problems, _ := apiErr["problems"].([]any)
			switch tc.status {
			case 422:
				if len(problems) == 0 {
					t.Fatalf("a 422 carries no problem: %s", data)
				}
			case 400:
				if len(problems) != 0 {
					t.Fatalf("a shape refusal carries item problems: %s", data)
				}
			}
			if got := labelCount(t, st); got != 0 {
				t.Fatalf("a rejected batch wrote %d labels", got)
			}
		})
	}
	if w.labels.putCalls != 0 {
		t.Fatalf("the API bypassed the real labeler: %d fake calls", w.labels.putCalls)
	}

	// The success path keeps its wire shape exactly: PutResult's stored
	// items, manual source, in request order.
	accepted := batch([]map[string]any{
		item(ids[0], "category", "work"),
		item(ids[1], "reply_needed", true),
	})
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/labels", body: accepted})
	mustStatus(t, status, 200, data)
	var got feedback.PutResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	want := []feedback.StoredLabel{
		{MessageID: ids[0], Question: "category", Source: store.LabelManual},
		{MessageID: ids[1], Question: "reply_needed", Source: store.LabelManual},
	}
	if !reflect.DeepEqual(got.Stored, want) {
		t.Fatalf("stored %+v, want %+v", got.Stored, want)
	}
	if envelope := decodeMap(t, data); len(envelope) != 1 {
		t.Fatalf("PutResult gained a field: %s", data)
	}
	if n := labelCount(t, st); n != 2 {
		t.Fatalf("the accepted batch stored %d labels", n)
	}
}
