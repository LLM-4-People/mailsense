package search

import (
	"context"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// warmQueryTimeout bounds the startup warm-up. It is deliberately generous:
// the first query embedding may have to load and tune the local runtime,
// while a normal search is still guarded by search.embed_timeout.
const warmQueryTimeout = 60 * time.Second

// warmQueryText is the one fixed text the startup warm-up embeds. It carries
// no mail content and no result is used.
const warmQueryText = "semantic search warm-up"

// Prepare loads everything a semantic search reads without embedding a
// query: the in-memory vector copy, the coverage count and both interactive
// filter sets. Startup calls it before the port is announced, so the first
// question does not pay for a cold vector load or a mailbox-wide id list. It
// does not start the local model: the background warm-up embeds a query once
// the API is served.
func (s *Searcher) Prepare(ctx context.Context) error {
	e, err := s.src.Embedder()
	if err != nil {
		return err
	}
	if err := s.refresh(ctx, s.st, e.Space(), e.Dimensions()); err != nil {
		return fmt.Errorf("search: warm-up vector refresh: %w", err)
	}
	// Coverage is the other cold read: counting the eligible set scans every
	// visible message. Warming it here keeps the first user search inside its
	// budget instead of paying that scan (and usually being canceled) on the
	// query path.
	covered, eligible := s.coverage(ctx)
	// The filter sets are the other cold read: the owner scope lists every
	// owner-facing message, and paying that scan inside a question's budget
	// was one reason a warm index looked cold. Both interactive filters are
	// listed here, once, before the port is announced.
	for _, f := range []store.SearchFilter{{}, {OwnerOnly: true}} {
		if _, err := s.allowed(ctx, Query{Filter: f}); err != nil {
			return fmt.Errorf("search: warm-up filter set: %w", err)
		}
	}
	s.log.Info("semantic index prepared", "vectors", s.vectors.Len(), "covered", covered,
		"eligible", eligible, "space", e.Space())
	return nil
}

// Warm prepares the semantic side for the first user search: Prepare (vector
// copy, coverage, filter sets) and one fixed query embedding with a generous
// bound, so the runtime and model are hot. It checks no learning gate, reads
// no mail content and returns no result. A failure is returned so the caller
// can log it; the next search still degrades explicitly.
func (s *Searcher) Warm(ctx context.Context) error {
	if err := s.Prepare(ctx); err != nil {
		return err
	}
	e, err := s.src.Embedder()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, warmQueryTimeout)
	defer cancel()
	if _, err := e.Embed(ctx, []engine.Text{{Purpose: engine.PurposeQuery, Body: warmQueryText}}); err != nil {
		return fmt.Errorf("search: warm-up query embedding: %w", err)
	}
	s.log.Debug("semantic search warmed", "vectors", s.vectors.Len(), "space", e.Space())
	return nil
}
