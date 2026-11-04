package fixturetest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

type countingEmbedder struct {
	engine.Embedder
	calls, texts int
}

func (e *countingEmbedder) Space() string              { return "s/1" }
func (e *countingEmbedder) Dimensions() int            { return 2 }
func (e *countingEmbedder) Input(t engine.Text) string { return t.Title + "|" + t.Body }
func (e *countingEmbedder) Embed(_ context.Context, texts []engine.Text) (engine.Embeddings, error) {
	e.calls++
	e.texts += len(texts)
	out := engine.Embeddings{}
	for _, t := range texts {
		out.Vectors = append(out.Vectors, []float32{float32(len(t.Body)), 1})
	}
	return out, nil
}

type fixedSource struct{ e engine.Embedder }

func (s fixedSource) Embedder() (engine.Embedder, error) { return s.e, nil }

func TestVectorCacheEmbedsEachInputOnceAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	inner := &countingEmbedder{}
	c, err := OpenVectorCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := c.Source(fixedSource{inner}).Embedder()
	texts := []engine.Text{{Body: "a"}, {Body: "bb"}}
	if _, err := e.Embed(t.Context(), texts); err != nil {
		t.Fatal(err)
	}
	res, err := e.Embed(t.Context(), append(texts, engine.Text{Body: "ccc"}))
	if err != nil || inner.texts != 3 || res.Vectors[2][0] != 3 || res.Vectors[1][0] != 2 {
		t.Fatalf("%v %v texts=%d", res.Vectors, err, inner.texts)
	}
	_ = c.Close()
	// An interrupted write leaves a cut record: it is dropped at the next open.
	files, _ := filepath.Glob(filepath.Join(dir, "*.vec"))
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.Write([]byte{1, 2, 3})
	_ = f.Close()
	c2, err := OpenVectorCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if n, err := c2.Len("s/1", 2); err != nil || n != 3 {
		t.Fatalf("len %d %v", n, err)
	}
	if _, err := c2.Len("s/1", 3); err == nil {
		t.Error("another length in the same space accepted")
	}
	e2, _ := c2.Source(fixedSource{inner}).Embedder()
	if _, err := e2.Embed(t.Context(), texts); err != nil || inner.texts != 3 {
		t.Fatalf("re-embedded after reopen: %d %v", inner.texts, err)
	}
}
