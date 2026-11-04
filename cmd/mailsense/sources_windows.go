//go:build windows

package main

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/source/outlook"
	"github.com/LLM-4-People/mailsense/internal/store"
)

const outlookSourceID = "outlook"

// newSourceRegistry registers the platform's connector. Classic Outlook
// is the only production source on Windows; the connector attaches to a
// running Outlook and never starts it by itself.
func newSourceRegistry(ctx context.Context, cfg *config.Config, bus *events.Bus, st *store.Store) (*source.Registry, error) {
	reg := source.NewRegistry(bus)
	c, err := outlook.New(outlookSourceID, cfg, bus, nil)
	if err != nil {
		return nil, fmt.Errorf("outlook connector: %w", err)
	}
	if err := c.Keep(ctx, st); err != nil {
		c.Close()
		return nil, fmt.Errorf("outlook state: %w", err)
	}
	if err := reg.Add(c); err != nil {
		c.Close()
		return nil, err
	}
	return reg, nil
}
