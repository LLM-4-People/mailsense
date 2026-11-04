package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// EmbeddingGeneration is the embedding-write invalidation signal: saving a
// vector, confirming one and recording a failure each advance it after the
// transaction commits, while reading states or writing mail does not.
func TestEmbeddingGenerationFollowsEmbeddingWrites(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z"))
	before := s.EmbeddingGeneration()

	if _, err := s.EmbeddingStates(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessage(ctx, "src", "store-1", "a"); err != nil {
		t.Fatal(err)
	}
	seedMsgs(t, s, msgAt("b", mail.RoleInbox, "2026-01-02T00:00:00Z"))
	if got := s.EmbeddingGeneration(); got != before {
		t.Fatalf("a mail read or write advanced EmbeddingGeneration: %d -> %d", before, got)
	}

	vec := vector.Quantize([]float32{1})
	if err := s.SaveEmbeddings(ctx, []EmbeddingRow{{MessageID: ids["a"], Space: "sp", TextHash: "h1", TextVersion: "v1", Vector: vec}}); err != nil {
		t.Fatal(err)
	}
	saved := s.EmbeddingGeneration()
	if saved == before {
		t.Fatal("SaveEmbeddings did not advance EmbeddingGeneration")
	}

	if err := s.ConfirmEmbeddings(ctx, map[int64]int64{ids["a"]: 4}, "v2"); err != nil {
		t.Fatal(err)
	}
	confirmed := s.EmbeddingGeneration()
	if confirmed == saved {
		t.Fatal("ConfirmEmbeddings did not advance EmbeddingGeneration")
	}

	if err := s.RecordIndexFailures(ctx, []IndexFailure{{MessageID: ids["a"], Revision: 4, Error: "boom"}}); err != nil {
		t.Fatal(err)
	}
	failed := s.EmbeddingGeneration()
	if failed == confirmed {
		t.Fatal("RecordIndexFailures did not advance EmbeddingGeneration")
	}

	// Empty writes commit nothing and must not invalidate a cached coverage.
	if err := s.SaveEmbeddings(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmEmbeddings(ctx, nil, "v2"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIndexFailures(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.EmbeddingGeneration(); got != failed {
		t.Fatalf("an empty embedding write advanced EmbeddingGeneration: %d -> %d", failed, got)
	}
}
