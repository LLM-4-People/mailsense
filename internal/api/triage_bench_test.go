package api

// Wire-size benchmark for the triage page (Round 21). The page is synthetic:
// 50 decided messages under the built-in 17-question profile, every choice
// and score answer carrying its full distribution, the reply prior blended
// exactly as internal/triage/priors.go blends it. No real mail.
//
// The timed loop runs the two steps writeJSON uses for a large response:
// marshal(page) then httpgzip.Compress. The response writer, middleware and
// headers are excluded; setup serves the same page through the real
// handleTriage/writeJSON path and fails unless both identity and gzip bodies
// are byte-for-byte equal to the timed steps.

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpgzip"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// benchTriagePage builds the synthetic page: the real default profile, 50
// full-distribution rows.
func benchTriagePage(b *testing.B) *triage.Page {
	b.Helper()
	profile, err := taxonomy.Default()
	if err != nil {
		b.Fatal(err)
	}
	page := &triage.Page{
		Key:     store.TriageKey{Fingerprint: profile.Fingerprint, Decider: "layer1"},
		Profile: profile,
		Results: make(map[int64]triage.PageRow, triage.TriagePageDefault),
		Routed:  true,
		Total:   triage.TriagePageDefault,
		Limit:   triage.TriagePageDefault,
	}
	for i := range triage.TriagePageDefault {
		id := int64(i + 1)
		values := make(map[string]taxonomy.Value, len(profile.Questions))
		for qi := range profile.Questions {
			q := &profile.Questions[qi]
			a := engine.Answer{Type: q.Type}
			switch q.Type {
			case engine.Choice:
				a.Probabilities = benchDistribution(benchOptionKeys(q), i, qi)
				a.Choice = benchArgmax(benchOptionKeys(q), a.Probabilities)
				confidence := a.Probabilities[a.Choice]
				a.Confidence = &confidence
			case engine.Score:
				keys := benchLevelKeys(q)
				a.Probabilities = benchDistribution(keys, i, qi)
				var expected, confidence float64
				for level, key := range keys {
					probability := a.Probabilities[key]
					expected += probability * float64(level)
					confidence = max(confidence, probability)
				}
				a.Value = expected
				a.Confidence = &confidence
			case engine.Noul:
				// A calibrated yes/no probability, no distribution unless it
				// abstains (engine.Answer).
				a.Value = 0.05 + float64((i*11+qi*7)%90)/100
			}
			v := taxonomy.FromAnswer(a)
			// The built-in profile blends only the reply role (0.3 model);
			// a dated message with at least one prior sample carries it.
			if role := profile.RoleOf(q.Name); role == taxonomy.RoleReply && i%5 != 0 {
				prior := 0.1 + float64((i*13+qi*3)%70)/100
				weight := profile.PriorBlend[role]
				v.Number = min(1, max(0, weight*a.Value+(1-weight)*prior))
				v.Prior = &prior
			}
			values[q.Name] = v
		}
		row := triage.PageRow{MessageID: id, Answered: len(values), FirstQuestion: "category", FirstValue: values["category"],
			DecidedAt: "2026-10-" + strconv.Itoa(1+i%28) + "T09:00:00Z", Stale: i%17 == 0}
		if i%9 == 0 {
			row.Escalate = true
		}
		page.Results[id] = row
	}
	return page
}

// benchDistribution makes a deterministic, long-fraction distribution over
// keys (a calibrated model's output, not round numbers).
func benchDistribution(keys []string, msg, question int) map[string]float64 {
	out := make(map[string]float64, len(keys))
	total := 0.0
	for i, key := range keys {
		weight := 1 + float64((msg*37+question*53+i*29)%97)/100
		out[key] = weight
		total += weight
	}
	for key := range out {
		out[key] /= total
	}
	return out
}

func benchArgmax(keys []string, probabilities map[string]float64) string {
	best := keys[0]
	for _, key := range keys[1:] {
		if probabilities[key] > probabilities[best] {
			best = key
		}
	}
	return best
}

func benchOptionKeys(q *engine.Question) []string {
	out := make([]string, len(q.Options))
	for i := range q.Options {
		out[i] = q.Options[i].Name
	}
	return out
}

func benchLevelKeys(q *engine.Question) []string {
	out := make([]string, len(q.Levels))
	for i := range q.Levels {
		out[i] = engine.LevelKey(i)
	}
	return out
}

// benchTriageWire serves one page through the real handler and returns the
// body ("" = identity, "gzip" = compressed).
func benchTriageWire(b *testing.B, srv *Server, encoding string) []byte {
	b.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/triage?limit="+strconv.Itoa(triage.TriagePageDefault), nil)
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	rec := httptest.NewRecorder()
	srv.handleTriage(rec, req)
	if rec.Code != http.StatusOK {
		b.Fatalf("handler status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != encoding {
		b.Fatalf("handler encoding %q, want %q", got, encoding)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		b.Fatalf("handler content length %q, body %d", got, rec.Body.Len())
	}
	return rec.Body.Bytes()
}

func BenchmarkTriagePageWire(b *testing.B) {
	page := benchTriagePage(b)
	srv := &Server{log: slog.Default(), services: Services{Triage: &fakeTriage{page: page}}}

	identityWire := benchTriageWire(b, srv, "")
	gzipWire := benchTriageWire(b, srv, "gzip")

	body, err := marshal(page)
	if err != nil {
		b.Fatal(err)
	}
	compressed, err := httpgzip.Compress(body)
	if err != nil {
		b.Fatal(err)
	}
	if !bytes.Equal(identityWire, body) {
		b.Fatalf("handler identity body differs from marshal (%d vs %d bytes)", len(identityWire), len(body))
	}
	if !bytes.Equal(gzipWire, compressed) {
		b.Fatalf("handler gzip body differs from httpgzip.Compress (%d vs %d bytes)", len(gzipWire), len(compressed))
	}

	b.ReportAllocs()
	b.ResetTimer()
	var jsonBytes, gzipBytes int
	for b.Loop() {
		body, err := marshal(page)
		if err != nil {
			b.Fatal(err)
		}
		compressed, err := httpgzip.Compress(body)
		if err != nil {
			b.Fatal(err)
		}
		jsonBytes, gzipBytes = len(body), len(compressed)
	}
	b.ReportMetric(float64(jsonBytes), "json_bytes/op")
	b.ReportMetric(float64(gzipBytes), "gzip_bytes/op")
}
