package logx

import (
	"strings"
	"testing"
)

func TestRingOrderSinceAndResize(t *testing.T) {
	r := NewRing(3)
	for i := 1; i <= 5; i++ {
		r.add(Entry{Seq: uint64(i)})
	}
	got := r.Snapshot()
	if len(got) != 3 || got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("ring: %+v", got)
	}
	if s := r.Since(4); len(s) != 1 || s[0].Seq != 5 {
		t.Fatalf("since: %+v", s)
	}
	r.Resize(2)
	if s := r.Snapshot(); len(s) != 2 || s[0].Seq != 4 {
		t.Fatalf("resize keeps newest: %+v", s)
	}
	r.Resize(0)
	if r.Cap() != 1 {
		t.Fatal("capacity floor is 1")
	}
}

func TestLevelTableMisuse(t *testing.T) {
	for _, bad := range [][]string{{"x"}, {"x=loud"}} {
		if _, err := newLevelTable("info", bad); err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
	if _, err := newLevelTable("loud", nil); err == nil {
		t.Error("bad default level must be rejected")
	}
	if _, err := newRedactor([]string{"("}, "x", 1); err == nil {
		t.Error("bad pattern must be rejected")
	}
}

func TestTextFormat(t *testing.T) {
	line := Entry{Level: "INFO", Component: "c", Message: "m", Attrs: map[string]any{"b": 2, "a": 1}}.text()
	if !strings.HasSuffix(line, "INFO  [c] m a=1 b=2\n") {
		t.Fatalf("text: %q", line)
	}
}

func TestReadCaughtUpReturnsBoundsWithoutEntries(t *testing.T) {
	r := NewRing(8)
	for i := 0; i < 5; i++ {
		r.add(Entry{Seq: uint64(i + 1), Message: "m"})
	}
	entries, oldest, last := r.Read(5)
	if len(entries) != 0 || oldest != 1 || last != 5 {
		t.Fatalf("caught up: %v %d %d", entries, oldest, last)
	}
	entries, _, _ = r.Read(3)
	if len(entries) != 2 || entries[0].Seq != 4 || entries[1].Seq != 5 {
		t.Fatalf("new entries: %+v", entries)
	}
}
