package threadintel

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// TestThreadResolveMatchesStateless proves the Thread memo is only an
// execution detail: every golden thread, including every option combination,
// resolves to the identical Assessment through the stateless entry point.
func TestThreadResolveMatchesStateless(t *testing.T) {
	for _, c := range goldenCorpus() {
		want := Resolve(c.Thread, c.Options)
		got := NewThread(c.Thread, nil).Resolve(c.Options)
		if got != want {
			t.Fatalf("%s: Thread.Resolve\n got %#v\nwant %#v", c.Name, got, want)
		}
	}
}

// TestThreadFrontierMatchesStateless walks the deferred-evidence loop both
// ways: the stateless function (which re-sorts and re-parses on every round)
// and the Thread (which keeps one order and one decision per member). The
// frontier sequence and the final assessment must be identical, including
// the number of rows fetched.
func TestThreadFrontierMatchesStateless(t *testing.T) {
	for _, c := range goldenCorpus() {
		if len(c.Thread) == 0 {
			continue
		}
		content := make(map[int64][2]string, len(c.Thread))
		for _, m := range c.Thread {
			content[m.RowID] = [2]string{m.Body, m.Headers}
		}
		// The store reads metadata first and leaves body/headers empty until
		// the frontier asks for them.
		initial := make([]Message, len(c.Thread))
		for i, m := range c.Thread {
			m.Body, m.Headers = "", ""
			initial[i] = m
		}

		stateless := append([]Message(nil), initial...)
		loadedStateless := map[int64]struct{}{}
		var wantOrder []int64
		for {
			id, ok := EvidenceFrontier(stateless, loadedStateless, c.Options)
			if !ok {
				break
			}
			wantOrder = append(wantOrder, id)
			for i := range stateless {
				if stateless[i].RowID == id {
					stateless[i].Body, stateless[i].Headers = content[id][0], content[id][1]
				}
			}
			loadedStateless[id] = struct{}{}
		}

		thread := NewThread(initial, nil)
		loaded := map[int64]struct{}{}
		var gotOrder []int64
		for {
			id, ok := thread.EvidenceFrontier(loaded, c.Options)
			if !ok {
				break
			}
			gotOrder = append(gotOrder, id)
			for i := range initial {
				if initial[i].RowID == id {
					thread.SetContent(i, content[id][0], content[id][1])
				}
			}
			loaded[id] = struct{}{}
		}

		if fmt.Sprint(gotOrder) != fmt.Sprint(wantOrder) {
			t.Fatalf("%s: frontier order %v, want %v", c.Name, gotOrder, wantOrder)
		}
		if got, want := thread.Resolve(c.Options), Resolve(stateless, c.Options); got != want {
			t.Fatalf("%s: Thread.Resolve after deferred loads\n got %#v\nwant %#v", c.Name, got, want)
		}
	}
}

// TestSetContentRecomputes: replacing a member's body invalidates its parsed
// decision, so a body that changes from a trivial acknowledgement to a real
// move changes the resolved state without rebuilding the Thread.
func TestSetContentRecomputes(t *testing.T) {
	ask := goldenInbound(1, 0, "Could you review the plan?")
	ask.ReplyNeeded = true
	thread := NewThread([]Message{ask, goldenOwner(2, 1, "Thanks")}, NewCache(16))
	if got := thread.Resolve(Options{}); got.State != AwaitingOwner || got.Reason != ReasonInboundLast {
		t.Fatalf("trivial reply state %+v", got)
	}
	thread.SetContent(1, "Done, the plan is attached.", "")
	if got := thread.Resolve(Options{}); got.State != AnsweredByOwner || got.Reason != ReasonOwnerReplied {
		t.Fatalf("substantive reply state %+v", got)
	}
}

func testKey(rowID, revision int64, digest uint64) evidenceKey {
	return evidenceKey{rowID: rowID, revision: revision, digest: digest, min: 1}
}

// TestCacheEvictsAtTheBound: the cache never holds more than its configured
// entry count, and evicts in insertion order when full.
func TestCacheEvictsAtTheBound(t *testing.T) {
	c := NewCache(8)
	for i := range 20 {
		c.put(testKey(int64(i), 1, uint64(i)), parsedEvidence{substantive: i%2 == 0})
	}
	st := c.Stats()
	if st.Entries != 8 || st.Evictions != 12 {
		t.Fatalf("stats %+v, want 8 entries / 12 evictions", st)
	}
	for i := 12; i < 20; i++ {
		if _, ok := c.get(testKey(int64(i), 1, uint64(i))); !ok {
			t.Fatalf("entry %d was evicted while inside the bound", i)
		}
	}
	for i := range 12 {
		if _, ok := c.get(testKey(int64(i), 1, uint64(i))); ok {
			t.Fatalf("entry %d survived eviction", i)
		}
	}
}

// TestCacheKeySeparatesRevisionAndContent: a revision bump or a changed body
// must miss, so a rewrite is recomputed even when the row id is unchanged.
func TestCacheKeySeparatesRevisionAndContent(t *testing.T) {
	c := NewCache(16)
	base := testKey(7, 3, 100)
	c.put(base, parsedEvidence{substantive: true})
	if _, ok := c.get(base); !ok {
		t.Fatal("the base key did not hit")
	}
	if _, ok := c.get(testKey(7, 4, 100)); ok {
		t.Fatal("a revision bump hit the old entry")
	}
	if _, ok := c.get(testKey(7, 3, 101)); ok {
		t.Fatal("a changed content digest hit the old entry")
	}
}

// TestThreadSecondPassParsesNothing is the zero-parse contract: an unchanged
// second resolution over the same cache computes no parsed evidence at all.
func TestThreadSecondPassParsesNothing(t *testing.T) {
	cache := NewCache(1024)
	cases := goldenCorpus()
	for _, c := range cases {
		NewThread(c.Thread, cache).Resolve(c.Options)
	}
	first := cache.Stats()
	if first.Parses == 0 {
		t.Fatalf("first pass did not parse: %+v", first)
	}
	for _, c := range cases {
		NewThread(c.Thread, cache).Resolve(c.Options)
	}
	second := cache.Stats()
	if second.Parses != first.Parses {
		t.Fatalf("second pass parsed %d more messages (stats %+v)", second.Parses-first.Parses, second)
	}
	if second.Hits <= first.Hits {
		t.Fatalf("second pass took no hits (stats %+v)", second)
	}
}

// TestThreadRevisionBumpRecomputes: the store bumps messages.revision on every
// write; the resolver must parse the new content and change its answer.
func TestThreadRevisionBumpRecomputes(t *testing.T) {
	c := NewCache(64)
	ask := goldenInbound(1, 0, "Could you review the plan?")
	ask.ReplyNeeded = true
	reply := goldenOwner(2, 1, "Thanks")
	reply.Revision = 1
	if got := NewThread([]Message{ask, reply}, c).Resolve(Options{}); got.State != AwaitingOwner {
		t.Fatalf("trivial reply state %+v", got)
	}
	before := c.Stats().Parses
	reply.Revision = 2
	reply.Body = "Done, the plan is attached."
	reply.InReplyTo = "<m1@example.test>"
	if got := NewThread([]Message{ask, reply}, c).Resolve(Options{}); got.State != AnsweredByOwner {
		t.Fatalf("rewritten reply state %+v", got)
	}
	if got := c.Stats().Parses - before; got == 0 {
		t.Fatal("a revision bump served the old parsed evidence")
	}
}

// TestCacheConcurrent exercises the shared cache under the race detector:
// concurrent builds read and write the same keys without corrupting the map,
// the bound or the counters.
func TestCacheConcurrent(t *testing.T) {
	c := NewCache(128)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 2000 {
				key := testKey(int64(i%300), int64(i%5), uint64(i%17))
				if _, ok := c.get(key); ok {
					continue
				}
				c.put(key, parsedEvidence{substantive: worker%2 == 0})
			}
		}()
	}
	wg.Wait()
	if st := c.Stats(); st.Entries > 128 {
		t.Fatalf("cache grew past its bound: %+v", st)
	}
}

// TestCacheRetainedBytesStaysBounded is the memory contract at the two scales
// the store is documented for: a 35k-message mailbox fits, and a million-row
// mailbox does not grow the cache past its fixed entry bound.
func TestCacheRetainedBytesStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("measures retained heap over a million inserts")
	}
	measure := func(label string, n int) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		c := NewCache(DefaultEvidenceEntries)
		for i := range n {
			c.put(testKey(int64(i), 1, uint64(i)*0x9e3779b97f4a7c15), parsedEvidence{
				autoReply: i%3 == 0, substantive: i%7 == 0,
			})
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		retained := float64(after.HeapAlloc) - float64(before.HeapAlloc)
		t.Logf("%s: %d inserts, %d entries retained, %.1f MiB (%.0f B/entry)",
			label, n, c.Stats().Entries, retained/(1<<20), retained/float64(c.Stats().Entries))
		if n == 1_000_000 && retained > 64<<20 {
			t.Fatalf("the bounded cache retained %.1f MiB; want <= 64 MiB", retained/(1<<20))
		}
	}
	measure("35k rows", 35_000)
	measure("1M rows", 1_000_000)
}
