package fixturetest

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// VectorCache keeps the float32 vectors a real embedder returned, by
// vector space and exact input (a digest of Embedder.Input), in files
// under a private directory (one append-only file per space), so the
// fixture harnesses embed each text once across runs and fresh stores:
// a slow local model (≈5 messages/s on the dev CPU) is paid once.
// Dev-time test support: never in the shipped app.
type VectorCache struct {
	dir    string
	mu     sync.Mutex
	spaces map[string]*spaceFile
}

type spaceFile struct {
	dims int
	vec  map[[8]byte][]float32
	f    *os.File
}

// cacheHeader is the first line of a space's file.
type cacheHeader struct {
	Space      string `json:"space"`
	Dimensions int    `json:"dimensions"`
}

// OpenVectorCache opens (creating it, mode 700) the cache directory.
func OpenVectorCache(dir string) (*VectorCache, error) {
	if err := fsx.EnsureDir(dir); err != nil {
		return nil, err
	}
	return &VectorCache{dir: dir, spaces: map[string]*spaceFile{}}, nil
}

// Close closes the space files.
func (c *VectorCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for _, s := range c.spaces {
		errs = append(errs, s.f.Close())
	}
	c.spaces = map[string]*spaceFile{}
	return errors.Join(errs...)
}

// Key is the cache key of an exact input: the raw 8-byte prefix of the one
// short hash, digest.Sum.
func Key(input string) [8]byte { return [8]byte(digest.Sum([]byte(input), 8)) }

// Get returns the cached vector of input in space (nil when absent).
func (c *VectorCache) Get(space string, dims int, input string) ([]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.open(space, dims)
	if err != nil {
		return nil, err
	}
	return s.vec[Key(input)], nil
}

// Len is the number of vectors cached in space.
func (c *VectorCache) Len(space string, dims int) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.open(space, dims)
	if err != nil {
		return 0, err
	}
	return len(s.vec), nil
}

// Put appends vectors (one per input) to space's file and keeps them.
func (c *VectorCache) Put(space string, dims int, inputs []string, vecs [][]float32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.open(space, dims)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(s.f)
	rec := make([]byte, 8+4*dims)
	for i, in := range inputs {
		if len(vecs[i]) != dims {
			return fmt.Errorf("vector cache: a %d-long vector in a %d-dimension space", len(vecs[i]), dims)
		}
		k := Key(in)
		if _, ok := s.vec[k]; ok {
			continue
		}
		copy(rec, k[:])
		for j, x := range vecs[i] {
			binary.LittleEndian.PutUint32(rec[8+4*j:], math.Float32bits(x))
		}
		_, _ = w.Write(rec)
		s.vec[k] = vecs[i]
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return s.f.Sync()
}

// open loads (or creates) a space's file; a record cut by an interrupted
// write is dropped (the file is truncated to the last whole record).
func (c *VectorCache) open(space string, dims int) (*spaceFile, error) {
	if s := c.spaces[space]; s != nil {
		if s.dims != dims {
			return nil, fmt.Errorf("vector cache: space %s holds %d-dimension vectors, not %d", space, s.dims, dims)
		}
		return s, nil
	}
	path := filepath.Join(c.dir, digest.Hex([]byte(space), 16)+".vec")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fsx.FilePerm)
	if err != nil {
		return nil, err
	}
	s := &spaceFile{dims: dims, vec: map[[8]byte][]float32{}, f: f}
	if err := s.load(space); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("vector cache %s: %w", path, err)
	}
	c.spaces[space] = s
	return s, nil
}

func (s *spaceFile) load(space string) error {
	r := bufio.NewReaderSize(s.f, 1<<20)
	line, err := r.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 { // a new file
		h, _ := json.Marshal(cacheHeader{Space: space, Dimensions: s.dims})
		_, err := s.f.Write(append(h, '\n'))
		return err
	}
	if err != nil {
		return err
	}
	var h cacheHeader
	if err := json.Unmarshal(line, &h); err != nil || h.Space != space || h.Dimensions != s.dims {
		return fmt.Errorf("header %q is not space %s with %d dimensions", line, space, s.dims)
	}
	offset := int64(len(line))
	rec := make([]byte, 8+4*s.dims)
	for {
		if _, err := io.ReadFull(r, rec); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		v := make([]float32, s.dims)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(rec[8+4*i:]))
		}
		s.vec[[8]byte(rec[:8])] = v
		offset += int64(len(rec))
	}
	if err := s.f.Truncate(offset); err != nil {
		return err
	}
	_, err = s.f.Seek(offset, io.SeekStart)
	return err
}

// Source wraps an embedder source so every Embed answers from the cache
// first and caches what the real embedder returns.
func (c *VectorCache) Source(src interface {
	Embedder() (engine.Embedder, error)
}) *CachedSource {
	return &CachedSource{cache: c, src: src}
}

// CachedSource is an index.EmbedderSource answering from a VectorCache.
type CachedSource struct {
	cache *VectorCache
	src   interface {
		Embedder() (engine.Embedder, error)
	}
	mu           sync.Mutex
	Hits, Misses int
}

// Embedder returns the cached embedder.
func (s *CachedSource) Embedder() (engine.Embedder, error) {
	e, err := s.src.Embedder()
	if err != nil {
		return nil, err
	}
	return &cachedEmbedder{Embedder: e, s: s}, nil
}

type cachedEmbedder struct {
	engine.Embedder
	s *CachedSource
}

// Embed answers cached inputs from the cache and embeds the rest.
func (e *cachedEmbedder) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	space, dims := e.Space(), e.Dimensions()
	out := engine.Embeddings{Vectors: make([][]float32, len(texts))}
	var miss []int
	var missTexts []engine.Text
	for i, t := range texts {
		v, err := e.s.cache.Get(space, dims, e.Input(t))
		if err != nil {
			return out, err
		}
		if v == nil {
			miss, missTexts = append(miss, i), append(missTexts, t)
			continue
		}
		out.Vectors[i] = v
	}
	e.s.mu.Lock()
	e.s.Hits += len(texts) - len(miss)
	e.s.Misses += len(miss)
	e.s.mu.Unlock()
	if len(miss) == 0 {
		return out, nil
	}
	res, err := e.Embedder.Embed(ctx, missTexts)
	out.Usage, out.Truncated = res.Usage, res.Truncated
	if err != nil {
		return engine.Embeddings{Usage: res.Usage}, err
	}
	inputs := make([]string, len(miss))
	for j, i := range miss {
		out.Vectors[i] = res.Vectors[j]
		inputs[j] = e.Input(missTexts[j])
	}
	return out, e.s.cache.Put(space, dims, inputs, res.Vectors)
}
