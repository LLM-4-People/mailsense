package feedback

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// InsightSettings are the feedback.insights.* settings.
type InsightSettings struct {
	TopSenders     int
	TopDomains     int
	FasterFactor   float64
	SlowerFactor   float64
	KnownDomainMin int
}

// Correspondent is one sender or domain of the top lists.
type Correspondent struct {
	Key string `json:"key"`
	threads.Stats
}

// Speed compares how fast the owner answers a domain with how fast it
// answers the owner. Factor = theirs / mine; Verdict faster, slower or
// similar (the owner's speed relative to theirs).
type Speed struct {
	Domain        string   `json:"domain"`
	MyMedian      *float64 `json:"my_median_minutes"`
	TheirMedian   *float64 `json:"their_median_minutes"`
	MyMessages    int      `json:"my_messages"`
	TheirMessages int      `json:"their_messages"`
	Factor        *float64 `json:"factor,omitempty"`
	Verdict       string   `json:"verdict,omitempty"`
}

// Closing is who sends the last message of threads with a domain.
type Closing struct {
	Domain    string  `json:"domain"`
	Threads   int     `json:"threads"`
	IClose    int     `json:"i_close"`
	TheyClose int     `json:"they_close"`
	Rate      float64 `json:"my_close_rate"`
}

// Suspect is inbox mail judged suspicious from a known domain (a spoofed
// or compromised exact domain; no look-alike detection).
type Suspect struct {
	MessageID int64   `json:"message_id"`
	Sender    string  `json:"sender"`
	Domain    string  `json:"domain"`
	Value     float64 `json:"suspicious"`
}

// Insights is the Insights view's data.
type Insights struct {
	TopSenders    []Correspondent `json:"top_senders"`
	TopDomains    []Correspondent `json:"top_domains"`
	Speed         []Speed         `json:"speed"`
	Closing       []Closing       `json:"closing"`
	Impersonation []Suspect       `json:"impersonation"`
	KnownDomains  []string        `json:"known_domains"`
}

// BuildInsights computes the insights from the behaviour profile, the
// analysis and the judged values.
func BuildInsights(a *threads.Analysis, b *threads.Profile, judged Judged, p *taxonomy.Profile, s InsightSettings) Insights {
	known := knownDomains(a, b, judged, p, s)
	return Insights{TopSenders: top(b.Senders, s.TopSenders), TopDomains: top(b.Domains, s.TopDomains), Speed: speeds(a, s),
		Closing: closings(a), Impersonation: impersonation(a, judged, p, known), KnownDomains: slices.Sorted(maps.Keys(known))}
}

func top(stats map[string]threads.Stats, n int) []Correspondent {
	out := make([]Correspondent, 0, len(stats))
	for k, v := range stats {
		out = append(out, Correspondent{Key: k, Stats: v})
	}
	slices.SortFunc(out, func(x, y Correspondent) int { return cmp.Or(y.Received-x.Received, strings.Compare(x.Key, y.Key)) })
	return out[:min(n, len(out))]
}

func speeds(a *threads.Analysis, s InsightSettings) []Speed {
	mine, theirs := map[string][]float64{}, map[string][]float64{}
	for row, r := range a.MyReplies {
		if m, ok := a.Message(row); ok && m.Domain != "" {
			mine[m.Domain] = append(mine[m.Domain], r.Seconds/60)
		}
	}
	for _, r := range a.TheirReplies {
		if m, ok := a.Message(r.ReplyRow); ok && m.Domain != "" { // the answer's sender's organisation
			theirs[m.Domain] = append(theirs[m.Domain], r.Seconds/60)
		}
	}
	domains := map[string]bool{}
	for d := range mine {
		domains[d] = true
	}
	for d := range theirs {
		domains[d] = true
	}
	var out []Speed
	for _, d := range slices.Sorted(maps.Keys(domains)) {
		sp := Speed{Domain: d, MyMessages: len(mine[d]), TheirMessages: len(theirs[d]), MyMedian: median1(mine[d]), TheirMedian: median1(theirs[d])}
		if sp.MyMedian != nil && sp.TheirMedian != nil && *sp.MyMedian > 0 {
			f := mathx.Round(*sp.TheirMedian / *sp.MyMedian, 2)
			sp.Factor, sp.Verdict = &f, "similar"
			if f >= s.FasterFactor {
				sp.Verdict = "faster"
			} else if f <= s.SlowerFactor {
				sp.Verdict = "slower"
			}
		}
		out = append(out, sp)
	}
	return out
}

func median1(v []float64) *float64 {
	m, ok := mathx.Median(v)
	if !ok {
		return nil
	}
	return ptr(threads.RoundMinutes(m))
}

// closings books each thread to the domain of its first inbox message.
func closings(a *threads.Analysis) []Closing {
	by := map[string]*Closing{}
	for _, t := range a.Threads {
		var domain string
		for _, i := range t.Members {
			if m := &a.Messages[i]; m.Role == mail.RoleInbox {
				domain = m.Domain
				break
			}
		}
		if domain == "" {
			continue
		}
		c := by[domain]
		if c == nil {
			c = &Closing{Domain: domain}
			by[domain] = c
		}
		c.Threads++
		if t.LastIsMine {
			c.IClose++
		} else {
			c.TheyClose++
		}
	}
	var out []Closing
	for _, c := range by {
		c.Rate = round3(mathx.Ratio(c.IClose, c.Threads))
		out = append(out, *c)
	}
	slices.SortFunc(out, func(x, y Closing) int { return cmp.Or(y.Threads-x.Threads, strings.Compare(x.Domain, y.Domain)) })
	return out
}

func knownDomains(a *threads.Analysis, b *threads.Profile, judged Judged, p *taxonomy.Profile, s InsightSettings) map[string]bool {
	known := map[string]bool{}
	for d, st := range b.Domains {
		if st.Received >= s.KnownDomainMin {
			known[d] = true
		}
	}
	rel, ok := p.RoleQuestion(taxonomy.RoleRelationship)
	for i := range a.Messages {
		m := &a.Messages[i]
		if !ok || m.Role != mail.RoleInbox {
			continue
		}
		if v, has := judged[m.RowID][rel.Name]; has && slices.Contains(p.KnownRelationships, v.Choice) && m.Domain != "" {
			known[m.Domain] = true
		}
	}
	return known
}

// impersonation reports inbox mail whose answered suspicious value reaches
// the question's cutoff from a known domain. Only an answered value is a
// suspicion input: a refusal or abstention is a deliberate non-answer and
// never a suspect, whatever number it carries.
func impersonation(a *threads.Analysis, judged Judged, p *taxonomy.Profile, known map[string]bool) []Suspect {
	q, ok := p.RoleQuestion(taxonomy.RoleSuspicious)
	if !ok {
		return nil
	}
	var out []Suspect
	for i := range a.Messages {
		m := &a.Messages[i]
		v, has := judged[m.RowID][q.Name]
		if d := m.Domain; m.Role == mail.RoleInbox && has && v.Answered() && v.Number >= p.Cutoff(q.Name) && known[d] {
			out = append(out, Suspect{MessageID: m.RowID, Sender: m.Sender, Domain: d, Value: round3(v.Number)})
		}
	}
	return out
}
