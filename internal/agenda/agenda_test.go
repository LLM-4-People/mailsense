package agenda

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func meeting(entry, subject, start, end string, attendees ...string) calendar.Event {
	e := calendar.Event{SourceID: "src", StoreKey: "k", EntryID: entry, CleanGlobalID: entry,
		Subject: subject, Start: ts(start), End: ts(end), Busy: calendar.Busy, Meeting: calendar.Meeting}
	for _, a := range attendees {
		e.Attendees = append(e.Attendees, calendar.Attendee{Kind: calendar.Required, Address: a})
	}
	return e
}

func TestMeetingLinkedObligationIsScheduledBeforeTheMeeting(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	ev := meeting("e1", "Kickoff", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z", "client@example.test")
	got := Build(Input{Now: now, Horizon: 8 * time.Hour,
		Events: []calendar.Event{ev},
		Obligations: []Obligation{{ConversationID: "C1", MessageID: 7, Subject: "Plan review",
			ActorName: "Client", LastAt: now.Add(-48 * time.Hour), Participants: []string{"client@example.test"}}},
	})
	if len(got.Items) != 2 {
		t.Fatalf("items %+v", got.Items)
	}
	reply := got.Items[0]
	if reply.Kind != KindReply || reply.LinkedEventID != "e1" || reply.At != ev.Start {
		t.Fatalf("reply %+v", reply)
	}
	if reply.Slack != 5*3600 || !hasReason(reply.Reasons, ReasonMeetingWithThem) {
		t.Fatalf("slack/reasons %+v", reply)
	}
	if got.Items[1].Kind != KindEvent || got.NextEvent == nil || got.NextEvent.EventID != "e1" {
		t.Fatalf("event %+v next %+v", got.Items[1], got.NextEvent)
	}
}

func TestUnlinkedObligationIsImmediateAndOlderFirst(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	got := Build(Input{Now: now, Horizon: 3 * time.Hour,
		Obligations: []Obligation{
			{ConversationID: "C2", Subject: "Recent", LastAt: now.Add(-1 * time.Hour), Participants: []string{"a@example.test"}},
			{ConversationID: "C1", Subject: "Older", LastAt: now.Add(-30 * time.Hour), Participants: []string{"b@example.test"}},
		},
	})
	if len(got.Items) != 2 {
		t.Fatalf("items %+v", got.Items)
	}
	if got.Items[0].ConversationID == got.Items[1].ConversationID {
		t.Fatalf("duplicate obligation: %+v", got.Items)
	}
	for _, it := range got.Items {
		if !it.At.Equal(now) || it.Slack != 0 || !hasReason(it.Reasons, ReasonWaitingSince) {
			t.Fatalf("item %+v", it)
		}
	}
}

func TestInactiveAndOutOfWindowEventsAreDropped(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	declined := meeting("e1", "Declined", "2026-11-03T10:00:00Z", "2026-11-03T11:00:00Z")
	declined.Response = calendar.Declined
	canceled := meeting("e2", "Canceled", "2026-11-03T11:00:00Z", "2026-11-03T12:00:00Z")
	canceled.Meeting = calendar.MeetingCanceled
	free := meeting("e3", "Free", "2026-11-03T12:00:00Z", "2026-11-03T13:00:00Z")
	free.Busy = calendar.Free
	future := meeting("e4", "Tomorrow", "2026-11-04T09:00:00Z", "2026-11-04T10:00:00Z")
	past := meeting("e5", "Yesterday", "2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z")
	got := Build(Input{Now: now, Horizon: 3 * time.Hour,
		Events: []calendar.Event{declined, canceled, free, future, past}})
	if len(got.Items) != 0 || got.Events != 0 {
		t.Fatalf("items %+v", got.Items)
	}
}

func TestDuplicateMeetingAcrossCalendarsIsShownOnce(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	a := meeting("e1", "Kickoff", "2026-11-03T10:00:00Z", "2026-11-03T11:00:00Z")
	b := a
	b.SourceID = "other"
	b.StoreKey = "other"
	got := Build(Input{Now: now, Horizon: 3 * time.Hour, Events: []calendar.Event{a, b}})
	if len(got.Items) != 1 || got.Events != 1 {
		t.Fatalf("items %+v events=%d", got.Items, got.Events)
	}
}

func TestDeadlinesInWindowIncludingOverdue(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	got := Build(Input{Now: now, Horizon: 8 * time.Hour,
		Deadlines: []Deadline{
			{MessageID: 1, Subject: "Due soon", Due: now.Add(2 * time.Hour)},
			{MessageID: 2, Subject: "Overdue", Due: now.Add(-2 * time.Hour)},
			{MessageID: 3, Subject: "Tomorrow", Due: now.Add(30 * time.Hour)},
			{MessageID: 4, Subject: "Ancient", Due: now.Add(-60 * 24 * time.Hour)},
		}})
	if len(got.Items) != 2 || got.Deadlines != 2 {
		t.Fatalf("items %+v", got.Items)
	}
	if got.Items[0].MessageID != 2 || got.Items[0].Slack >= 0 {
		t.Fatalf("overdue first: %+v", got.Items[0])
	}
}

func TestEmptyInputIsAnEmptyAgenda(t *testing.T) {
	got := Build(Input{Now: ts("2026-11-03T09:00:00Z")})
	if len(got.Items) != 0 || got.HorizonHours != 3 || got.NextEvent != nil {
		t.Fatalf("result %+v", got)
	}
}

func TestTruncationIsReported(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	var deadlines []Deadline
	for i := 0; i < MaxItems+5; i++ {
		deadlines = append(deadlines, Deadline{MessageID: int64(i + 1), Subject: "d",
			Due: now.Add(time.Duration(i) * time.Minute)})
	}
	got := Build(Input{Now: now, Horizon: 3 * time.Hour, Deadlines: deadlines})
	if len(got.Items) != MaxItems || !got.Truncated || got.Deadlines != MaxItems+5 {
		t.Fatalf("len=%d truncated=%v deadlines=%d", len(got.Items), got.Truncated, got.Deadlines)
	}
}

func hasReason(list []Reason, want Reason) bool {
	for _, r := range list {
		if r == want {
			return true
		}
	}
	return false
}

func TestBacklogIsCountedNotScheduled(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	old := Obligation{ConversationID: "C-old", Subject: "Old", LastAt: now.Add(-30 * 24 * time.Hour),
		Participants: []string{"old@example.test"}}
	var recent []Obligation
	for i := 0; i < maxUnlinked+3; i++ {
		recent = append(recent, Obligation{ConversationID: "C" + string(rune('a'+i)), Subject: "Recent",
			LastAt: now.Add(-time.Duration(i) * time.Hour), Participants: []string{"x@example.test"}})
	}
	got := Build(Input{Now: now, Horizon: 3 * time.Hour, Obligations: append([]Obligation{old}, recent...)})
	if got.Obligations != maxUnlinked || got.Backlog != 4 {
		t.Fatalf("obligations=%d backlog=%d", got.Obligations, got.Backlog)
	}
	for _, it := range got.Items {
		if it.ConversationID == "C-old" {
			t.Fatalf("an old unlinked thread was scheduled: %+v", it)
		}
	}
}

func TestOldOverdueDeadlineIsNotInTheNextHours(t *testing.T) {
	now := ts("2026-11-03T09:00:00Z")
	got := Build(Input{Now: now, Horizon: 3 * time.Hour, Deadlines: []Deadline{
		{MessageID: 1, Subject: "Stale", Due: now.Add(-72 * time.Hour)},
		{MessageID: 2, Subject: "Yesterday", Due: now.Add(-12 * time.Hour)},
		{MessageID: 3, Subject: "Soon", Due: now.Add(time.Hour)},
	}})
	if got.Deadlines != 2 || len(got.Items) != 2 {
		t.Fatalf("deadlines=%d items=%+v", got.Deadlines, got.Items)
	}
	for _, it := range got.Items {
		if it.MessageID == 1 {
			t.Fatalf("a three-day-old due date flooded the agenda: %+v", it)
		}
	}
}
