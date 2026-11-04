//go:build !windows

package main

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// newSourceRegistry has no production connector outside Windows: the
// Outlook connector is COM-only and the legacy archive needs an export
// path that no setting names. The API reports an empty source list, so
// discover/sync answer 503 no_source instead of pretending to have mail.
func newSourceRegistry(_ context.Context, _ *config.Config, bus *events.Bus, _ *store.Store) (*source.Registry, error) {
	return source.NewRegistry(bus), nil
}
