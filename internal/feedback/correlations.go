package feedback

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Group is one row of a finding.
type Group struct {
	Key           string   `json:"key"`
	Other         string   `json:"other,omitempty"` // second key of a cross-tab
	N             int      `json:"n"`
	MedianMinutes *float64 `json:"median_minutes,omitempty"` // owner's response time
	Rate          *float64 `json:"rate,omitempty"`           // share replied
	Median        *float64 `json:"median,omitempty"`         // median of a model score
}

// Finding is one correlation (correlations.py), as data.
type Finding struct {
	ID         string   `json:"id"`
	N          int      `json:"n"`
	Groups     []Group  `json:"groups,omitempty"`
	Strength   *float64 `json:"strength,omitempty"` // Pearson r
	Meaningful *bool    `json:"meaningful,omitempty"`
}

// CorrelationSettings are the feedback.* settings correlations use.
type CorrelationSettings struct {
	MinSample   int
	MaxCells    int
	MeaningfulR float64
}

// inputs bundles what every correlation reads.
type inputs struct {
	a       *threads.Analysis
	judged  Judged
	p       *taxonomy.Profile
	s       CorrelationSettings
	inbox   []*threads.Message
	latency map[int64]float64 // inbox row → owner's response minutes
}

// Correlations computes every finding that has data. Values are the final
// (blended) ones: what the owner sees.
func Correlations(a *threads.Analysis, judged Judged, p *taxonomy.Profile, s CorrelationSettings) []Finding {
	in := inputs{a: a, judged: judged, p: p, s: s, latency: map[int64]float64{}}
	for i := range a.Messages {
		if m := &a.Messages[i]; m.Role == mail.RoleInbox {
			in.inbox = append(in.inbox, m)
		}
	}
	for row, r := range a.MyReplies {
		in.latency[row] = r.Seconds / 60
	}
	var out []Finding
	for _, f := range []func(inputs) (Finding, bool){latencyBy(taxonomy.RoleAsksFor, "asks_for_vs_response", choiceKey),
		latencyBy(taxonomy.RoleSentiment, "sentiment_vs_response", in.tone), latencyBy(taxonomy.RoleDeadline, "deadline_vs_response", levelKey),
		questionsVsReplyRate, completenessVsResponse, effortByTopic, lengthVsResponse,
		latencyBy(taxonomy.RoleRequests, "requests_vs_response", levelKey), replyRateByRelationship, stanceVsAsk} {
		if finding, ok := f(in); ok {
			out = append(out, finding)
		}
	}
	return out
}

// value returns the judged value of a role for a message. Only an answered
// value is a correlation input: a refusal or abstention is a deliberate
// non-answer, not data for a finding.
func (in inputs) value(row int64, role taxonomy.Role) (taxonomy.Value, bool) {
	q, ok := in.p.RoleQuestion(role)
	if !ok {
		return taxonomy.Value{}, false
	}
	v, ok := in.judged[row][q.Name]
	return v, ok && v.Answered()
}

func choiceKey(v taxonomy.Value) string { return v.Choice }

// levelKey groups a score by its level, rounded half-up (legacy floored).
func levelKey(v taxonomy.Value) string { return "level " + strconv.Itoa(mathx.RoundInt(v.Number)) }

func (in inputs) tone(v taxonomy.Value) string {
	switch {
	case v.Number < in.p.Threshold(taxonomy.ThresholdSentimentNegative):
		return "negative"
	case v.Number > in.p.Threshold(taxonomy.ThresholdSentimentPositive):
		return "positive"
	}
	return "neutral"
}

// latencyBy groups the owner's response minutes by a role's value.
func latencyBy(role taxonomy.Role, id string, key func(taxonomy.Value) string) func(inputs) (Finding, bool) {
	return func(in inputs) (Finding, bool) {
		groups := map[string][]float64{}
		n := 0
		for _, m := range in.inbox {
			minutes, replied := in.latency[m.RowID]
			v, ok := in.value(m.RowID, role)
			if replied && ok {
				groups[key(v)] = append(groups[key(v)], minutes)
				n++
			}
		}
		return Finding{ID: id, N: n, Groups: medianGroups(groups, 1)}, n > 0
	}
}

// medianGroups turns key → values into groups with at least floor members,
// largest first.
func medianGroups(groups map[string][]float64, floor int) []Group {
	var out []Group
	for k, vals := range groups {
		if len(vals) >= floor {
			m, _ := mathx.Median(vals)
			out = append(out, Group{Key: k, N: len(vals), MedianMinutes: ptr(threads.RoundMinutes(m))})
		}
	}
	sortGroups(out)
	return out
}

func sortGroups(g []Group) {
	slices.SortStableFunc(g, func(a, b Group) int {
		if a.N != b.N {
			return b.N - a.N
		}
		return cmp.Or(strings.Compare(a.Key, b.Key), strings.Compare(a.Other, b.Other))
	})
}

func ptr(v float64) *float64 { return &v }
