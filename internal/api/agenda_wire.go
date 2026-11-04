package api

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/qa"
)

// AgendaWire is the "next X" view as the wire carries it. The service also
// carries the build bounds and the next-event shortcut; the screen renders
// only the applied horizon, the ordered rows, the counts and whether the row
// list was cut. A cut is never silent: the counts describe the whole result,
// so without the flag the screen would claim to list every counted item.
type AgendaWire struct {
	HorizonHours float64          `json:"horizon_hours"`
	Items        []AgendaItemWire `json:"items"`
	Events       int              `json:"events"`
	Obligations  int              `json:"obligations"`
	Backlog      int              `json:"backlog"`
	Deadlines    int              `json:"deadlines"`
	Truncated    bool             `json:"truncated"`
}

// AgendaItemWire is one row. It keeps the evidence the row renders and drops
// the service's ordering internals: slack_seconds, end, event and
// conversation ids, and the participant count.
type AgendaItemWire struct {
	Kind             agenda.Kind     `json:"kind"`
	At               time.Time       `json:"at"`
	LastAt           *time.Time      `json:"last_at,omitempty"`
	Title            string          `json:"title"`
	Detail           string          `json:"detail,omitempty"`
	MessageID        int64           `json:"message_id,omitempty"`
	Location         string          `json:"location,omitempty"`
	AllDay           bool            `json:"all_day,omitempty"`
	LinkedEventTitle string          `json:"linked_event_title,omitempty"`
	Reasons          []agenda.Reason `json:"reasons"`
}

// AgendaViewOf is the one projection of an agenda build to the wire. Every
// list is non-nil so items and reasons stay arrays.
func AgendaViewOf(result agenda.Result) AgendaWire {
	out := AgendaWire{
		HorizonHours: result.HorizonHours,
		Items:        make([]AgendaItemWire, len(result.Items)),
		Events:       result.Events,
		Obligations:  result.Obligations,
		Backlog:      result.Backlog,
		Deadlines:    result.Deadlines,
		Truncated:    result.Truncated,
	}
	for i, item := range result.Items {
		out.Items[i] = AgendaItemWire{
			Kind: item.Kind, At: item.At, LastAt: item.LastAt, Title: item.Title,
			Detail: item.Detail, MessageID: item.MessageID, Location: item.Location,
			AllDay: item.AllDay, LinkedEventTitle: item.LinkedEvent,
			Reasons: append([]agenda.Reason{}, item.Reasons...),
		}
	}
	return out
}

// AskViewWire overlays the projected agenda on the otherwise unchanged Q&A
// response. The embedded AskView keeps every answer field; the outer Agenda
// is shallower, so encoding/json serializes it instead of the service value.
type AskViewWire struct {
	qa.AskView
	Agenda *AgendaWire `json:"agenda,omitempty"`
}

// AskViewWireOf projects the one agenda block an Ask response carries.
func AskViewWireOf(view qa.AskView) AskViewWire {
	out := AskViewWire{AskView: view}
	if view.Agenda != nil {
		projected := AgendaViewOf(*view.Agenda)
		out.Agenda = &projected
	}
	return out
}
