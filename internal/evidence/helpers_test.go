package evidence

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// everything is a source that reports every capability.
var everything = source.Capabilities{Incremental: true, Bodies: true, Recipients: true, Deletes: true, OwnerIdentity: true,
	AddressedToMe: true, FolderRoles: true, MessageClass: true, Headers: true, MultiFolder: true, Attachments: true,
	ReadState: true, Flags: true, Categories: true, LastVerb: true}

var testKey = source.StoreKey("src", "store-1")

type world struct {
	t   *testing.T
	cfg *config.Config
	st  *store.Store
	bus *events.Bus
	ev  *Engine
}

// newWorld is a store whose one mailbox is picked, its owner declared
// (mailtest.Owner) and its source reporting caps.
func newWorld(t *testing.T, caps source.Capabilities, overrides map[string]any) *world {
	t.Helper()
	w := &world{t: t, bus: events.New()}
	t.Cleanup(w.bus.Close)
	settings := map[string]any{source.SettingSelected: []any{testKey}, source.SettingDeclared: []any{testKey + "=" + mailtest.Owner},
		"time.zone": "Europe/Paris", "evidence.workers": 2, "evidence.batch_size": 3}
	for k, v := range overrides {
		settings[k] = v
	}
	w.cfg = configtest.NewWithBus(t, w.bus, settings)
	st, err := store.Open(t.Context(), w.cfg, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w.st = st
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", caps); err != nil {
		t.Fatal(err)
	}
	if w.ev, err = New(Deps{Config: w.cfg, Store: st, Bus: w.bus}); err != nil {
		t.Fatal(err)
	}
	return w
}

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC) // a Monday

// msg is a plain received message from sender at t0 + hours.
func msg(id, sender string, hours int, body string) mail.Message {
	m := mailtest.Message("src", id)
	m.SenderAddress, m.SentRepresentingAddress, m.SenderName = sender, sender, strings.Split(sender, "@")[0]
	m.Body, m.Headers, m.Subject = body, "Received: from mx.example.net", "Subject "+id
	m.InReplyTo, m.References, m.ConversationID, m.ConversationIndex, m.InternetMessageID = "", "", "", "", "<"+id+"@x>"
	m.Recipients = []mail.Recipient{{Kind: mail.To, Address: mailtest.Owner}}
	m.ReceivedAt = t0.Add(time.Duration(hours) * time.Hour).Format(mail.TimeLayout)
	m.SentOn = m.ReceivedAt
	m.Attachments, m.HasAttachments, m.Categories, m.FlagStatus, m.TaskDueDate = nil, false, "", mail.OptInt{}, ""
	m.ReadReceiptRequested = false
	return m
}

// sentMsg is the owner's message to rcpt in the Sent folder (no headers).
func sentMsg(id, rcpt string, hours int, body string) mail.Message {
	m := msg(id, mailtest.Owner, hours, body)
	m.FolderPath, m.FolderRole, m.Headers = "Sent", mail.RoleSent, ""
	m.Recipients = []mail.Recipient{{Kind: mail.To, Address: rcpt}}
	return m
}

// write stores messages and judges them: the owner's own are sent by the
// owner, the rest addressed to the owner (scope itself is tested elsewhere).
func (w *world) write(msgs ...mail.Message) {
	w.t.Helper()
	if err := w.st.UpsertMessages(w.t.Context(), 0, msgs); err != nil {
		w.t.Fatal(err)
	}
	var rows []store.VerdictRow
	_ = w.st.EachMessage(w.t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		reason := scope.ReasonRecipientMatch
		if mail.NormalizeAddress(r.Message.SenderAddress) == mailtest.Owner {
			reason = scope.ReasonSentByOwner
		}
		rows = append(rows, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: reason})
		return nil
	})
	if err := w.st.WriteVerdicts(w.t.Context(), "test", rows); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) run() Report {
	w.t.Helper()
	rep, err := w.ev.Run(w.t.Context(), nil)
	if err != nil {
		w.t.Fatalf("run: %v (%+v)", err, rep)
	}
	return rep
}

// record is the current evidence of a message by its id.
func (w *world) record(id string) Record {
	w.t.Helper()
	var row int64
	_ = w.st.EachMessage(w.t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == id {
			row = r.RowID
		}
		return nil
	})
	recs, err := w.ev.Records(w.t.Context(), []int64{row})
	if err != nil || recs[row] == nil {
		w.t.Fatalf("record %s: %v", id, err)
	}
	return recs[row]
}

// want checks one value of a record.
func want(t *testing.T, r Record, name string, v Value) {
	t.Helper()
	if got := r[name]; !got.Equal(v) {
		t.Errorf("%s = %+v, want %+v", name, got, v)
	}
}

func storeAll() store.MessageFilter { return store.MessageFilter{} }

// storeBytes checkpoints the store (folding and truncating the WAL), then
// reports the logical database size and the WAL file size.
func (w *world) storeBytes() (db, wal int64) {
	w.t.Helper()
	if err := w.st.Checkpoint(w.t.Context()); err != nil {
		w.t.Fatalf("checkpoint: %v", err)
	}
	n, err := w.st.SizeBytes(w.t.Context())
	if err != nil {
		w.t.Fatalf("store size: %v", err)
	}
	return n, fileBytes(store.Path(w.cfg) + "-wal")
}

// walBytes is the WAL file size right now, before a checkpoint folds it.
func (w *world) walBytes() int64 {
	w.t.Helper()
	return fileBytes(store.Path(w.cfg) + "-wal")
}

func fileBytes(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// evidenceRows counts the stored evidence rows.
func (w *world) evidenceRows() int {
	w.t.Helper()
	n := 0
	if err := w.st.EachEvidence(w.t.Context(), func(store.EvidenceRow) error {
		n++
		return nil
	}); err != nil {
		w.t.Fatalf("evidence rows: %v", err)
	}
	return n
}

// evidenceRuns counts the persisted evidence runs.
func (w *world) evidenceRuns() int {
	w.t.Helper()
	runs, err := w.st.ListEvidenceRuns(w.t.Context(), 1<<20)
	if err != nil {
		w.t.Fatalf("evidence runs: %v", err)
	}
	return len(runs)
}
