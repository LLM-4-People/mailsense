package qa

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestParseRoute(t *testing.T) {
	cases := []struct {
		raw  string
		want Route
		ok   bool
	}{
		{"", RouteAuto, true},
		{"auto", RouteAuto, true},
		{" AUTO ", RouteAuto, true},
		{"local", RouteLocal, true},
		{"remote", RouteRemote, true},
		{"Local", RouteLocal, true},
		{"remote ", RouteRemote, true},
		{"offline", "", false},
		{"auto remote", "", false},
	}
	for _, tc := range cases {
		got, err := ParseRoute(tc.raw)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("ParseRoute(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidRoute) || got != "" {
			t.Errorf("ParseRoute(%q) = %q, %v; want ErrInvalidRoute", tc.raw, got, err)
		}
	}
}

func TestAskRejectsAnUnknownRoute(t *testing.T) {
	w := newWorld(t, false)
	_, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "what is urgent?", Language: "en", Route: Route("sideways"),
	})
	if !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("err = %v, want ErrInvalidRoute", err)
	}
	if w.gate.calls != 0 || w.store.nextID != 0 {
		t.Fatal("an invalid route reached the gate or history")
	}
}

func TestAskLocalNeverCallsTheGenerator(t *testing.T) {
	w := newWorld(t, false)
	w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(1, "urgent"), item(2, "urgent")}
	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "what is urgent?", Language: "en", Route: RouteLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Local called the generator %d times", w.gen.calls)
	}
	if view.Route != RouteLocal || view.Mode != ModeStructured || view.Intent != IntentUrgent {
		t.Fatalf("view = %+v", view)
	}
	if view.Counts["urgent"] != 2 || len(view.Citations) != 2 {
		t.Fatalf("counts=%v citations=%d", view.Counts, len(view.Citations))
	}
	if got := w.store.saved[0].Route; got != string(RouteLocal) {
		t.Fatalf("stored route = %q", got)
	}
}

func TestAskLocalFreeformReturnsSourcesWithAnHonestNotice(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Plan", SenderName: "Bob",
		SenderAddress: "bob@example.com", Instant: "2026-10-25T10:00:00Z", Body: "The plan is ready."}
	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "tell me about the plan", Language: "en", Route: RouteLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Local called the generator %d times", w.gen.calls)
	}
	if view.Route != RouteLocal || view.Mode != ModeRetrieval || view.Notice != NoticeLocalNoAnswer {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("citations = %+v", view.Citations)
	}
}

func TestAskRemoteRequiresAConfiguredGenerator(t *testing.T) {
	w := newWorld(t, false)
	w.source.gen, w.source.err = nil, ErrUnavailable
	_, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "what is urgent?", Language: "en", Route: RouteRemote,
	})
	if !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("err = %v, want ErrRouteUnavailable", err)
	}
	var routeErr *RouteUnavailableError
	if !errors.As(err, &routeErr) || routeErr.Route != RouteRemote || routeErr.Reason == "" {
		t.Fatalf("typed route error = %#v", routeErr)
	}
	if w.gen.calls != 0 || w.store.nextID != 0 {
		t.Fatal("an unavailable remote route called the model or saved history")
	}
}

func TestUnwrapAnswerEnvelope(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", "The plan is ready [1].", "The plan is ready [1].", true},
		{"citation-first prose", "[1] The invoice was approved.", "[1] The invoice was approved.", true},
		{"braced prose", "{see note} The plan is ready [1].", "{see note} The plan is ready [1].", true},
		{"fenced plain", "```\nThe plan is ready [1].\n```", "The plan is ready [1].", true},
		{"envelope", `{"answer":"The plan is ready [1].","citations":[1]}`, "The plan is ready [1].", true},
		{"fenced envelope", "```json\n{\"answer\":\"The plan is ready [1].\"}\n```", "The plan is ready [1].", true},
		{"planner envelope", `{"route":"answer","answer":"Ready [1]","used":[1]}`, "Ready [1]", true},
		{"no answer", `{"citations":[1]}`, "", false},
		{"malformed", `{"answer":`, "", false},
		{"array", `[1,2]`, "", false},
	}
	for _, tc := range cases {
		got, ok := unwrapAnswerEnvelope(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q/%v, want %q/%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAskRemoteUnwrapsAJSONAnswerEnvelope(t *testing.T) {
	w := newWorld(t, false)
	w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(1, "urgent"), item(2, "urgent")}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Server down", SenderName: "Ops",
		SenderAddress: "ops@example.com", Instant: "2026-10-25T10:00:00Z", Body: "The server is down."}
	w.store.summaries[2] = store.Summary{RowID: 2, Subject: "Invoice", SenderName: "Billing",
		SenderAddress: "billing@example.com", Instant: "2026-10-25T09:00:00Z", Body: "The invoice is due."}
	w.gen.content = `{"answer":"Two messages need attention: the server outage [1] and the invoice [2].","citations":[1,2]}`

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "what is urgent?", Language: "en", Route: RouteRemote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || strings.Contains(view.Answer, `{"answer"`) ||
		view.Answer != "Two messages need attention: the server outage [1] and the invoice [2]." {
		t.Fatalf("the raw envelope was shown: %+v", view.Answer)
	}
	if len(view.Citations) != 2 {
		t.Fatalf("citations = %+v", view.Citations)
	}
	if got := w.store.saved[0].Answer; strings.Contains(got, `{"answer"`) {
		t.Fatalf("the raw envelope was stored: %q", got)
	}
}

func TestHistoryUnwrapsALegacyJSONAnswer(t *testing.T) {
	w := newWorld(t, false)
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Plan", SenderName: "Bob",
		SenderAddress: "bob@example.com", Instant: "2026-10-25T10:00:00Z", Body: "The plan is ready."}
	w.store.saved = []store.QAHistory{{
		ID: 9, Question: "tell me about the plan", Mode: ModeGenerated, Intent: IntentFreeform,
		Language: "en", Provider: "fake", Model: "fake-model",
		Answer:    `{"answer":"The plan is ready [1].","citations":[1]}`,
		Citations: []store.QACitation{{MessageID: 1}},
	}}
	view, ok, err := w.svc.HistoryItem(t.Context(), 9)
	if err != nil || !ok {
		t.Fatalf("history item: ok=%v err=%v", ok, err)
	}
	if view.Answer != "The plan is ready [1]." || strings.Contains(view.Answer, "{") {
		t.Fatalf("legacy envelope displayed raw: %q", view.Answer)
	}
	if len(view.Citations) != 1 {
		t.Fatalf("citations = %+v", view.Citations)
	}
}

func TestAskRemoteStructuredForcesOneWrittenAnswer(t *testing.T) {
	w := newWorld(t, false)
	w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(1, "urgent"), item(2, "urgent")}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Server down", SenderName: "Ops",
		SenderAddress: "ops@example.com", Instant: "2026-10-25T10:00:00Z", Body: "The server is down."}
	w.store.summaries[2] = store.Summary{RowID: 2, Subject: "Invoice", SenderName: "Billing",
		SenderAddress: "billing@example.com", Instant: "2026-10-25T09:00:00Z", Body: "The invoice is due."}
	w.gen.content = "Two messages need attention: the server outage [1] and the invoice [2]."

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "what is urgent?", Language: "en", Route: RouteRemote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 1 {
		t.Fatalf("Remote made %d model calls, want exactly 1", w.gen.calls)
	}
	if view.Route != RouteRemote || view.Mode != ModeGenerated || view.Intent != IntentUrgent {
		t.Fatalf("view = %+v", view)
	}
	if view.Provider != "fake" || view.Model != "fake-model" {
		t.Fatalf("provenance = %q/%q", view.Provider, view.Model)
	}
	if len(view.Citations) != 2 {
		t.Fatalf("citations = %+v", view.Citations)
	}
	if !strings.Contains(w.gen.user, "Messages received today") && !strings.Contains(w.gen.user, "urgent") {
		t.Fatalf("prompt lacks typed facts or intent: %s", w.gen.user)
	}
	if !strings.Contains(w.gen.system, "Do not use em dashes") {
		t.Fatalf("the written-answer prompt lost the plain-language rule: %s", w.gen.system)
	}
	if got := w.store.saved[0].Route; got != string(RouteRemote) {
		t.Fatalf("stored route = %q", got)
	}
}

func TestAskRemoteFreeformCallsTheGeneratorOnce(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Plan", SenderName: "Bob",
		SenderAddress: "bob@example.com", Instant: "2026-10-25T10:00:00Z", Body: "The plan is ready."}
	w.gen.content = "The plan is ready [1]."

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "tell me about the plan", Language: "en", Route: RouteRemote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 1 || w.store.queryCalls != 0 {
		t.Fatalf("model calls=%d query calls=%d", w.gen.calls, w.store.queryCalls)
	}
	if view.Mode != ModeGenerated || view.Route != RouteRemote || len(view.Citations) != 1 {
		t.Fatalf("view = %+v", view)
	}
}

func TestAskAutoSummaryTodayUsesTheOwnersTodayWindow(t *testing.T) {
	w := newWorld(t, false)
	w.prio.byFilter[priority.FilterSecurity] = []priority.Item{item(1, "security"), item(2, "security")}
	w.store.queryResult = mailquery.Result{Total: 0, Rows: []mailquery.Row{}}
	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "résume ma journée", Language: "fr", Route: RouteAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeStructured || view.Intent != IntentSummaryToday || view.Counts["total"] != 0 {
		t.Fatalf("Auto did not use the today window: %+v", view)
	}
	if len(view.Counts) != 1 {
		t.Fatalf("Auto leaked mailbox-wide priority counts: %v", view.Counts)
	}
	if w.store.lastPlan.TimeRange != mailquery.TimeToday {
		t.Fatalf("Auto plan = %+v", w.store.lastPlan)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Auto summary called the generator %d times", w.gen.calls)
	}
}

func TestAskTodayUsesTheOwnersTodayWindow(t *testing.T) {
	w := newWorld(t, false)
	w.store.queryResult = mailquery.Result{Total: 3, Rows: []mailquery.Row{
		{MessageID: 1, Subject: "A", SenderName: "One", Instant: "2026-10-26T08:00:00Z"},
		{MessageID: 2, Subject: "B", SenderName: "Two", Instant: "2026-10-26T07:00:00Z"},
	}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "A", SenderName: "One", SenderAddress: "one@example.com", Instant: "2026-10-26T08:00:00Z"}
	w.store.summaries[2] = store.Summary{RowID: 2, Subject: "B", SenderName: "Two", SenderAddress: "two@example.com", Instant: "2026-10-26T07:00:00Z"}

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "résume ma journée", Language: "fr", Route: RouteLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Intent != IntentSummaryToday || view.Mode != ModeStructured || view.Counts["total"] != 3 {
		t.Fatalf("view = %+v", view)
	}
	if w.store.lastPlan.TimeRange != mailquery.TimeToday ||
		w.store.lastPlan.Direction != mailquery.DirectionReceived ||
		w.store.lastPlan.Limit != todayCitationLimit || w.store.lastPlan.Order != mailquery.OrderDesc {
		t.Fatalf("today plan = %+v", w.store.lastPlan)
	}
	if len(view.Citations) != 2 {
		t.Fatalf("citations = %+v", view.Citations)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Local called the generator %d times", w.gen.calls)
	}
}

func TestAskRemoteTodayWithNoMessagesDoesNotCallTheModel(t *testing.T) {
	w := newWorld(t, false)
	w.store.queryResult = mailquery.Result{Total: 0, Rows: []mailquery.Row{}}
	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "résume ma journée", Language: "fr", Route: RouteRemote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Remote called the model with no evidence: %d", w.gen.calls)
	}
	if view.Notice != NoticeNoMatches || view.Mode != ModeRetrieval {
		t.Fatalf("view = %+v", view)
	}
}
