//go:build fixture

package threads

// BenchmarkThreadsLoad over the imported mailbox: the production projection
// against the full-column read, so the read's cost and the field reduction
// come from one fixture and one process.
//
//	FIXTURE_JSONL=$FIXTURE_DIR/mailbox.jsonl \
//	go test -tags fixture -run '^$' -bench '^BenchmarkThreadsLoad$' -benchmem -benchtime=1x ./internal/threads

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func BenchmarkThreadsLoad(b *testing.B) {
	cfg, st := fixturetest.Import(b, fixturetest.Path(b), nil)
	set, err := SettingsFrom(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("selected m.<column> references: full=%d projected=%d", len(messageColumnRefs(store.MessageFields{}.Select())), len(messageColumnRefs(threadFields.Select())))
	for _, tc := range []struct {
		name   string
		fields store.MessageFields
	}{
		{"full", store.MessageFields{}},
		{"projected", threadFields},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			if _, err := load(b.Context(), st, set, tc.fields); err != nil { // warm the pages both reads share
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				a, err := load(b.Context(), st, set, tc.fields)
				if err != nil {
					b.Fatal(err)
				}
				if len(a.Messages) == 0 {
					b.Fatal("no messages")
				}
			}
		})
	}
}
