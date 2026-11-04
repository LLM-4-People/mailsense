package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

// TestSSELogWakeSharedAcrossClients pins the fan-out contract after the
// payload moved to the ring: every client sees the same wake frame, and the
// frame names only a cursor and a count. Entry text and attributes never
// cross the event stream.
func TestSSELogWakeSharedAcrossClients(t *testing.T) {
	tail := func(t *testing.T, srv *Server, cursor uint64) (string, uint64) {
		t.Helper()
		rec := httptest.NewRecorder()
		next, err := srv.tailLogs(context.Background(), &sseWriter{w: rec, timeout: time.Second}, cursor)
		if err != nil {
			t.Fatal(err)
		}
		return rec.Body.String(), next
	}
	w := newWorld()
	w.logs.Reset(entry(1, "one"), entry(2, "two"), entry(3, "three"))
	e := entry(4, "secret message text")
	e.Attrs = map[string]any{"attempt": 2, "mailbox": "bench@example.invalid"}
	w.logs.Append(e)
	srv := &Server{
		services: Services{Logs: w.logs},
		hub:      &Hub{done: make(chan struct{})},
	}

	first, next := tail(t, srv, 0)
	second, _ := tail(t, srv, 0)
	if first != second {
		t.Fatalf("clients received different frames:\n%q\n%q", first, second)
	}
	if next != 4 {
		t.Fatalf("cursor %d, want 4", next)
	}
	f, err := readFrame(bufio.NewReader(strings.NewReader(first)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Event != LogWakeEvent || f.ID != "4" {
		t.Fatalf("frame %+v", f)
	}
	var got logWake
	if err := json.Unmarshal([]byte(f.data()), &got); err != nil {
		t.Fatalf("wake data %q: %v", f.data(), err)
	}
	if got.Seq != 4 || got.Count != 4 {
		t.Fatalf("wake %+v", got)
	}
	for _, forbidden := range []string{"secret message text", "attrs", "mailbox", "attempt", logx.TopicEntry.Name()} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("wake frame contains %q: %q", forbidden, first)
		}
	}
	if f.ID != strconv.FormatUint(next, 10) {
		t.Fatalf("id %q for cursor %d", f.ID, next)
	}
}

// TestSSELogWakeReadsAtMostOneEntry proves the wake path never materializes
// the backlog: a ring holding 100,000 entries yields the same small frame as
// an empty cursor.
func TestSSELogWakeReadsAtMostOneEntry(t *testing.T) {
	w := newWorld()
	entries := make([]logx.Entry, 100_000)
	for i := range entries {
		entries[i] = entry(uint64(i+1), strings.Repeat("x", 200))
	}
	w.logs.Reset(entries...)
	srv := &Server{
		services: Services{Logs: w.logs},
		hub:      &Hub{done: make(chan struct{})},
	}
	rec := httptest.NewRecorder()
	if _, err := srv.tailLogs(context.Background(), &sseWriter{w: rec, timeout: time.Second}, 0); err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.Len(); got >= 1024 {
		t.Fatalf("wake for 100,000 entries is %d bytes", got)
	}
}
