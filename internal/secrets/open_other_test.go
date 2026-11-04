//go:build !windows

package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

func TestEnvStoreIsReadOnlyAndRedacted(t *testing.T) {
	cfg := configtest.New(t, nil)
	svc, err := logx.Setup(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	s, err := Open(cfg, []string{"MAILSENSE_SECRET_API_KEY=very-secret-value", "MAILSENSE_SECRET_lower=x", "MAILSENSE_SECRET_EMPTY=", "OTHER=1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind() != "env" || !s.ReadOnly() {
		t.Fatal("kind")
	}
	if names, _ := s.List(); len(names) != 1 || names[0] != "API_KEY" {
		t.Fatalf("list: %v", names)
	}
	if v, _ := s.Get("API_KEY"); v != "very-secret-value" {
		t.Fatalf("get: %q", v)
	}
	if err := s.Set("API_KEY", "x"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("set: %v", err)
	}
	if err := s.Delete("API_KEY"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Set("bad", "x"); errors.Is(err, ErrReadOnly) {
		t.Fatal("invalid names are rejected before read-only")
	}
	logx.For("t").Info("leak very-secret-value")
	all := svc.Ring().Snapshot()
	if strings.Contains(all[len(all)-1].Message, "very-secret-value") {
		t.Fatal("loaded secret not registered for redaction")
	}
}
