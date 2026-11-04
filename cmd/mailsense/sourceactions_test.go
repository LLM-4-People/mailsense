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

// startConnector, resumeConnector and detailConnector implement one optional
// interface each; allActionsConnector implements every one.
type startConnector struct{ plainConnector }

func (startConnector) StartOutlook(context.Context) error { return nil }

type resumeConnector struct{ plainConnector }

func (resumeConnector) Resume() error { return nil }

type detailConnector struct{ plainConnector }

func (detailConnector) ReadDetails(context.Context, source.ItemRef) (mail.Details, error) {
	return mail.Details{}, nil
}

type allActionsConnector struct{ plainConnector }

func (allActionsConnector) StartOutlook(context.Context) error { return nil }
func (allActionsConnector) Resume() error                      { return nil }
func (allActionsConnector) ReadDetails(context.Context, source.ItemRef) (mail.Details, error) {
	return mail.Details{}, nil
}

// The advertised owner actions and the action dispatcher use the same table:
// for every registered source, an action is offered exactly when the
// dispatcher can run it instead of answering 409 action_unsupported.
func TestAdvertisedActionsMatchTheDispatcher(t *testing.T) {
	actions := []struct {
		action source.OwnerAction
		run    func(sourcesAPI, string) (api.SourceActionView, error)
	}{
		{source.ActionStart, func(a sourcesAPI, id string) (api.SourceActionView, error) { return a.Start(context.Background(), id) }},
		{source.ActionResume, func(a sourcesAPI, id string) (api.SourceActionView, error) { return a.Resume(context.Background(), id) }},
		{source.ActionRetryDetails, func(a sourcesAPI, id string) (api.SourceActionView, error) {
			return a.RetryDetails(context.Background(), id)
		}},
	}
	cases := []struct {
		name string
		c    source.Connector
		want []source.OwnerAction
	}{
		{"no action", plainConnector{}, nil},
		{"start", startConnector{}, []source.OwnerAction{source.ActionStart}},
		{"resume", resumeConnector{}, []source.OwnerAction{source.ActionResume}},
		{"retry-details", detailConnector{}, []source.OwnerAction{source.ActionRetryDetails}},
		{"all", allActionsConnector{}, []source.OwnerAction{source.ActionStart, source.ActionResume, source.ActionRetryDetails}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := sourceWire(tc.c)
			a := sourcesAPI{reg}
			registered := reg.app.registry.List()
			if len(registered) != 1 {
				t.Fatalf("registered sources %d", len(registered))
			}
			advertised := source.ConnectorActions(registered[0])
			if len(advertised) != len(tc.want) {
				t.Fatalf("advertised %v, want %v", advertised, tc.want)
			}
			for i, action := range tc.want {
				if advertised[i] != action {
					t.Fatalf("advertised %v, want %v", advertised, tc.want)
				}
			}
			for _, entry := range actions {
				_, err := entry.run(a, "archive")
				var apiErr *api.APIError
				unsupported := errors.As(err, &apiErr) && apiErr.Code == "action_unsupported"
				offered := false
				for _, action := range advertised {
					if action == entry.action {
						offered = true
					}
				}
				if offered == unsupported {
					t.Fatalf("%s: advertised %v, dispatcher unsupported %v (err %v)", entry.action, offered, unsupported, err)
				}
			}
		})
	}
}
