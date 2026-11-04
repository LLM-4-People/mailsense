package logx

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A provider key is registered by the secret store when it is loaded. A
// diagnostic that quotes it must not survive RedactedError, and the rest of
// the message must stay readable.
func TestRedactedErrorMasksRegisteredKey(t *testing.T) {
	const key = "Zq7Rt2Vx9Kp4Nm6Bc1Df8Gh3Js5Lw0Ay"
	RegisterSecret(key)
	err := fmt.Errorf("engine: provider dec answered: %s", key)
	out := RedactedError(err)
	if strings.Contains(out, key) {
		t.Fatalf("RedactedError leaked the key: %q", out)
	}
	if !strings.Contains(out, "engine: provider dec answered:") {
		t.Fatalf("RedactedError dropped the diagnostic: %q", out)
	}
	if got := RedactedError(nil); got != "" {
		t.Fatalf("RedactedError(nil) = %q", got)
	}
}

// The sink redacts an error value even when a caller passes it raw; the
// projection is defense in depth, never the only barrier.
func TestSinkRedactsErrorCarryingKey(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	const key = "Yb6Uv1Ws8Jq3Mz5Nc0Dg7Fh2Kt4Lx9Ap"
	RegisterSecret(key)
	For("test").Error("provider call failed", "err", errors.New("answer: "+key))
	e := last(t, svc)
	s, _ := e.Attrs["err"].(string)
	if strings.Contains(s, key) {
		t.Fatalf("sink leaked the key: %q", s)
	}
}
