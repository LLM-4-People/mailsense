package logx

import "testing"

func BenchmarkRingReadCaughtUp(b *testing.B) {
	r := NewRing(2000)
	for i := 0; i < 2000; i++ {
		r.add(Entry{Seq: uint64(i + 1), Message: "heartbeat"})
	}
	_, last := r.Bounds()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		entries, _, _ := r.Read(last)
		if len(entries) != 0 {
			b.Fatal("caught-up read returned entries")
		}
	}
}
