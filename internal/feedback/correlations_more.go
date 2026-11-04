package feedback

import (
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

func rateGroup(key string, flags []bool) Group {
	yes := 0
	for _, f := range flags {
		if f {
			yes++
		}
	}
	return Group{Key: key, N: len(flags), Rate: ptr(round3(mathx.Ratio(yes, len(flags))))}
}

func questionsVsReplyRate(in inputs) (Finding, bool) {
	asked, notAsked := []bool{}, []bool{}
	for _, m := range in.inbox {
		v, ok := in.value(m.RowID, taxonomy.RoleQuestion)
		if !ok {
			continue
		}
		_, replied := in.latency[m.RowID]
		if v.Number >= in.p.Cutoff(in.roleName(taxonomy.RoleQuestion)) {
			asked = append(asked, replied)
		} else {
			notAsked = append(notAsked, replied)
		}
	}
	n := len(asked) + len(notAsked)
	return Finding{ID: "questions_vs_reply_rate", N: n, Groups: []Group{rateGroup("asks", asked), rateGroup("does_not_ask", notAsked)}},
		n >= in.s.MinSample
}

func completenessVsResponse(in inputs) (Finding, bool) {
	groups := map[string][]float64{}
	for _, m := range in.inbox {
		minutes, replied := in.latency[m.RowID]
		v, ok := in.value(m.RowID, taxonomy.RoleInfo)
		if !replied || !ok {
			continue
		}
		key := "incomplete"
		if v.Number > in.p.Cutoff(in.roleName(taxonomy.RoleInfo)) { // legacy: strictly above
			key = "complete"
		}
		groups[key] = append(groups[key], minutes)
	}
	g := medianGroups(groups, 1)
	return Finding{ID: "completeness_vs_response", N: len(groups["complete"]) + len(groups["incomplete"]), Groups: g}, len(g) == 2
}

func effortByTopic(in inputs) (Finding, bool) {
	groups := map[string][]float64{}
	for _, m := range in.inbox {
		effort, ok1 := in.value(m.RowID, taxonomy.RoleEffort)
		topic, ok2 := in.value(m.RowID, taxonomy.RoleTopic)
		if ok1 && ok2 {
			groups[topic.Choice] = append(groups[topic.Choice], effort.Number)
		}
	}
	var out []Group
	n := 0
	for k, vals := range groups {
		if len(vals) >= in.s.MinSample {
			med, _ := mathx.Median(vals)
			out = append(out, Group{Key: k, N: len(vals), Median: ptr(mathx.Round(med, 2))})
			n += len(vals)
		}
	}
	sortGroups(out)
	return Finding{ID: "effort_by_topic", N: n, Groups: out}, len(out) > 0
}

func lengthVsResponse(in inputs) (Finding, bool) {
	var words, minutes []float64
	for _, m := range in.inbox {
		if l, ok := in.latency[m.RowID]; ok {
			words, minutes = append(words, float64(m.Words)), append(minutes, l)
		}
	}
	r, ok := mathx.Pearson(words, minutes)
	if len(words) < in.s.MinSample || !ok {
		return Finding{}, false
	}
	meaningful := r > in.s.MeaningfulR || r < -in.s.MeaningfulR
	return Finding{ID: "length_vs_response", N: len(words), Strength: ptr(round3(r)), Meaningful: &meaningful}, true
}

func replyRateByRelationship(in inputs) (Finding, bool) {
	flags := map[string][]bool{}
	for _, m := range in.inbox {
		if v, ok := in.value(m.RowID, taxonomy.RoleRelationship); ok {
			_, replied := in.latency[m.RowID]
			flags[v.Choice] = append(flags[v.Choice], replied)
		}
	}
	var out []Group
	n := 0
	for k, f := range flags {
		if len(f) >= in.s.MinSample {
			out = append(out, rateGroup(k, f))
			n += len(f)
		}
	}
	sortGroups(out)
	return Finding{ID: "reply_rate_by_relationship", N: n, Groups: out}, len(out) > 0
}

// stanceVsAsk cross-tabs what the counterpart asked against the stance of
// the owner's reply (the counterpart is indexed by thread, not O(n²)).
func stanceVsAsk(in inputs) (Finding, bool) {
	cells := map[[2]string]int{}
	for i := range in.a.Messages {
		m := &in.a.Messages[i]
		if m.Role != mail.RoleSent {
			continue
		}
		other, ok := in.a.Counterpart(m.RowID)
		if !ok {
			continue
		}
		ask, ok1 := in.value(other.RowID, taxonomy.RoleAsksFor)
		stance, ok2 := in.value(m.RowID, taxonomy.RoleStance)
		if ok1 && ok2 {
			cells[[2]string{ask.Choice, stance.Choice}]++
		}
	}
	var out []Group
	for k, n := range cells {
		out = append(out, Group{Key: k[0], Other: k[1], N: n})
	}
	sortGroups(out)
	out = out[:min(len(out), in.s.MaxCells)]
	n := 0
	for _, g := range out {
		n += g.N
	}
	return Finding{ID: "reply_stance_vs_ask", N: n, Groups: out}, len(out) > 0
}

// roleName is the question of a role ("" when dropped).
func (in inputs) roleName(role taxonomy.Role) string {
	q, _ := in.p.RoleQuestion(role)
	if q == nil {
		return ""
	}
	return q.Name
}
