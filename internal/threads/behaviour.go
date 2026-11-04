package threads

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// Stats is the owner's behaviour toward one sender, one domain or overall.
// Minutes are rounded half-up to one decimal (the one rounding rule).
type Stats struct {
	Received      int      `json:"received"`
	Replied       int      `json:"replied"`
	ReplyRate     float64  `json:"reply_rate"`
	MedianMinutes *float64 `json:"median_response_minutes"`
	MeanMinutes   *float64 `json:"mean_response_minutes"`
	P90Minutes    *float64 `json:"p90_response_minutes"`
	FirstSeen     string   `json:"first_seen,omitempty"` // owner's local time, RFC 3339 with offset
	LastSeen      string   `json:"last_seen,omitempty"`
}

// Example is one past reply of the owner to a sender.
type Example struct {
	MessageID        int64   `json:"message_id"` // the inbox message that was answered
	Subject          string  `json:"subject"`
	ReceivedAt       string  `json:"received_at"`
	RespondedMinutes float64 `json:"responded_minutes"`
	ReplyPreview     string  `json:"reply_preview"`
}

// MinutePlaces is how a reply latency is rounded to minutes (half-up). The
// behaviour profile, the known history and the feedback correlations report
// the same number for the same reply, so they round in one place.
const MinutePlaces = 1

// RoundMinutes rounds a reply latency in minutes, half-up, to MinutePlaces.
func RoundMinutes(minutes float64) float64 { return mathx.Round(minutes, MinutePlaces) }

// newExample is the one reply example: the inbox message that was answered and
// the owner's reply to it, in the owner's zone. Behaviour and the known history
// describe the same reply, so a field added here reaches both.
func newExample(m *Message, r Reply, zone *time.Location) Example {
	return Example{MessageID: m.RowID, Subject: m.Subject,
		ReceivedAt: stampIn(m.At, zone), RespondedMinutes: RoundMinutes(r.Seconds / 60), ReplyPreview: r.Excerpt}
}

// stampIn renders an instant in the owner's zone. The behaviour view is the
// owner's local view, so it keeps the offset; the stored form (mail.Stamp) is
// the UTC instant and stays separate on purpose.
func stampIn(t time.Time, zone *time.Location) string { return t.In(zone).Format(time.RFC3339) }

// Bucket counts replies by response time.
type Bucket struct {
	Label      string `json:"label"`
	UpToMinute int    `json:"up_to_minute"` // 0 for the open last bucket and "no reply"
	Count      int    `json:"count"`
}

// Volume is one month of mail.
type Volume struct {
	Received int `json:"received"`
	Sent     int `json:"sent"`
	Replied  int `json:"replied"`
}

// Profile is the owner's behaviour (history.build_behavior), in Zone.
type Profile struct {
	Zone        string               `json:"zone"`
	Totals      Stats                `json:"totals"`
	Sent        int                  `json:"sent"`
	MatchedSent int                  `json:"matched_sent"`
	Buckets     []Bucket             `json:"response_buckets"`
	Months      map[string]Volume    `json:"volume_by_month"` // "2006-01" in Zone
	Senders     map[string]Stats     `json:"senders"`
	Domains     map[string]Stats     `json:"domains"`
	Examples    map[string][]Example `json:"examples"` // by sender, newest first
}

// Options shape a behaviour profile (threads.* and time.zone settings).
type Options struct {
	Zone              *time.Location
	BucketMinutes     []int // ascending upper bounds
	ExamplesPerSender int
}

// Validate checks the bucket bounds are strictly ascending.
func (o Options) Validate() error {
	if o.Zone == nil || len(o.BucketMinutes) == 0 {
		return fmt.Errorf("threads: a zone and at least one response bucket are required")
	}
	for i := 1; i < len(o.BucketMinutes); i++ {
		if o.BucketMinutes[i] <= o.BucketMinutes[i-1] {
			return fmt.Errorf("threads: threads.response_buckets must be strictly ascending, got %v", o.BucketMinutes)
		}
	}
	return nil
}

type acc struct {
	received, replied int
	latencies         []float64
	first, last       time.Time
}

func (a *acc) add(m *Message, reply *Reply) {
	a.received++
	if m.Dated() {
		if a.first.IsZero() || m.At.Before(a.first) {
			a.first = m.At
		}
		if m.At.After(a.last) {
			a.last = m.At
		}
	}
	if reply != nil {
		a.replied++
		a.latencies = append(a.latencies, reply.Seconds)
	}
}

func (a *acc) stats(zone *time.Location) Stats {
	s := Stats{Received: a.received, Replied: a.replied, ReplyRate: mathx.Ratio(a.replied, a.received)}
	minutes := func(v float64, ok bool) *float64 {
		if !ok {
			return nil
		}
		r := RoundMinutes(v / 60)
		return &r
	}
	s.MedianMinutes = minutes(mathx.Median(a.latencies))
	s.MeanMinutes = minutes(mathx.Mean(a.latencies))
	s.P90Minutes = minutes(mathx.Percentile(a.latencies, 0.9))
	if !a.first.IsZero() {
		s.FirstSeen, s.LastSeen = stampIn(a.first, zone), stampIn(a.last, zone)
	}
	return s
}

// Behaviour aggregates the owner's behaviour from an analysis.
func (a *Analysis) Behaviour(o Options) (*Profile, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	p := &Profile{Zone: o.Zone.String(), Months: map[string]Volume{}, Senders: map[string]Stats{}, Domains: map[string]Stats{},
		Examples: map[string][]Example{}}
	senders, domains, total := map[string]*acc{}, map[string]*acc{}, &acc{}
	for i := range a.Messages {
		m := &a.Messages[i]
		if a.copyOf[i] >= 0 { // a copy is its original, counted once
			continue
		}
		if m.Role == mail.RoleSent {
			p.Sent++
			p.addMonth(m, o.Zone, func(v *Volume) { v.Sent++ })
			continue
		}
		var reply *Reply
		if r, ok := a.MyReplies[m.RowID]; ok {
			reply = &r
			p.addExample(m, r, o.Zone)
		}
		total.add(m, reply)
		into(senders, m.Sender).add(m, reply)
		into(domains, m.Domain).add(m, reply)
		p.addMonth(m, o.Zone, func(v *Volume) {
			v.Received++
			if reply != nil {
				v.Replied++
			}
		})
	}
	p.finish(o, senders, domains, total)
	return p, nil
}

func (p *Profile) finish(o Options, senders, domains map[string]*acc, total *acc) {
	for k, v := range senders {
		p.Senders[k] = v.stats(o.Zone)
	}
	for k, v := range domains {
		p.Domains[k] = v.stats(o.Zone)
	}
	p.Totals = total.stats(o.Zone)
	p.Totals.FirstSeen, p.Totals.LastSeen = "", ""
	p.MatchedSent = total.replied
	p.Buckets = buckets(total.latencies, o.BucketMinutes, total.received-total.replied)
	for k, list := range p.Examples {
		slices.SortStableFunc(list, func(x, y Example) int { return strings.Compare(y.ReceivedAt, x.ReceivedAt) })
		p.Examples[k] = list[:min(len(list), o.ExamplesPerSender)]
		if len(p.Examples[k]) == 0 {
			delete(p.Examples, k)
		}
	}
}

func (p *Profile) addExample(m *Message, r Reply, zone *time.Location) {
	if m.Sender == "" {
		return
	}
	p.Examples[m.Sender] = append(p.Examples[m.Sender], newExample(m, r, zone))
}

func (p *Profile) addMonth(m *Message, zone *time.Location, add func(*Volume)) {
	if !m.Dated() {
		return
	}
	key := mail.MonthKey(m.At.In(zone))
	v := p.Months[key]
	add(&v)
	p.Months[key] = v
}

// into returns the accumulator of key; an empty key gets a throwaway one
// (mail without a sender counts only in the totals).
func into(set map[string]*acc, key string) *acc {
	if key == "" {
		return &acc{}
	}
	if set[key] == nil {
		set[key] = &acc{}
	}
	return set[key]
}
