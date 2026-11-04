package feedback

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

func profile(t *testing.T) *taxonomy.Profile {
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func label(id int64, q string, typ engine.QuestionType, v taxonomy.Label, sender, subject, at string) Label {
	return Label{MessageID: id, Question: q, Type: typ, Value: v, Source: store.LabelManual,
		Sender: sender, Subject: subject, CreatedAt: at}
}

var day = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// at builds a label made at day+n hours about a message of day+n−1 hours.
func at(l Label, n int) Label {
	l.Created, l.At = day.Add(time.Duration(n)*time.Hour), day.Add(time.Duration(n-1)*time.Hour)
	l.CreatedAt = l.Created.Format(time.RFC3339)
	return l
}

// Review 2a #1 (repro TestReviewOneLabelInvalidatesArchive; failed first:
// one label changed 39 of 40 states through domain and subject overlap):
// only labels of the same sender or domain, made before the message, on
// other messages, may inform it.
func TestSelectExamplesIsCausalAndLocal(t *testing.T) {
	ls := []Label{
		at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "a@x.com", "Budget 2026", ""), 1),
		at(label(2, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "b@x.com", "Other", ""), 2),
		at(label(3, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "z@y.com", "budget 2026 plan", ""), 3), // other domain
		at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "action"}, "a@x.com", "Budget 2026", ""), 4),    // duplicate (id, question)
		at(label(9, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "a@x.com", "Budget 2026", ""), 5),      // the message itself
		at(label(7, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "a@x.com", "Budget 2026", ""), 50),     // made after the message
	}
	got := SelectExamples(ls, 9, " A@X.com ", "Budget 2026", day.Add(10*time.Hour), 10, mail.NewDomains(nil))
	if len(got) != 2 || got[0].Value.Choice != "action" || got[1].MessageID != 2 {
		t.Fatalf("same sender first (newest label of a message), then same domain; never another domain, itself or the future: %+v", got)
	}
	if SelectExamples(ls, 9, "a@x.com", "", day.Add(10*time.Hour), 0, mail.NewDomains(nil)) != nil || SelectExamples(ls, 9, "a@x.com", "", time.Time{}, 5, mail.NewDomains(nil)) != nil {
		t.Fatal("limit 0 and an undated message select nothing")
	}
}

func TestSuspiciousPriorIsCausal(t *testing.T) {
	p := profile(t)
	ls := []Label{
		at(label(1, "suspicious", engine.Noul, taxonomy.Label{Number: 1}, "a@evil.com", "", ""), 1),
		at(label(2, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "b@evil.com", "", ""), 2),
		at(label(3, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "c@good.com", "", ""), 3), // not a suspicious label
		at(label(4, "suspicious", engine.Noul, taxonomy.Label{Number: 0}, "d@good.com", "", ""), 4),
	}
	later := day.Add(10 * time.Hour)
	if pr, ok := SuspiciousPrior(ls, p, "A@evil.com", 99, later, 1, 2, mail.NewDomains(nil)); !ok || pr.Rate != 1 || pr.Samples != 1 {
		t.Fatalf("address prior %+v", pr)
	}
	if pr, ok := SuspiciousPrior(ls, p, "x@evil.com", 99, later, 1, 2, mail.NewDomains(nil)); !ok || pr.Samples != 2 {
		t.Fatalf("domain prior needs 2 labels: %+v", pr)
	}
	if _, ok := SuspiciousPrior(ls, p, "x@good.com", 99, later, 1, 2, mail.NewDomains(nil)); ok {
		t.Fatal("one labelled good.com message is not enough for the domain")
	}
	if pr, ok := SuspiciousPrior(ls, p, "a@evil.com", 1, later, 1, 1, mail.NewDomains(nil)); !ok || pr.Samples != 1 {
		t.Fatalf("its own label is excluded, the domain's other label still counts: %+v", pr)
	}
	if pr, _ := SuspiciousPrior(ls, p, "a@evil.com", 1, later, 1, 3, mail.NewDomains(nil)); pr.Samples != 0 {
		t.Fatal("excluding itself leaves too few")
	}
	if _, ok := SuspiciousPrior(ls, p, "a@evil.com", 99, day, 1, 2, mail.NewDomains(nil)); ok {
		t.Fatal("labels made after the message never inform it")
	}
}

// Regression (legacy leak, PORTING §8): the weak reply evaluation scored
// the blended value, which is 70% a prior learned from the same replies.
func TestWeakReplyEvaluationUsesTheModelValue(t *testing.T) {
	p := profile(t)
	prior := 0.9
	judged := Judged{}
	weak := map[int64]bool{}
	for i := int64(1); i <= 4; i++ {
		// The owner replied to all four; the model said "no reply needed" (0.1)
		// but the blend with a reply prior of 0.9 pushed the final value to 0.66.
		judged[i] = map[string]taxonomy.Value{"reply_needed": {Type: engine.Noul, Model: 0.1, Number: 0.66, Prior: &prior}}
		weak[i] = true
	}
	ev := Evaluate(judged, nil, weak, p)
	if ev.WeakReply == nil || ev.WeakReply.Metrics["recall"] != 0 || ev.WeakReply.Metrics["accuracy"] != 0 {
		t.Fatalf("the model was wrong 4/4; the blended value would have scored 4/4: %+v", ev.WeakReply)
	}
}

// The weak reply evaluation also ranks (AUC) and calibrates (Brier) the
// model's probability; AUC is omitted while one class is absent.
func TestWeakReplyAUCAndBrier(t *testing.T) {
	p := profile(t)
	judged, weak := Judged{}, map[int64]bool{}
	for i, c := range []struct {
		model   float64
		replied bool
	}{{0.9, true}, {0.6, true}, {0.7, false}, {0.2, false}} {
		judged[int64(i)] = map[string]taxonomy.Value{"reply_needed": {Type: engine.Noul, Model: c.model}}
		weak[int64(i)] = c.replied
	}
	m := Evaluate(judged, nil, weak, p).WeakReply.Metrics
	// pairs: 0.9 beats both, 0.6 beats 0.2 only → 3/4; Brier (0.01+0.16+0.49+0.04)/4 = 0.175
	if m["auc"] != 0.75 || m["brier"] != 0.175 || m["accuracy"] != 0.75 {
		t.Fatalf("metrics %v", m)
	}
	delete(weak, 2)
	delete(weak, 3)
	if m := Evaluate(judged, nil, weak, p).WeakReply.Metrics; m["brier"] != 0.085 {
		t.Fatalf("one class: brier only, %v", m)
	} else if _, has := m["auc"]; has {
		t.Fatalf("AUC is undefined with one class: %v", m)
	}
}

func TestEvaluateAgainstLabels(t *testing.T) {
	p := profile(t)
	judged := Judged{
		1: {"category": {Type: engine.Choice, Choice: "work"}, "reply_needed": {Type: engine.Noul, Number: 0.8}, "urgency": {Type: engine.Score, Number: 1.2}},
		2: {"category": {Type: engine.Choice, Choice: "spam"}, "reply_needed": {Type: engine.Noul, Number: 0.2}, "urgency": {Type: engine.Score, Number: 3}},
	}
	ls := []Label{
		label(1, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "", "", ""),
		label(2, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "", "", ""),
		label(1, "reply_needed", engine.Noul, taxonomy.Label{Number: 1}, "", "", ""),
		label(2, "reply_needed", engine.Noul, taxonomy.Label{Number: 1}, "", "", ""),
		label(1, "urgency", engine.Score, taxonomy.Label{Number: 1}, "", "", ""),
		label(2, "urgency", engine.Score, taxonomy.Label{Number: 4}, "", "", ""),
		label(7, "urgency", engine.Score, taxonomy.Label{Number: 4}, "", "", ""), // not judged: ignored
	}
	ev := Evaluate(judged, ls, nil, p)
	if len(ev.Manual) != 3 || ev.WeakReply != nil {
		t.Fatalf("%+v", ev)
	}
	cat, reply, urg := ev.Manual[0], ev.Manual[1], ev.Manual[2]
	if cat.Metrics["accuracy"] != 0.5 || cat.Metrics["recall[work]"] != 0.5 || cat.Confusion.Columns[0] != "spam" {
		t.Fatalf("choice %+v", cat)
	}
	if reply.Metrics["recall"] != 0.5 || reply.Metrics["precision"] != 1 || reply.Metrics["f1"] != 0.667 || reply.Confusion.Rows[1].Counts[0] != 1 {
		t.Fatalf("noul %+v", reply)
	}
	if urg.Metrics["mean_absolute_error"] != 0.6 || urg.Metrics["within_half_level"] != 0.5 || urg.Count != 2 {
		t.Fatalf("score %+v", urg)
	}
}

func TestCalibrateSweepsThresholds(t *testing.T) {
	p := profile(t)
	judged := Judged{}
	var ls []Label
	for i, c := range []struct {
		v   float64
		yes bool
	}{{0.9, true}, {0.7, true}, {0.6, false}, {0.3, false}} {
		id := int64(i + 1)
		judged[id] = map[string]taxonomy.Value{"reply_needed": {Type: engine.Noul, Number: c.v}}
		ls = append(ls, label(id, "reply_needed", engine.Noul, taxonomy.Label{Number: map[bool]float64{true: 1}[c.yes]}, "", "", ""))
	}
	s := Calibrate(judged, ls, p, 0.05, 3)
	if len(s) != 1 || s[0].Suggested != 0.65 || s[0].F1 != 1 || s[0].Current != 0.5 || s[0].Labels != 4 {
		t.Fatalf("lowest threshold separating 0.6 from 0.7: %+v", s)
	}
	if len(Calibrate(judged, ls, p, 0.05, 5)) != 0 {
		t.Fatal("needs min labels")
	}
}

func analysis(t *testing.T) *threads.Analysis {
	norm, _ := mail.NewSubjectNormalizer(1, []string{"re"})
	t0 := time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)
	var msgs []threads.Message
	for i := range 6 {
		conv := string(rune('A' + i))
		msgs = append(msgs, threads.Message{RowID: int64(10*i + 1), Store: "s", Role: mail.RoleInbox, Sender: "a@client.com", Domain: "client.com", ConversationID: conv,
			At: t0.Add(time.Duration(i) * time.Hour), Words: 10 * (i + 1)})
		if i < 4 {
			msgs = append(msgs, threads.Message{RowID: int64(10*i + 2), Store: "s", Role: mail.RoleSent, Sender: "me@x.com", Domain: "x.com", ConversationID: conv,
				Recipients: []string{"a@client.com"}, At: t0.Add(time.Duration(i)*time.Hour + time.Duration(10*(i+1))*time.Minute)})
		}
	}
	return threads.Analyze(msgs, norm)
}

func TestCorrelationsAreData(t *testing.T) {
	p := profile(t)
	a := analysis(t)
	judged := Judged{}
	for i := range a.Messages {
		m := &a.Messages[i]
		v := map[string]taxonomy.Value{"asks_for": {Type: engine.Choice, Choice: "answer_question"}, "is_question": {Type: engine.Noul, Number: 0.9},
			"relationship": {Type: engine.Choice, Choice: "existing_client"}, "deadline": {Type: engine.Score, Number: 2.5},
			"stance": {Type: engine.Choice, Choice: "provides_answers"}, "suspicious": {Type: engine.Noul, Number: 0.7}}
		judged[m.RowID] = v
	}
	set := SettingsFrom(configtest.New(t, map[string]any{"feedback.insights.top_senders": 1}))
	fs := Correlations(a, judged, p, set.Correlations)
	byID := map[string]Finding{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	if f := byID["asks_for_vs_response"]; f.N != 4 || *f.Groups[0].MedianMinutes != 25 {
		t.Fatalf("asks_for %+v", f)
	}
	if f := byID["deadline_vs_response"]; f.Groups[0].Key != "level 3" {
		t.Fatalf("2.5 rounds half-up to level 3 (legacy floored to 2): %+v", f)
	}
	if f := byID["questions_vs_reply_rate"]; f.N != 6 || *f.Groups[0].Rate != 0.667 {
		t.Fatalf("questions %+v", f)
	}
	if f := byID["length_vs_response"]; f.Strength != nil {
		t.Fatalf("4 pairs < min sample 5: %+v", f)
	}
	if f := byID["reply_rate_by_relationship"]; f.Groups[0].N != 6 {
		t.Fatalf("relationship %+v", f)
	}
	if f := byID["reply_stance_vs_ask"]; f.N != 4 || f.Groups[0].Other != "provides_answers" {
		t.Fatalf("stance cross-tab %+v", f)
	}
	b, _ := a.Behaviour(threads.Options{Zone: time.UTC, BucketMinutes: []int{60}, ExamplesPerSender: 3})
	in := BuildInsights(a, b, judged, p, set.Insights)
	if len(in.TopSenders) != 1 || in.TopSenders[0].Key != "a@client.com" || len(in.Impersonation) != 6 || in.Closing[0].Threads != 6 ||
		in.Closing[0].IClose != 4 || len(in.Speed) != 1 || in.Speed[0].MyMessages != 4 {
		t.Fatalf("insights %+v", in)
	}
	vol := Patterns(a, b, time.UTC, "")
	if vol.Weekday["Monday"] != 10 || vol.Hour[14] != 1 || b.Months["2026-03"].Received != 6 || vol.StdDev != nil {
		t.Fatalf("volume %+v", vol)
	}
	// #12: no hemisphere, no seasons; March is spring in the north,
	// autumn in the south.
	if vol.Season != nil {
		t.Fatal("seasons are never assumed")
	}
	if n, s := Patterns(a, b, time.UTC, HemisphereNorth), Patterns(a, b, time.UTC, HemisphereSouth); n.Season["spring"] != 10 || s.Season["autumn"] != 10 {
		t.Fatalf("north %v south %v", n.Season, s.Season)
	}
}

func TestCalibrateEscalation(t *testing.T) {
	p := profile(t)
	judged := Judged{}
	var ls []Label
	for i, c := range []struct {
		conf  float64
		right bool
	}{{0.9, true}, {0.8, true}, {0.7, true}, {0.35, false}, {0.3, false}, {0.2, true}} {
		id := int64(i + 1)
		conf := c.conf
		judged[id] = map[string]taxonomy.Value{"category": {Type: engine.Choice, Choice: "work", Confidence: &conf}}
		want := "spam"
		if c.right {
			want = "work"
		}
		ls = append(ls, label(id, "category", engine.Choice, taxonomy.Label{Choice: want}, "", "", ""))
	}
	got := CalibrateEscalation(judged, ls, p, 0.8, 0.05, 3)
	if len(got) != 1 || got[0].Role != taxonomy.RoleCategory || got[0].Suggested != 0.4 || got[0].AccuracyKept != 1 ||
		got[0].FlaggedShare != 0.5 || got[0].Current != 0.4 {
		t.Fatalf("lowest threshold keeping ≥ 80%% right answers: %+v", got)
	}
	if len(CalibrateEscalation(judged, ls, p, 0.8, 0.05, 7)) != 0 {
		t.Fatal("needs min labels")
	}
}
