package store

import (
	"testing"
	"time"
)

func TestLeasesAndGeneration(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	soon := time.Now().Add(time.Hour)
	if ok, err := s.AcquireLease(ctx, "triage", "a", soon); !ok || err != nil {
		t.Fatal("free lease", err)
	}
	if ok, _ := s.AcquireLease(ctx, "triage", "b", soon); ok {
		t.Fatal("a held lease refuses another holder")
	}
	if ok, _ := s.AcquireLease(ctx, "triage", "a", soon); !ok {
		t.Fatal("the holder renews")
	}
	_ = s.ReleaseLease(ctx, "triage", "b") // not b's: no effect
	if ok, _ := s.AcquireLease(ctx, "triage", "b", soon); ok {
		t.Fatal("only the holder releases")
	}
	_ = s.ReleaseLease(ctx, "triage", "a")
	if ok, _ := s.AcquireLease(ctx, "triage", "b", time.Now().Add(-time.Hour)); !ok {
		t.Fatal("released")
	}
	if ok, _ := s.AcquireLease(ctx, "triage", "c", soon); !ok {
		t.Fatal("an expired lease is taken over (crashed holder)")
	}
	g := s.Generation()
	seed(t, s, 1)
	if s.Generation() == g {
		t.Fatal("a message write moves the generation")
	}
}
