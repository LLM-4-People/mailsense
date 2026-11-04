package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/resource"
)

// TestResourceLimitErrorsKeepTheirCeiling: the refusal must reach the client
// as one typed code with the resource and both percentages, so the UI can
// compose a sentence in the owner's language instead of showing an English
// diagnostic.
func TestResourceLimitErrorsKeepTheirCeiling(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ceiling := &resource.LimitError{Kind: resource.Disk, Percent: 95.4, Limit: 90}
	mapped := srv.mapError(fmt.Errorf("start sync: %w", ceiling))
	if mapped.Status != http.StatusServiceUnavailable || mapped.Code != "resource_limit" {
		t.Fatalf("mapped %d %s", mapped.Status, mapped.Code)
	}
	if mapped.Params["resource"] != "disk" || mapped.Params["percent"] != "95" || mapped.Params["limit"] != "90" {
		t.Fatalf("params = %+v", mapped.Params)
	}
	if !errors.Is(mapped, resource.ErrLimited) && mapped.Code != "resource_limit" {
		t.Fatalf("mapped %+v", mapped)
	}
}

// TestResourceLimitClassWithoutDetailStillMaps: a wrapped class with no
// typed cause must not become an internal error.
func TestResourceLimitClassWithoutDetailStillMaps(t *testing.T) {
	srv, _, _ := newTestServer(t)
	mapped := srv.mapError(fmt.Errorf("queue: %w", resource.ErrLimited))
	if mapped.Status != http.StatusServiceUnavailable || mapped.Code != "resource_limit" {
		t.Fatalf("mapped %d %s", mapped.Status, mapped.Code)
	}
}
