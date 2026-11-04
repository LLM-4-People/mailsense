package logx

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

func testWireEntry() Entry {
	return Entry{
		Seq:       42,
		Time:      time.Date(2026, 10, 27, 12, 0, 0, 123456789, time.UTC),
		Level:     "INFO",
		Component: "test.sse",
		Message:   "fanout",
		Attrs:     map[string]any{"attempt": int64(2), "mailbox": "bench@example.invalid"},
	}
}

// TestEntryJSONMatchesMarshal checks the cached wire form is exactly the
// exported-entry JSON: the unexported cache field never reaches the wire.
func TestEntryJSONMatchesMarshal(t *testing.T) {
	e := testWireEntry()
	got, err := e.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("JSON():\n%s\nwant\n%s", got, want)
	}

	cached := e
	cached.wire = []byte(`{"seq":42}`)
	got, err = cached.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"seq":42}` {
		t.Fatalf("cached JSON %q", got)
	}
	out, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("the cache field leaked into JSON:\n%s\nwant\n%s", out, want)
	}
}

// TestRingAddLeavesWireUncached covers test-inserted entries: add cannot
// cache (there is no pipeline to enable it), so JSON() encodes on demand.
func TestRingAddLeavesWireUncached(t *testing.T) {
	r := NewRing(2)
	r.add(testWireEntry())
	entries := r.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("snapshot %d entries", len(entries))
	}
	e := entries[0]
	if e.wire != nil {
		t.Fatal("Ring.add cached wire bytes")
	}
	got, err := e.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("JSON():\n%s\nwant\n%s", got, want)
	}
}

func TestRingPushCachesWireOnlyWhenAsked(t *testing.T) {
	r := NewRing(4)

	cached := r.push(testWireEntry(), true)
	if cached.wire == nil {
		t.Fatal("push with a JSON consumer did not cache the wire form")
	}
	want, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached.wire, want) {
		t.Fatalf("cached wire:\n%s\nwant\n%s", cached.wire, want)
	}

	uncached := r.push(testWireEntry(), false)
	if uncached.wire != nil {
		t.Fatal("push without a JSON consumer cached the wire form")
	}
}

// TestPipelineCachesWireOnlyForJSONConsumers pins the condition in core.write:
// neither the file nor the event bus means no encoding for the ring.
func TestPipelineCachesWireOnlyForJSONConsumers(t *testing.T) {
	t.Run("both disabled", func(t *testing.T) {
		svc, _ := setup(t, nil, map[string]any{
			"log.file.enabled":   false,
			"log.events.enabled": false,
		})
		For("t").Info("no consumer")
		if e := last(t, svc); e.wire != nil {
			t.Fatal("wire was cached with both JSON consumers disabled")
		}
	})

	t.Run("event bus only", func(t *testing.T) {
		bus := events.New()
		t.Cleanup(bus.Close)
		svc, _ := setup(t, bus, map[string]any{"log.file.enabled": false})
		For("t").Info("event consumer")
		if e := last(t, svc); e.wire == nil {
			t.Fatal("wire was not cached for the event bus")
		}
	})

	t.Run("file only", func(t *testing.T) {
		svc, _ := setup(t, nil, map[string]any{"log.events.enabled": false})
		For("t").Info("file consumer")
		if e := last(t, svc); e.wire == nil {
			t.Fatal("wire was not cached for the log file")
		}
	})
}

// TestLineReusesCachedWire checks the file line is still the same JSON plus
// one newline, that the cached bytes have no spare capacity for an append to
// write into, and that appending the line did not mutate the cached payload.
func TestLineReusesCachedWire(t *testing.T) {
	r := NewRing(2)
	e := r.push(testWireEntry(), true)
	if cap(e.wire) != len(e.wire) {
		t.Fatalf("cached wire has spare capacity: len=%d cap=%d", len(e.wire), cap(e.wire))
	}
	before := bytes.Clone(e.wire)
	line, err := e.line()
	if err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Clone(before), '\n')
	if !bytes.Equal(line, want) {
		t.Fatalf("line:\n%s\nwant\n%s", line, want)
	}
	if len(line) > 0 && len(e.wire) > 0 && &line[0] == &e.wire[0] {
		t.Fatal("line() aliases the cached wire")
	}
	after, err := e.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("line() changed the cached JSON:\n%s\nwant\n%s", after, before)
	}
	if &after[0] != &e.wire[0] {
		t.Fatal("JSON() no longer returns the cached bytes")
	}
}
