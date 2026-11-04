package api

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/qa"
)

func agendaWireFixture() agenda.Result {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	last := at.Add(-time.Hour)
	end := at.Add(2 * time.Hour)
	next := agenda.Item{Kind: agenda.KindEvent, At: at.Add(time.Hour), Title: "Next"}
	return agenda.Result{
		Now: at, HorizonHours: 3, Until: at.Add(3 * time.Hour),
		Items: []agenda.Item{{
			Kind: agenda.KindReply, At: at, LastAt: &last, End: &end,
			Title: "Plan review", Detail: "Waiting since yesterday",
			ConversationID: "C1", MessageID: 42, EventID: "ev-1",
			Location: "Room A", AllDay: true, Participants: 4, Slack: -120,
			LinkedEventID: "ev-1", LinkedEvent: "Kickoff",
			Reasons: []agenda.Reason{agenda.ReasonWaitingSince, agenda.ReasonMeetingWithThem},
		}},
		Events: 1, Obligations: 1, Backlog: 2, Deadlines: 3,
		NextEvent: &next, BusySeconds: 1800, Truncated: true,
	}
}

// Every field the agenda row reads survives the wire projection.
func TestAgendaWireKeepsEveryReadField(t *testing.T) {
	body := wireObject(t, AgendaViewOf(agendaWireFixture()))
	for _, path := range []string{"horizon_hours", "items.0.kind", "items.0.at",
		"items.0.last_at", "items.0.title", "items.0.detail", "items.0.message_id",
		"items.0.location", "items.0.all_day", "items.0.linked_event_title",
		"items.0.reasons.0", "events", "obligations", "backlog", "deadlines", "truncated"} {
		if _, ok := jsonAtPath(body, path); !ok {
			t.Errorf("agenda wire dropped %s", path)
		}
	}
}

// The service keeps the build bounds, the next-event shortcut and the slack
// arithmetic; none of them reaches the agenda row. The truncation flag is a
// read field: the counts describe the whole result, so the screen needs it.
func TestAgendaWireDropsServiceOnlyFields(t *testing.T) {
	body := wireObject(t, AgendaViewOf(agendaWireFixture()))
	for _, path := range []string{"now", "until", "next_event", "busy_seconds",
		"items.0.end", "items.0.conversation_id", "items.0.event_id",
		"items.0.participants", "items.0.slack_seconds", "items.0.linked_event_id"} {
		if _, ok := jsonAtPath(body, path); ok {
			t.Errorf("agenda wire still carries %s", path)
		}
	}
}

func TestAgendaWireZeroValueKeepsItemsAndReasonsArrays(t *testing.T) {
	body := wireObject(t, AgendaViewOf(agenda.Result{}))
	if items, ok := jsonAtPath(body, "items"); !ok {
		t.Fatal("zero agenda has no items field")
	} else if _, ok := items.([]any); !ok {
		t.Fatalf("items = %#v, want an array", items)
	}
	withItem := wireObject(t, AgendaViewOf(agenda.Result{Items: []agenda.Item{{Title: "x"}}}))
	if reasons, ok := jsonAtPath(withItem, "items.0.reasons"); !ok {
		t.Fatal("agenda item has no reasons field")
	} else if _, ok := reasons.([]any); !ok {
		t.Fatalf("reasons = %#v, want an array", reasons)
	}
}

// Ask embeds the same agenda result; the override keeps every answer field
// and replaces only the agenda block.
func TestAskViewWireProjectsTheEmbeddedAgenda(t *testing.T) {
	result := agendaWireFixture()
	view := qa.AskView{
		ID: 7, Question: "What is next?", Mode: qa.ModeComputed,
		Counts:    map[string]int{"agenda": 1},
		Citations: []qa.Citation{{MessageID: 42, Reason: "agenda"}},
		Agenda:    &result,
	}
	body := wireObject(t, AskViewWireOf(view))
	for _, path := range []string{"id", "question", "mode", "counts.agenda",
		"citations.0.message_id", "agenda.horizon_hours", "agenda.items.0.title",
		"agenda.items.0.reasons.0", "agenda.truncated"} {
		if _, ok := jsonAtPath(body, path); !ok {
			t.Errorf("Ask wire dropped %s", path)
		}
	}
	for _, path := range []string{"agenda.now", "agenda.until", "agenda.next_event",
		"agenda.busy_seconds", "agenda.items.0.slack_seconds",
		"agenda.items.0.conversation_id", "agenda.items.0.linked_event_id"} {
		if _, ok := jsonAtPath(body, path); ok {
			t.Errorf("Ask wire still carries %s", path)
		}
	}

	empty := wireObject(t, AskViewWireOf(qa.AskView{ID: 8}))
	if _, ok := empty["agenda"]; ok {
		t.Fatal("an answer without an agenda still carries the field")
	}
}
