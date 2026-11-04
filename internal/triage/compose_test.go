package triage

import (
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

func testComposer(t *testing.T, history *threads.History, labels []feedback.Label) *Composer {
	t.Helper()
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	r, err := loadRules(rulesJSON)
	if err != nil {
		t.Fatal(err)
	}
	norm, _ := mail.NewSubjectNormalizer(1, []string{"re", "fw", "fwd", "aw", "tr", "sv", "vs", "ref", "enc", "rif", "vb"})
	sc := newScales([]int{60, 240, 1440, 4320}, r.Urgency.BoundsMinutes, r.Urgency.ResolutionMinutes)
	if history == nil {
		history = threads.Analyze(nil, norm).KnownHistory(sc.fine, 3, time.UTC, mail.NewDomains(nil))
	}
	return &Composer{profile: p, rules: r, history: history, labels: labels, norm: norm, scales: sc}
}

func trickyMessage() *mail.Message {
	body := "  Bonjour Équipe,\u00a0URGENT : le SERVEUR ÉTÉ est down?! Voir www.x.fr et HTTPS://y.z/a\x1c(ok)\n\nABC×DEF déjà ÀÉÎ_ OK. Fin…  "
	return &mail.Message{FolderRole: mail.RoleInbox, Subject: "TR: Réunion?", SenderAddress: "a@b.c", ReceivedAt: "2026-03-02T14:00:00Z",
		Body: body, HasAttachments: true, Recipients: []mail.Recipient{{Kind: mail.To, Address: "x@y.z"}, {Kind: mail.To, Address: "me@x.com"},
			{Kind: mail.Cc, Address: "c@d.e"}, {Kind: mail.Bcc, Address: "hidden@d.e"}}}
}

// A judge answer is the model's own production answer: it must never be
// rendered into the example block of the prompt. Before the source policy
// the composer selected it exactly like an owner label.
func TestComposerExamplesSkipJudgeAnswers(t *testing.T) {
	created := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	l := func(id int64, source, choice string) feedback.Label {
		return feedback.Label{MessageID: id, Question: "category", Type: engine.Choice,
			Value: taxonomy.Label{Choice: choice}, Source: source, Sender: "a@x.com", Subject: "Budget 2026",
			Created: created, At: created.Add(-time.Hour), CreatedAt: created.Format(time.RFC3339)}
	}
	c := testComposer(t, nil, feedback.Teaching([]feedback.Label{
		l(1, store.LabelManual, "work"),
		l(2, store.LabelJudge, "action"),
		l(3, store.LabelJudgeAnswer, "spam"),
	}))
	m := &mail.Message{FolderRole: mail.RoleInbox, SenderAddress: "a@x.com", Subject: "Budget 2026",
		ReceivedAt: created.Add(time.Hour).Format(time.RFC3339)}
	out := c.examples(9, m, created.Add(time.Hour))
	for _, want := range []string{"work", "action"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the %q example is missing:\n%s", want, out)
		}
	}
	if strings.Contains(out, "spam") {
		t.Fatalf("a judge production answer reached the prompt:\n%s", out)
	}
}

// The facts-only state is pinned: any change of the composer changes
// every state (and re-decides every message), so it must be deliberate.
// L0 (#6) left the legacy hash c876f6a1ffbc12fe on purpose: facts are
// measured on the cleaned body with Unicode classes (all-caps words of
// every cased script: ABC×DEF is two words, the legacy class counted it
// as one and missed non-Latin-1 capitals).
func TestStateIsPinned(t *testing.T) {
	c := testComposer(t, nil, nil)
	state := c.State(1, trickyMessage())
	if h := StateHash(state); h != "22af6e01a787c139" {
		t.Fatalf("hash %s, pinned 22af6e01a787c139:\n%q", h, state)
	}
	f := measure(trickyMessage(), mail.CleanBody(trickyMessage().Body), c.norm, c.rules)
	if f.CapsWords != 7 || f.Sentences != 6 || f.Words != 19 || f.Chars != 114 || f.Links != 2 || f.QuestionMarks != 2 {
		t.Fatalf("facts %+v", f)
	}
}

func TestStateTruncatesTheBodyOnceAtBodyChars(t *testing.T) {
	c := testComposer(t, nil, nil)
	c.profile.BodyChars = 5
	m := trickyMessage()
	m.Body = "éééééééééé?"
	s := c.State(1, m)
	if !strings.Contains(s, "Email text:\nééééé") || strings.Contains(s, "éééééé") || !strings.Contains(s, "(5 characters)") ||
		strings.Contains(s, "Contains 2 question") {
		t.Fatalf("facts describe the truncated view:\n%s", s)
	}
}

func threadsBucket(minutes int, fine []float64) int {
	return threads.BucketIndex(float64(minutes), fine)
}

func TestRulesAreValidated(t *testing.T) {
	for name, bad := range map[string]string{
		"unknown key":    `{"surprise": 1}`,
		"bad bands":      `{"volume_bands": {"bounds": [3, 1], "labels": ["a", "b", "c"]}, "reply_bands": {"bounds": [0.5], "labels": ["a"]}}`,
		"phrase missing": `{"volume_bands": {"bounds": [1], "labels": ["a", "b"]}, "reply_bands": {"bounds": [0.5], "labels": ["a", "b"]}, "urgency_prior": {"bounds_minutes": [1], "levels": [1, 2]}, "phrases": {"history": "{x"}}`,
		"not json":       `{`,
	} {
		if _, err := loadRules([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	r, _ := loadRules(rulesJSON)
	sc := newScales([]int{60, 240, 1440, 4320}, r.Urgency.BoundsMinutes, r.Urgency.ResolutionMinutes)
	for minutes, want := range map[int]float64{100: 3.25, 120: 3.25, 121: 2.5, 1439: 1.75, 1440: 1.75, 1441: 1.25, 4320: 1.25, 4321: 0.75, 5000: 0.75} {
		fine := threadsBucket(minutes, sc.fine)
		if got := r.Urgency.Levels[sc.coarse(fine, sc.urgency)]; got != want {
			t.Errorf("urgency prior(%v min) = %v, want %v", minutes, got, want)
		}
	}
	if sc.speedLabels[sc.coarse(threadsBucket(150, sc.fine), sc.speed)] != "1h-4h" {
		t.Fatal("speed band of 150 min")
	}
}
