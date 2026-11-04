package logx

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Review 1a bug 3: default patterns missed common token shapes.
func TestDefaultRedactionPatterns(t *testing.T) {
	snap := defaultValues()
	r, err := newRedactor(snap.Strings("log.redact.patterns"), "[R]", 4)
	if err != nil {
		t.Fatal(err)
	}
	for in, leak := range map[string]string{
		`{"access_token":"eyJhbGciOiJSUzI1NiJ9.payload.sig","token_type":"Bearer"}`: "eyJhbGci",
		`{"refresh_token":"0.AAAAxyzxyzxyz"}`:                                       "AAAAxyz",
		`client_secret=abcDEF123456789`:                                             "abcDEF",
		`Authorization: Basic dXNlcjpwYXNzd29yZA==`:                                 "dXNlcj",
		`"password": "correct horse battery"`:                                       "horse",
		`x-goog-api-key: AIzaSyA1234567890abcdef`:                                   "AIzaSy",
		`api_key=sk_live_abcdefghijklmnop`:                                          "sk_live",
		`Bearer abcdefghijklmnop`:                                                   "abcdefgh",
	} {
		if out := r.redact(in); strings.Contains(out, leak) {
			t.Errorf("%s leaked %q: %s", in, leak, out)
		}
	}
	for _, keep := range []string{`"input_tokens": 1234`, `max_tokens=4096`, `token count is low`} {
		if out := r.redact(keep); out != keep {
			t.Errorf("over-redaction of %q: %q", keep, out)
		}
	}
}

// Review 1a bug 7: RegisterSecret mutated the slice readers were iterating.
func TestConcurrentRegisterSecretAndRedact(t *testing.T) {
	r, _ := newRedactor(nil, "[R]", 4)
	RegisterSecret("SECRET-BBBBBBBBBBBBBBB")
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 300 {
			RegisterSecret(fmt.Sprintf("NEWSECRET-%040d", i))
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 300 {
				if out := r.redact("x SECRET-BBBBBBBBBBBBBBB y"); strings.Contains(out, "BBBB") {
					t.Error("registered secret leaked during concurrent registration")
					return
				}
			}
		})
	}
	wg.Wait()
}

// Review 1a rules bug 5: Resize lost entries written between its snapshot and its lock.
func TestRingResizeUnderWritesLosesNothing(t *testing.T) {
	for round := range 5 {
		r := NewRing(4000)
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := range 2000 {
				r.add(Entry{Seq: uint64(i + 1)})
			}
		})
		wg.Go(func() {
			for i := range 200 {
				r.Resize(3000 + i%2)
			}
		})
		wg.Wait()
		if n := len(r.Snapshot()); n != 2000 {
			t.Fatalf("round %d: ring holds %d of 2000 entries", round, n)
		}
	}
}
