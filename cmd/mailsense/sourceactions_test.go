package main

import (
	"context"
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// plainConnector declares no owner action: the source actions must answer a
// named conflict instead of calling a nil capability or falling back to sync.
type plainConnector struct{}

func (plainConnector) ID() string                                      { return "archive" }
func (plainConnector) Kind() string                                    { return "archive" }
func (plainConnector) Capabilities() source.Capabilities               { return source.Capabilities{} }
func (plainConnector) Discover(context.Context) ([]mail.Folder, error) { return nil, nil }
func (plainConnector) Mailboxes() []source.Mailbox                     { return nil }
func (plainConnector) Sync(context.Context, mail.Folder, source.Cursor, source.Sink) (source.Cursor, error) {
	return "", nil
}

func sourceWire(connectors ...source.Connector) *wire {
	reg := source.NewRegistry(nil)
	for _, c := range connectors {
		_ = reg.Add(c)
	}
	return &wire{app: &app{registry: reg}}
}

func TestSourceActionsRejectMissingCapabilities(t *testing.T) {
	a := sourcesAPI{sourceWire(plainConnector{})}
	cases := []struct {
		name   string
		action string
		run    func(context.Context, string) (api.SourceActionView, error)
	}{
		{"start", "start", a.Start},
		{"resume", "resume", a.Resume},
		{"retry-details", "retry-details", a.RetryDetails},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.run(t.Context(), "archive")
			var apiErr *api.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "action_unsupported" {
				t.Fatalf("error %v, want 409 action_unsupported", err)
			}
			if apiErr.Params["action"] != tc.action || apiErr.Params["source"] != "archive" {
				t.Fatalf("params %+v", apiErr.Params)
			}
		})
	}
}

// A missing registry or connector is a 404, never a nil dereference. This is
// the Linux shape: no connector implements the Windows-only capabilities.
func TestSourceActionsWithoutConnector(t *testing.T) {
	for name, a := range map[string]sourcesAPI{
		"empty registry": {sourceWire()},
		"no wire":        {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := a.Start(t.Context(), "outlook")
			var apiErr *api.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "unknown_source" {
				t.Fatalf("error %v, want 404 unknown_source", err)
			}
		})
	}
}
