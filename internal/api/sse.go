package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/source/outlook"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/triage"
)

// errHubClosed ends a handler that was seeding the log ring when the hub
// closed; the stream is already over, so it is not reported as a write error.
var errHubClosed = errors.New("sse: hub closed")

// Hub owns the bus subscriptions and fans out to at most SSEMaxClients
// clients. Each client has a bounded queue for non-log topics; the log
// ring is the payload source and a coalesced log.wake frame only tells the
// Logs screen that entries are waiting there.
type Hub struct {
	bus     *events.Bus
	opts    Options
	log     *slog.Logger
	mu      sync.RWMutex
	clients map[*sseClient]struct{}
	closed  bool
	done    chan struct{}
	wg      sync.WaitGroup
	subs    []busSource
	unsubs  []func()
}

type busSource struct {
	topic   string
	dropped func() uint64
}

// ErrTooManyStreams is returned at the client cap.
var ErrTooManyStreams = errors.New("api: too many SSE clients")

// ErrHubClosed is returned after Hub.Close.
var ErrHubClosed = errors.New("api: SSE hub is closed")

func newHub(bus *events.Bus, opts Options) (*Hub, error) {
	h := &Hub{bus: bus, opts: opts, log: logx.For("api"), clients: map[*sseClient]struct{}{}, done: make(chan struct{})}
	if err := h.subscribeAll(); err != nil {
		h.unsubscribeAll()
		return nil, err
	}
	return h, nil
}

func (h *Hub) subscribeAll() error {
	for _, sub := range []func(*Hub) error{
		func(h *Hub) error { return subscribeHub(h, learning.TopicStatus) },
		func(h *Hub) error { return subscribeHub(h, jobs.TopicJob) },
		func(h *Hub) error { return subscribeHub(h, config.TopicChanged) },
		func(h *Hub) error { return subscribeHub(h, secrets.TopicChanged) },
		func(h *Hub) error { return subscribeHub(h, syncer.TopicProgress) },
		func(h *Hub) error { return subscribeHub(h, syncer.TopicFinished) },
		func(h *Hub) error { return subscribeHub(h, syncer.TopicMailboxes) },
		func(h *Hub) error { return subscribeHub(h, syncer.TopicHeld) },
		func(h *Hub) error { return subscribeHub(h, index.TopicProgress) },
		func(h *Hub) error { return subscribeHub(h, index.TopicFinished) },
		func(h *Hub) error { return subscribeHub(h, evidence.TopicProgress) },
		func(h *Hub) error { return subscribeHub(h, evidence.TopicFinished) },
		func(h *Hub) error { return subscribeHub(h, triage.TopicProgress) },
		func(h *Hub) error { return subscribeHub(h, triage.TopicFinished) },
		func(h *Hub) error { return subscribeHub(h, layer1.TopicProgress) },
		func(h *Hub) error { return subscribeHub(h, layer1.TopicFinished) },
		func(h *Hub) error { return subscribeHub(h, sidecar.TopicState) },
		func(h *Hub) error { return subscribeHub(h, engine.TopicProviderHealth) },
		func(h *Hub) error { return subscribeHub(h, engine.TopicUsage) },
		func(h *Hub) error { return subscribeHub(h, search.TopicStatus) },
		func(h *Hub) error { return subscribeHub(h, logx.TopicHealth) },
		func(h *Hub) error { return subscribeHub(h, logx.TopicApplied) },
		func(h *Hub) error { return subscribeHub(h, store.TopicRunChanged) },
		func(h *Hub) error { return subscribeHub(h, source.TopicRegistry) },
		func(h *Hub) error { return subscribeHub(h, outlook.TopicBreaker) },
		func(h *Hub) error { return subscribeHub(h, outlook.TopicPhase) },
		func(h *Hub) error { return subscribeHub(h, httpx.TopicRetry) },
		func(h *Hub) error { return subscribeHub(h, feedback.TopicLabel) },
	} {
		if err := sub(h); err != nil {
			return err
		}
	}
	return h.subscribeLogWake()
}

func subscribeHub[T any](h *Hub, topic events.Topic[T]) error {
	sub, err := events.Subscribe(h.bus, topic, h.opts.EventsBuffer)
	if err != nil {
		return err
	}
	h.subs = append(h.subs, busSource{topic: topic.Name(), dropped: sub.Dropped})
	h.unsubs = append(h.unsubs, sub.Unsubscribe)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for v := range sub.C {
			data, err := marshal(v)
			if err != nil {
				h.log.Error("SSE payload encoding failed", "topic", topic.Name(), "err", err)
				continue
			}
			h.broadcast(topic.Name(), data)
		}
	}()
	return nil
}

func (h *Hub) subscribeLogWake() error {
	sub, err := events.Subscribe(h.bus, logx.TopicEntry, 1)
	if err != nil {
		return err
	}
	h.unsubs = append(h.unsubs, sub.Unsubscribe)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for range sub.C {
			h.wakeAll()
		}
	}()
	return nil
}

func (h *Hub) unsubscribeAll() {
	for _, u := range h.unsubs {
		u()
	}
	h.wg.Wait()
}

// Add registers one stream. It returns ErrTooManyStreams at the cap and
// ErrHubClosed after Close.
func (h *Hub) Add() (*sseClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	if len(h.clients) >= h.opts.SSEMaxClients {
		return nil, ErrTooManyStreams
	}
	c := &sseClient{
		ch:             make(chan sseEvent, h.opts.SSEBuffer),
		logWake:        make(chan struct{}, 1),
		dropped:        map[string]int{},
		lastBusDropped: map[string]uint64{},
	}
	for _, s := range h.subs {
		c.lastBusDropped[s.topic] = s.dropped()
	}
	h.clients[c] = struct{}{}
	return c, nil
}

func (h *Hub) Remove(c *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		c.close()
	}
}

// Close unsubscribes (forwarders stop), then closes every client queue so
// streaming handlers return. Add fails afterwards.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	close(h.done)
	h.mu.Unlock()
	h.unsubscribeAll()
	h.mu.Lock()
	for c := range h.clients {
		delete(h.clients, c)
		c.close()
	}
	h.mu.Unlock()
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) broadcast(topic string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	for c := range h.clients {
		if !c.offer(topic, sseEvent{topic: topic, data: data}) {
			c.drop(topic)
		}
	}
}

func (h *Hub) wakeAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	for c := range h.clients {
		c.wake()
	}
}

// pollBusDropped reports each non-log subscription's drop delta since the
// client's last poll. log.entry is excluded: a lost wake-up is recovered
// from the ring.
func (h *Hub) pollBusDropped(c *sseClient) []dropped {
	var out []dropped
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range h.subs {
		cur := s.dropped()
		prev := c.lastBusDropped[s.topic]
		if cur <= prev {
			continue
		}
		c.lastBusDropped[s.topic] = cur
		delta := int(cur - prev)
		n := newDropped("bus_dropped")
		n.Topics[s.topic] = delta
		n.Total = delta
		out = append(out, n)
	}
	return out
}

type sseEvent struct {
	topic string
	data  []byte
}

type sseClient struct {
	ch             chan sseEvent
	logWake        chan struct{}
	mu             sync.Mutex
	closed         bool
	dropped        map[string]int
	lastBusDropped map[string]uint64
}

func (c *sseClient) offer(_ string, ev sseEvent) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return true
	}
	select {
	case c.ch <- ev:
		return true
	default:
		return false
	}
}

func (c *sseClient) drop(topic string) {
	c.mu.Lock()
	c.dropped[topic]++
	c.mu.Unlock()
}

func (c *sseClient) takeDropped() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dropped) == 0 {
		return nil
	}
	out := c.dropped
	c.dropped = map[string]int{}
	return out
}

func (c *sseClient) wake() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.logWake <- struct{}{}:
	default:
	}
}

func (c *sseClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.ch)
}

// dropped is the one synthetic loss notice. Topics is always non-nil.
type dropped struct {
	Reason    string         `json:"reason"`
	Topics    map[string]int `json:"topics"`
	Total     int            `json:"total"`
	OldestSeq *uint64        `json:"oldest_seq,omitempty"`
	LastSeq   *uint64        `json:"last_seq,omitempty"`
}

func newDropped(reason string) dropped {
	return dropped{Reason: reason, Topics: map[string]int{}}
}

func u64ptr(v uint64) *uint64 { return &v }

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	since, hasSince, err := sseSince(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	c, err := s.hub.Add()
	if err != nil {
		if errors.Is(err, ErrTooManyStreams) {
			w.Header().Set("Retry-After", "5")
			s.writeAPIError(w, r, http.StatusServiceUnavailable, &APIError{Status: http.StatusServiceUnavailable,
				Code: "too_many_streams", Message: "too many event streams are open"})
			return
		}
		s.writeAPIError(w, r, http.StatusServiceUnavailable, shuttingDown())
		return
	}
	defer s.hub.Remove(c)

	cursor := uint64(0)
	if hasSince {
		cursor = since
	} else {
		_, cursor = s.services.Logs.Bounds()
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, timeout: s.opts.SSEWriteTimeout}
	if err := sw.retry(); err != nil {
		return
	}
	cursor, err = s.tailLogs(r.Context(), sw, cursor)
	if err != nil {
		return
	}
	ticker := time.NewTicker(s.opts.SSEHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.hub.done:
			return
		case <-c.logWake:
			if err := s.writeBusDropped(sw, c); err != nil {
				return
			}
			if cursor, err = s.tailLogs(r.Context(), sw, cursor); err != nil {
				return
			}
		case ev, ok := <-c.ch:
			if !ok {
				return
			}
			if err := s.writeBusDropped(sw, c); err != nil {
				return
			}
			if err := s.writeClientDropped(sw, c); err != nil {
				return
			}
			if err := sw.event(ev.topic, ev.data); err != nil {
				return
			}
			if cursor, err = s.tailLogs(r.Context(), sw, cursor); err != nil {
				return
			}
		case <-ticker.C:
			if err := s.writeBusDropped(sw, c); err != nil {
				return
			}
			if err := s.writeClientDropped(sw, c); err != nil {
				return
			}
			if cursor, err = s.tailLogs(r.Context(), sw, cursor); err != nil {
				return
			}
			if err := sw.heartbeat(); err != nil {
				return
			}
		}
	}
}

func sseSince(r *http.Request) (uint64, bool, error) {
	if value, present, err := queryUint(r, "since", invalidSince); present {
		return value, true, err
	}
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		v, err := strconv.ParseUint(header, 10, 64)
		if err != nil {
			return 0, true, invalidSince()
		}
		return v, true, nil
	}
	return 0, false, nil
}

func invalidSince() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_since", Message: "invalid since cursor"}
}

func (s *Server) writeBusDropped(sw *sseWriter, c *sseClient) error {
	for _, n := range s.hub.pollBusDropped(c) {
		if err := sw.dropped(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) writeClientDropped(sw *sseWriter, c *sseClient) error {
	topics := c.takeDropped()
	if len(topics) == 0 {
		return nil
	}
	n := newDropped("buffer_full")
	n.Topics = topics
	for _, count := range topics {
		n.Total += count
	}
	return sw.dropped(n)
}

// LogWakeEvent announces that new entries are in the log ring. It carries
// only the newest sequence and how many entries are waiting; the Logs
// screen reads them from /api/logs?since=.
const LogWakeEvent = "log.wake"

// logWake is the complete payload of LogWakeEvent: no message text and no
// attributes cross the event stream.
type logWake struct {
	Seq   uint64 `json:"seq"`
	Count int    `json:"count"`
}

// tailLogs is the one cursor/rotation helper used by the seed and every
// tail read. A read that found entries writes at most one coalesced
// log.wake frame; the cursor advances only after that frame is flushed.
func (s *Server) tailLogs(ctx context.Context, w *sseWriter, cursor uint64) (uint64, error) {
	_, oldest, last := s.services.Logs.ReadLimit(cursor, 1, false)
	switch {
	case cursor > last:
		n := newDropped("seq_reset")
		n.OldestSeq, n.LastSeq = u64ptr(oldest), u64ptr(last)
		if err := w.dropped(n); err != nil {
			return cursor, err
		}
		cursor = 0
	case cursor > 0 && oldest > 0 && cursor < oldest-1:
		missed := int(oldest - cursor - 1)
		n := newDropped("ring_rotated")
		n.Topics[logx.TopicEntry.Name()] = missed
		n.Total = missed
		n.OldestSeq = u64ptr(oldest)
		if err := w.dropped(n); err != nil {
			return cursor, err
		}
		cursor = oldest - 1
	}
	if err := ctx.Err(); err != nil {
		return cursor, err
	}
	select {
	case <-s.hub.done:
		return cursor, errHubClosed
	default:
	}
	base := cursor
	if base == 0 && oldest > 0 {
		// A zero cursor means "everything retained", including a ring
		// whose first sequence is not 1 (a restarted test world).
		base = oldest - 1
	}
	if last <= base {
		return cursor, nil
	}
	if err := w.wake(logWake{Seq: last, Count: int(last - base)}); err != nil {
		return cursor, err
	}
	return last, nil
}

type sseWriter struct {
	w       http.ResponseWriter
	timeout time.Duration
}

func (w *sseWriter) write(frame []byte) error {
	rc := http.NewResponseController(w.w)
	if err := rc.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := w.w.Write(frame); err != nil {
		return err
	}
	if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (w *sseWriter) retry() error {
	return w.write([]byte("retry: " + strconv.Itoa(sseRetryMillis) + "\n\n"))
}

func (w *sseWriter) heartbeat() error { return w.write([]byte(": heartbeat\n\n")) }

func (w *sseWriter) event(topic string, data []byte) error {
	return w.write(sseFrame(topic, data, nil))
}

func (w *sseWriter) wake(n logWake) error {
	data, err := marshal(n)
	if err != nil {
		return err
	}
	id := n.Seq
	return w.write(sseFrame(LogWakeEvent, data, &id))
}

func (w *sseWriter) dropped(n dropped) error {
	data, err := marshal(n)
	if err != nil {
		return err
	}
	return w.write(sseFrame("dropped", data, nil))
}

// sseFrame writes one event. A payload newline would be split into another
// data: line (json.Marshal never emits one, but the framing is complete).
func sseFrame(event string, data []byte, id *uint64) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "event: %s\n", event)
	if id != nil {
		fmt.Fprintf(&b, "id: %d\n", *id)
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.Bytes()
}
