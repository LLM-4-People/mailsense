package source

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
)

// ContactCollection is one address book as a connector reports it, before
// any item is read. EntryID is the connector's stable folder identity.
type ContactCollection struct {
	SourceID string `json:"source_id"`
	StoreID  string `json:"store_id"`
	StoreKey string `json:"store_key"`
	EntryID  string `json:"entry_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Default  bool   `json:"default"`
}

// ContactSink receives one address book. The syncer buffers a whole
// collection and replaces it atomically; a failed read therefore never
// deletes contacts it did not see.
type ContactSink interface {
	Contact(ctx context.Context, c contacts.Contact) error
	Progress(p Progress)
}

// ContactSource is implemented by connectors that read address books.
type ContactSource interface {
	ContactCollections(ctx context.Context) ([]ContactCollection, error)
	SyncContacts(ctx context.Context, col ContactCollection, sink ContactSink) error
}

// CalendarCollection is one calendar folder. A connector reports the
// default calendar and any additional calendar folder of a picked store.
type CalendarCollection struct {
	SourceID string `json:"source_id"`
	StoreID  string `json:"store_id"`
	StoreKey string `json:"store_key"`
	EntryID  string `json:"entry_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Default  bool   `json:"default"`
}

// CalendarSink receives the occurrences of one calendar inside the sync
// window. Recurrences are expanded by the connector: an Event is one
// occurrence, never an unbounded rule.
type CalendarSink interface {
	Event(ctx context.Context, e calendar.Event) error
	Progress(p Progress)
}

// CalendarSource is implemented by connectors that read calendars.
type CalendarSource interface {
	CalendarCollections(ctx context.Context) ([]CalendarCollection, error)
	SyncCalendar(ctx context.Context, col CalendarCollection, since, until time.Time, sink CalendarSink) error
}
