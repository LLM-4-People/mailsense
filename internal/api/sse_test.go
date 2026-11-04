package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

type sseStream struct {
	t      *testing.T
	resp   *http.Response
	reader *bufio.Reader
	cancel context.CancelFunc
}

type testSSEFrame struct {
	Event   string
	ID      string
	Data    []string
	Comment []string
	Retry   string
}

func (f testSSEFrame) data() string { return strings.Join(f.Data, "\n") }

func readFrame(r *bufio.Reader) (testSSEFrame, error) {
	var f testSSEFrame
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return f, nil
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			f.Event = value
		case "id":
			f.ID = value
		case "data":
			f.Data = append(f.Data, value)
		case "retry":
			f.Retry = value
		case "":
			f.Comment = append(f.Comment, value)
		default:
			f.Comment = append(f.Comment, line)
		}
	}
}

func (s *sseStream) next(timeout time.Duration) testSSEFrame {
	s.t.Helper()
	type result struct {
		frame testSSEFrame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := readFrame(s.reader)
		ch <- result{f, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			s.t.Fatalf("reading SSE frame: %v", r.err)
		}
		return r.frame
	case <-time.After(timeout):
		s.t.Fatal("timed out waiting for an SSE frame")
		return testSSEFrame{}
	}
}

// event returns the next frame of the named topic, skipping comments and
// keep-alive fields.
func (s *sseStream) event(name string) testSSEFrame {
	s.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f := s.next(time.Until(deadline))
		if f.Event == name {
			return f
		}
		if f.Event == "" {
			continue
		}
		s.t.Fatalf("event %q, want %q: %+v", f.Event, name, f)
	}
}

func (s *sseStream) heartbeat() testSSEFrame {
	s.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f := s.next(time.Until(deadline))
		if f.Event != "" {
			s.t.Fatalf("event %q before the heartbeat: %+v", f.Event, f)
		}
		if len(f.Comment) > 0 && strings.Contains(f.Comment[0], "heartbeat") {
			return f
		}
	}
}

func (s *sseStream) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

func openSSE(t *testing.T, ts *httptest.Server, srv *Server, path string, headers map[string]string, ctx context.Context) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+srv.token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	st := &sseStream{t: t, resp: resp, reader: bufio.NewReader(resp.Body), cancel: cancel}
	t.Cleanup(st.close)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("SSE status %d: %s", resp.StatusCode, data)
	}
	f := st.next(2 * time.Second)
	if f.Retry != "3000" {
		t.Fatalf("first SSE frame is not retry: %+v", f)
	}
	return st
}

func openSSEURL(t *testing.T, base string, srv *Server, path string, ctx context.Context) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+srv.token)
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	st := &sseStream{t: t, resp: resp, reader: bufio.NewReader(resp.Body), cancel: cancel}
	t.Cleanup(st.close)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status %d", resp.StatusCode)
	}
	f := st.next(2 * time.Second)
	if f.Retry != "3000" {
		t.Fatalf("first SSE frame is not retry: %+v", f)
	}
	return st
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func entry(seq uint64, message string) logx.Entry {
	return logx.Entry{Seq: seq, Level: "INFO", Component: "test", Message: message, Time: time.Unix(1700000000, 0).UTC()}
}

func wake(w *world, e logx.Entry) {
	events.Publish(w.bus, logx.TopicEntry, e)
}

func (s *sseStream) expectWake(seq uint64, count int) {
	s.t.Helper()
	f := s.event(LogWakeEvent)
	if f.ID != strconv.FormatUint(seq, 10) {
		s.t.Fatalf("wake id %q, want %d: %+v", f.ID, seq, f)
	}
	var got logWake
	if err := json.Unmarshal([]byte(f.data()), &got); err != nil {
		s.t.Fatalf("wake data %q: %v", f.data(), err)
	}
	if got.Seq != seq || got.Count != count {
		s.t.Fatalf("wake %+v, want seq %d count %d", got, seq, count)
	}
	for _, forbidden := range []string{"attrs", "msg", "message", "hello", "entry"} {
		if strings.Contains(f.data(), forbidden) {
			s.t.Fatalf("wake frame leaks %q: %q", forbidden, f.data())
		}
	}
}

func TestSSEFraming(t *testing.T) {
	srv, ts, w := newTestServer(t)
	st := openSSE(t, ts, srv, "/api/events", nil, context.Background())

	events.Publish(w.bus, learning.TopicStatus, learning.Status{State: learning.Learned})
	f := st.event("learning.status")
	if f.ID != "" {
		t.Fatalf("a non-log event carries id %q", f.ID)
	}
	var status learning.Status
	if err := json.Unmarshal([]byte(f.data()), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != learning.Learned {
		t.Fatalf("status %+v", status)
	}

	e := entry(3, "three")
	w.logs.Append(e)
	wake(w, e)
	st.expectWake(3, 1)

	st.heartbeat()
}

func TestSSESeedThenTail(t *testing.T) {
	w := newWorld()
	w.logs.Reset(entry(1, "one"), entry(2, "two"), entry(3, "three"), entry(4, "four"), entry(5, "five"))
	hooked := make(chan struct{})
	var once bool
	w.logs.firstRead = func() {
		if once {
			return
		}
		once = true
		w.logs.Append(entry(6, "six"))
		close(hooked)
	}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	st := openSSE(t, ts, srv, "/api/events", map[string]string{"Last-Event-ID": "3"}, context.Background())

	select {
	case <-hooked:
	case <-time.After(2 * time.Second):
		t.Fatal("the seed read never happened")
	}
	st.expectWake(6, 3)

	e := entry(7, "seven")
	w.logs.Append(e)
	wake(w, e)
	st.expectWake(7, 1)

	// The same entries must not be written again by the next tail read.
	st.heartbeat()
}

func TestSSEReplayModes(t *testing.T) {
	t.Run("since zero replays the retained ring", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(1, "one"), entry(2, "two"), entry(3, "three"), entry(4, "four"), entry(5, "five"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events?since=0", nil, context.Background())
		st.expectWake(5, 5)
	})

	t.Run("live only starts at the newest retained entry", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(1, "one"), entry(2, "two"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events", nil, context.Background())
		e := entry(3, "three")
		w.logs.Append(e)
		wake(w, e)
		st.expectWake(3, 1)
	})

	t.Run("reconnect replays only newer entries", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(1, "one"), entry(2, "two"), entry(3, "three"), entry(4, "four"), entry(5, "five"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events", map[string]string{"Last-Event-ID": "4"}, context.Background())
		st.expectWake(5, 1)
	})
}

func TestSSERingRotation(t *testing.T) {
	t.Run("seed discloses the overwritten entry", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(3, "three"), entry(4, "four"), entry(5, "five"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events?since=1", nil, context.Background())
		f := st.event("dropped")
		var d dropped
		if err := json.Unmarshal([]byte(f.data()), &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "ring_rotated" || d.Topics["log.entry"] != 1 || d.Total != 1 ||
			d.OldestSeq == nil || *d.OldestSeq != 3 {
			t.Fatalf("dropped %+v", d)
		}
		st.expectWake(5, 3)
	})

	t.Run("since zero never invents a rotation", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(3, "three"), entry(4, "four"), entry(5, "five"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events?since=0", nil, context.Background())
		st.expectWake(5, 3)
		st.heartbeat()
	})

	t.Run("tail discloses a cursor that fell behind", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(1, "one"), entry(2, "two"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events", nil, context.Background())
		w.logs.Reset(entry(10, "ten"), entry(11, "eleven"), entry(12, "twelve"), entry(13, "thirteen"))
		events.Publish(w.bus, logx.TopicEntry, entry(13, "thirteen"))
		f := st.event("dropped")
		var d dropped
		if err := json.Unmarshal([]byte(f.data()), &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "ring_rotated" || d.Topics["log.entry"] != 7 || d.Total != 7 ||
			d.OldestSeq == nil || *d.OldestSeq != 10 {
			t.Fatalf("dropped %+v", d)
		}
		st.expectWake(13, 4)
	})
}

func TestSSESeqReset(t *testing.T) {
	t.Run("query cursor from an older launch", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(1, "one"), entry(2, "two"), entry(3, "three"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events?since=999", nil, context.Background())
		f := st.event("dropped")
		var d dropped
		if err := json.Unmarshal([]byte(f.data()), &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "seq_reset" || len(d.Topics) != 0 || d.Total != 0 ||
			d.OldestSeq == nil || *d.OldestSeq != 1 || d.LastSeq == nil || *d.LastSeq != 3 {
			t.Fatalf("dropped %+v", d)
		}
		st.expectWake(3, 3)
		e := entry(4, "four")
		w.logs.Append(e)
		wake(w, e)
		st.expectWake(4, 1)
	})

	t.Run("header cursor from an older launch", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset(entry(1, "one"), entry(2, "two"), entry(3, "three"))
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events", map[string]string{"Last-Event-ID": "18446744073709551615"}, context.Background())
		f := st.event("dropped")
		var d dropped
		if err := json.Unmarshal([]byte(f.data()), &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "seq_reset" || d.Total != 0 || *d.OldestSeq != 1 || *d.LastSeq != 3 {
			t.Fatalf("dropped %+v", d)
		}
		st.expectWake(3, 3)
	})

	t.Run("empty ring resets and then tails", func(t *testing.T) {
		w := newWorld()
		w.logs.Reset()
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		st := openSSE(t, ts, srv, "/api/events?since=999", nil, context.Background())
		f := st.event("dropped")
		var d dropped
		if err := json.Unmarshal([]byte(f.data()), &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "seq_reset" || d.OldestSeq == nil || *d.OldestSeq != 0 || d.LastSeq == nil || *d.LastSeq != 0 {
			t.Fatalf("dropped %+v", d)
		}
		e := entry(1, "one")
		w.logs.Append(e)
		wake(w, e)
		st.expectWake(1, 1)
	})
}

func TestSSEInvalidCursor(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	for _, tc := range []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"text", "/api/events?since=abc", nil},
		{"negative", "/api/events?since=-1", nil},
		{"empty", "/api/events?since=", nil},
		{"header", "/api/events", map[string]string{"Last-Event-ID": "abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := request{method: "GET", path: tc.path, headers: tc.headers}
			status, header, data := do(t, ts, srv, in)
			mustStatus(t, status, 400, data)
			if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
				t.Fatalf("content type %q", header.Get("Content-Type"))
			}
			if code := errorCode(t, data); code != "invalid_since" {
				t.Fatalf("code %q", code)
			}
		})
	}
}

func TestSSEBufferFullDisclosure(t *testing.T) {
	opts := testOptions(t)
	opts.SSEBuffer = 2
	w := newWorld()
	srv, _, _ := newTestServerWith(t, opts, w)
	c, err := srv.hub.Add()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.hub.Remove(c)
	for i := range 5 {
		srv.hub.broadcast("sync.progress", []byte(`{"i":`+strconv.Itoa(i)+`}`))
	}
	if len(c.ch) != 2 {
		t.Fatalf("queue length %d", len(c.ch))
	}
	c.mu.Lock()
	droppedCount := c.dropped["sync.progress"]
	c.mu.Unlock()
	if droppedCount != 3 {
		t.Fatalf("dropped %d", droppedCount)
	}
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec, timeout: time.Second}
	if err := srv.writeClientDropped(sw, c); err != nil {
		t.Fatal(err)
	}
	want := "event: dropped\ndata: {\"reason\":\"buffer_full\",\"topics\":{\"sync.progress\":3},\"total\":3}\n\n"
	if rec.Body.String() != want {
		t.Fatalf("dropped frame:\n%q\nwant\n%q", rec.Body.String(), want)
	}
	first := <-c.ch
	if first.topic != "sync.progress" || string(first.data) != `{"i":0}` {
		t.Fatalf("first queued event %+v", first)
	}
}

func TestSSEBusDroppedDisclosure(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	sub, err := events.Subscribe(bus, syncer.TopicProgress, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	h := &Hub{subs: []busSource{{topic: syncer.TopicProgress.Name(), dropped: sub.Dropped}}}
	c := &sseClient{lastBusDropped: map[string]uint64{syncer.TopicProgress.Name(): sub.Dropped()}}
	for range 3 {
		events.Publish(bus, syncer.TopicProgress, syncer.Progress{})
	}
	if got := sub.Dropped(); got != 2 {
		t.Fatalf("subscription dropped %d", got)
	}
	notices := h.pollBusDropped(c)
	if len(notices) != 1 {
		t.Fatalf("notices %+v", notices)
	}
	n := notices[0]
	if n.Reason != "bus_dropped" || n.Topics["sync.progress"] != 2 || n.Total != 2 {
		t.Fatalf("notice %+v", n)
	}
	if again := h.pollBusDropped(c); len(again) != 0 {
		t.Fatalf("the same drops were reported twice: %+v", again)
	}
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec, timeout: time.Second}
	if err := sw.dropped(n); err != nil {
		t.Fatal(err)
	}
	want := "event: dropped\ndata: {\"reason\":\"bus_dropped\",\"topics\":{\"sync.progress\":2},\"total\":2}\n\n"
	if rec.Body.String() != want {
		t.Fatalf("dropped frame:\n%q\nwant\n%q", rec.Body.String(), want)
	}
}

func TestSSELogWakeCoalesced(t *testing.T) {
	w := newWorld()
	w.logs.Reset()
	for i := 1; i <= 5; i++ {
		w.logs.Append(entry(uint64(i), "entry"))
	}
	srv, _, _ := newTestServerWith(t, testOptions(t), w)
	c, err := srv.hub.Add()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.hub.Remove(c)
	for range 5 {
		srv.hub.wakeAll()
	}
	c.mu.Lock()
	wakes := len(c.logWake)
	c.mu.Unlock()
	if wakes != 1 {
		t.Fatalf("log wakes coalesced to %d", wakes)
	}
	if notices := srv.hub.pollBusDropped(c); len(notices) != 0 {
		t.Fatalf("a lost log wake-up was reported as bus loss: %+v", notices)
	}
	entries, _, _ := w.logs.Read(0)
	if len(entries) != 5 {
		t.Fatalf("the ring lost entries: %d", len(entries))
	}
}

func TestSSELogWakeIsAReferenceNotThePayload(t *testing.T) {
	w := newWorld()
	w.logs.Reset()
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	// One client stands for a screen that does not read logs; the other
	// behaves like Logs and reads the ring after the wake.
	idle := openSSE(t, ts, srv, "/api/events", nil, context.Background())
	logs := openSSE(t, ts, srv, "/api/events", nil, context.Background())
	waitFor(t, "both stream clients", func() bool { return srv.hub.Count() == 2 })

	const n = 100
	entries := make([]logx.Entry, n)
	for i := range entries {
		e := entry(uint64(i+1), strings.Repeat("payload-", 20))
		e.Attrs = map[string]any{"i": i, "mailbox": "bench@example.invalid"}
		entries[i] = e
	}
	w.logs.Reset(entries...)
	wake(w, entries[n-1])

	for name, st := range map[string]*sseStream{"idle": idle, "logs": logs} {
		f := st.event(LogWakeEvent)
		if f.ID != strconv.Itoa(n) {
			t.Fatalf("%s wake id %q", name, f.ID)
		}
		var got logWake
		if err := json.Unmarshal([]byte(f.data()), &got); err != nil {
			t.Fatalf("%s wake: %v", name, err)
		}
		if got.Seq != n || got.Count != n {
			t.Fatalf("%s wake %+v", name, got)
		}
		frame := "event: " + f.Event + "\nid: " + f.ID + "\ndata: " + f.data() + "\n\n"
		if len(frame) >= 1024 {
			t.Fatalf("%s wake frame is %d bytes for %d entries", name, len(frame), n)
		}
		for _, forbidden := range []string{"payload", "attrs", "mailbox"} {
			if strings.Contains(f.data(), forbidden) {
				t.Fatalf("%s wake leaks %q: %q", name, forbidden, f.data())
			}
		}
	}

	// The Logs side reads the entries once, through the same since route the
	// screen uses; the wake itself carried none of them.
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/logs?since=0&limit=500"})
	mustStatus(t, status, 200, data)
	var page LogSnapshot
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != n || page.LastSeq != n {
		t.Fatalf("logs page: %d entries, last %d", len(page.Entries), page.LastSeq)
	}
	if page.Entries[0].Message != entries[0].Message {
		t.Fatalf("first entry %+v", page.Entries[0])
	}
}

func TestSSECancellation(t *testing.T) {
	w := newWorld()
	w.logs.Reset(entry(1, "one"), entry(2, "two"))
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	t.Run("request context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		st := openSSE(t, ts, srv, "/api/events?since=0", nil, ctx)
		waitFor(t, "the stream to register", func() bool { return srv.hub.Count() == 1 })
		cancel()
		waitFor(t, "the stream to leave the hub", func() bool { return srv.hub.Count() == 0 })
		st.close()
	})

	t.Run("client disconnect", func(t *testing.T) {
		st := openSSE(t, ts, srv, "/api/events?since=0", nil, context.Background())
		waitFor(t, "the stream to register", func() bool { return srv.hub.Count() == 1 })
		_ = st.resp.Body.Close()
		waitFor(t, "the stream to leave the hub", func() bool { return srv.hub.Count() == 0 })
	})

	t.Run("cancel during a large seed", func(t *testing.T) {
		entries := make([]logx.Entry, 2000)
		for i := range entries {
			entries[i] = entry(uint64(i+1), "seed")
		}
		w.logs.Reset(entries...)
		ctx, cancel := context.WithCancel(context.Background())
		st := openSSE(t, ts, srv, "/api/events?since=0", nil, ctx)
		waitFor(t, "the stream to register", func() bool { return srv.hub.Count() == 1 })
		cancel()
		waitFor(t, "the stream to leave the hub", func() bool { return srv.hub.Count() == 0 })
		st.close()
	})
}

func TestSSEMaxClients(t *testing.T) {
	opts := testOptions(t)
	opts.SSEMaxClients = 1
	w := newWorld()
	w.logs.Reset(entry(1, "one"))
	srv, ts, _ := newTestServerWith(t, opts, w)
	first := openSSE(t, ts, srv, "/api/events", nil, context.Background())
	waitFor(t, "the first client", func() bool { return srv.hub.Count() == 1 })

	status, header, data := do(t, ts, srv, request{method: "GET", path: "/api/events"})
	mustStatus(t, status, 503, data)
	if header.Get("Retry-After") != "5" {
		t.Fatalf("Retry-After %q", header.Get("Retry-After"))
	}
	if code := errorCode(t, data); code != "too_many_streams" {
		t.Fatalf("code %q", code)
	}
	first.close()
	waitFor(t, "the first client to leave", func() bool { return srv.hub.Count() == 0 })

	second := openSSE(t, ts, srv, "/api/events", nil, context.Background())
	waitFor(t, "the second client", func() bool { return srv.hub.Count() == 1 })
	second.close()
	waitFor(t, "the second client to leave", func() bool { return srv.hub.Count() == 0 })
}

func TestSSEShutdownWhileStreaming(t *testing.T) {
	opts := testOptions(t)
	w := newWorld()
	w.logs.Reset(entry(1, "one"))
	srv, base := newStartedServer(t, opts, w)
	st := openSSEURL(t, base, srv, "api/events", context.Background())
	waitFor(t, "the stream to register", func() bool { return srv.hub.Count() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(st.resp.Body)
	if err != nil {
		t.Fatalf("reading the stream after shutdown: %v", err)
	}
	if strings.Contains(string(rest), "event: log.wake") {
		t.Fatalf("unexpected frame after shutdown: %q", rest)
	}
	if srv.hub.Count() != 0 {
		t.Fatalf("clients after shutdown: %d", srv.hub.Count())
	}
	if _, err := st.resp.Body.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the stream did not end: %v", err)
	}
	if _, err := os.Stat(opts.DescriptorPath); !os.IsNotExist(err) {
		t.Fatalf("the descriptor survived shutdown: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// A hub close during the initial log seed must end the handler at once. The
// seed can hold a large ring; waiting for every entry would pin the handler
// (and Shutdown) for the whole write timeout.
func TestSSEHubCloseInterruptsLogSeed(t *testing.T) {
	w := newWorld()
	entries := make([]logx.Entry, 500)
	for i := range entries {
		entries[i] = entry(uint64(i+1), "seed")
	}
	w.logs.Reset(entries...)
	started, release := make(chan struct{}), make(chan struct{})
	w.logs.firstRead = func() {
		close(started)
		<-release
	}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	st := openSSE(t, ts, srv, "/api/events?since=0", nil, context.Background())

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the seed read never started")
	}
	srv.hub.Close()
	close(release)

	done := make(chan []byte, 1)
	go func() {
		rest, _ := io.ReadAll(st.resp.Body)
		done <- rest
	}()
	select {
	case rest := <-done:
		if strings.Contains(string(rest), "event: log.wake") {
			t.Fatalf("the seed kept writing after the hub closed: %d bytes", len(rest))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler did not stop after the hub closed")
	}
}
