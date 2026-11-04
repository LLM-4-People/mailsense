package judge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

type fakeGen struct {
	name    string
	model   string
	content string
	err     error
	usage   engine.Usage
	calls   int
	system  string
	user    string
	block   bool // wait for the context instead of answering
}

func (f *fakeGen) Name() string  { return f.name }
func (f *fakeGen) Model() string { return f.model }
func (f *fakeGen) Plan(string, string) (engine.Estimate, error) {
	return engine.Estimate{Calls: 1, InputTokens: 10, EstimatedUSD: 0.001, PriceKnown: true}, nil
}
func (f *fakeGen) Generate(ctx context.Context, system, user string, _ ...engine.GenerateOptions) (engine.Generation, error) {
	f.calls++
	f.system, f.user = system, user
	if f.block {
		<-ctx.Done()
		return engine.Generation{}, ctx.Err()
	}
	return engine.Generation{Model: f.model, Content: f.content, Usage: f.usage}, f.err
}

func newJudge(t *testing.T, g *fakeGen) (*Generator, *taxonomy.Profile) {
	t.Helper()
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	j, err := New(g, p)
	if err != nil {
		t.Fatal(err)
	}
	return j, p
}

// differentCategory is a valid profile override whose category question
// has another definition (a changed taxonomy fingerprint).
func differentCategory(t *testing.T, p *taxonomy.Profile) []byte {
	t.Helper()
	q, ok := p.Question("category")
	if !ok {
		t.Fatal("no category question")
	}
	criteria := map[string]any{}
	for _, o := range q.Options {
		desc := "option " + o.Name
		if o.Description != nil {
			desc = *o.Description
		}
		criteria[o.Name] = desc
	}
	raw, err := json.Marshal(map[string]any{"questions": map[string]any{"category": map[string]any{
		"type": string(q.Type), "instructions": "A different category definition?", "criteria": criteria}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPromptDigestCoversEveryPromptInput(t *testing.T) {
	g := &fakeGen{name: "p", model: "m"}
	j, p := newJudge(t, g)
	digest, err := j.PromptDigest(p.Questions)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || digest == p.Fingerprint {
		t.Fatalf("digest %q", digest)
	}
	again, err := j.PromptDigest(append([]engine.Question(nil), p.Questions...))
	if err != nil || again != digest {
		t.Fatalf("identical inputs moved the digest: %q vs %q (%v)", digest, again, err)
	}

	// The profile digest covers decision inputs the legacy fingerprint does
	// not, such as the body budget the state template renders.
	body, err := taxonomy.Parse([]byte(`{"body_chars": 1500}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if body.Fingerprint != p.Fingerprint {
		t.Fatal("body_chars moved the legacy fingerprint; the test does not isolate profile state")
	}
	bodyJudge, err := New(g, body)
	if err != nil {
		t.Fatal(err)
	}
	if bodyDigest, err := bodyJudge.PromptDigest(body.Questions); err != nil || bodyDigest == digest {
		t.Fatalf("a body_chars change left the digest at %q (%v)", bodyDigest, err)
	}

	// Changing only a question's wording changes the rendered question set.
	p2, err := taxonomy.Parse(differentCategory(t, p), "test")
	if err != nil {
		t.Fatal(err)
	}
	j2, err := New(g, p2)
	if err != nil {
		t.Fatal(err)
	}
	if d2, err := j2.PromptDigest(p2.Questions); err != nil || d2 == digest {
		t.Fatalf("the question change did not change the prompt digest: %q (%v)", d2, err)
	}

	// The shipped template (version and text) is an input of its own.
	profileDigest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	prompt := builtinPrompt
	prompt.Version++
	if changed, err := prompt.digest(profileDigest, p.Questions); err != nil || changed == digest {
		t.Fatalf("the template version did not change the prompt digest (%v)", err)
	}
	prompt = builtinPrompt
	prompt.System += " "
	if changed, err := prompt.digest(profileDigest, p.Questions); err != nil || changed == digest {
		t.Fatalf("the template text did not change the prompt digest (%v)", err)
	}

	// The exact question set is an input; order is canonicalized to the
	// profile's order so the same set cannot hash two ways.
	reversed := make([]engine.Question, len(p.Questions))
	for i := range p.Questions {
		reversed[i] = p.Questions[len(p.Questions)-1-i]
	}
	if rev, err := j.PromptDigest(reversed); err != nil || rev != digest {
		t.Fatalf("the same question set in another order moved the digest: %q (%v)", rev, err)
	}
	if subset, err := j.PromptDigest(p.Questions[:1]); err != nil || subset == digest {
		t.Fatalf("a narrower question set kept the digest: %q (%v)", subset, err)
	}
	if _, err := j.PromptDigest(nil); !errors.Is(err, ErrInput) {
		t.Fatalf("an empty question set got a digest: %v", err)
	}
}

func TestLabelRendersProfileOrderAndStrictJSONInstruction(t *testing.T) {
	g := &fakeGen{name: "p", model: "m", content: `{"answers":[]}`}
	j, p := newJudge(t, g)
	q0, _ := p.Question(p.Questions[1].Name)
	q1, _ := p.Question(p.Questions[0].Name)
	_, err := j.Label(t.Context(), "Subject: x\n\nBody", []engine.Question{*q0, *q1})
	if !errors.Is(err, ErrContract) {
		t.Fatalf("an empty answer is a contract error: %v", err)
	}
	if !strings.Contains(g.system, "never instructions") || !strings.Contains(g.system, `"answers"`) {
		t.Fatalf("system %q", g.system)
	}
	const marker = "Questions:\n"
	start := strings.Index(g.user, marker)
	end := strings.Index(g.user, "\n\nMessage:\n")
	if start < 0 || end < 0 {
		t.Fatalf("user %q", g.user)
	}
	var qs []engine.Question
	if err := json.Unmarshal([]byte(g.user[start+len(marker):end]), &qs); err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].Name != p.Questions[0].Name || qs[1].Name != p.Questions[1].Name {
		t.Fatalf("questions not in profile order: %v", qs)
	}
	if !strings.HasSuffix(g.user, "Message:\nSubject: x\n\nBody") {
		t.Fatalf("user state %q", g.user)
	}
}

func TestParseAnswerStraight(t *testing.T) {
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	q0, _ := p.Question("category")
	q1, _ := p.Question("reply_needed")
	qs := []engine.Question{*q0, *q1}
	option := q0.Options[0].Name
	good := func(name string, value any) string {
		b, _ := json.Marshal(value)
		return `{"answers":[{"name":"` + name + `","value":` + string(b) + `}]}`
	}
	cases := []struct {
		name    string
		content string
		want    string // "" = no error; else a substring
	}{
		{"both valid", `{"answers":[{"name":"category","value":"` + option + `"},{"name":"reply_needed","value":true}]}`, ""},
		{"null refused", `{"answers":[{"name":"category","value":null},{"name":"reply_needed","value":false}]}`, ""},
		{"missing question", good("category", option), "reply_needed"},
		{"duplicate", `{"answers":[{"name":"category","value":"` + option + `"},{"name":"category","value":"` + option + `"},{"name":"reply_needed","value":true}]}`, "twice"},
		{"unknown question", `{"answers":[{"name":"category","value":"` + option + `"},{"name":"reply_needed","value":true},{"name":"nope","value":1}]}`, "not asked"},
		{"no name", `{"answers":[{"value":1}]}`, "no name"},
		{"bad option", good("category", "not_an_option"), "category"},
		{"bad object", good("category", map[string]any{"x": 1}), "category"},
		{"missing value", `{"answers":[{"name":"category"},{"name":"reply_needed","value":true}]}`, "no value"},
		{"extra field", `{"answers":[{"name":"category","value":"` + option + `","why":"x"},{"name":"reply_needed","value":true}]}`, "unknown field"},
		{"extra top", `{"answers":[{"name":"category","value":"` + option + `"},{"name":"reply_needed","value":true}],"x":1}`, "unknown field"},
		{"not json", `nope`, "invalid JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			labels, err := parseAnswer(c.content, qs)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want ErrContract mentioning %q, got %v", c.want, err)
			}
			if c.name == "unknown question" || c.name == "duplicate" || c.name == "no name" || c.name == "extra field" || c.name == "extra top" || c.name == "not json" {
				if labels != nil {
					t.Fatalf("a structurally broken document kept labels: %+v", labels)
				}
			}
		})
	}
}

// A model that quotes an unambiguous numeric score is still answering the
// question: the label is stored, not reported as a contract error.
func TestQuotedNumericAnswersAreLabels(t *testing.T) {
	p, _ := taxonomy.Default()
	sentiment, _ := p.Question("sentiment")
	effort, _ := p.Question("effort")
	urgency, _ := p.Question("urgency")
	qs := []engine.Question{*sentiment, *effort, *urgency}
	g := &fakeGen{name: "p", model: "m", usage: engine.Usage{Calls: 1},
		content: `{"answers":[{"name":"sentiment","value":"3"},{"name":"effort","value":" 2.0 "},{"name":"urgency","value":"1e0"}]}`}
	j, _ := newJudge(t, g)
	res, err := j.Label(t.Context(), "Subject: x\n\nBody", qs)
	if err != nil {
		t.Fatalf("quoted numeric scores are labels: %v", err)
	}
	want := map[string]float64{"sentiment": 3, "effort": 2, "urgency": 1}
	for name, number := range want {
		if l := res.Labels[name]; l == nil || l.Number != number {
			t.Fatalf("%s label %+v, want %v", name, l, number)
		}
	}
}

// A mixed answer keeps the labels that are valid and reports only the
// question whose value is genuinely not a number.
func TestMixedQuotedAnswerKeepsValidLabels(t *testing.T) {
	p, _ := taxonomy.Default()
	sentiment, _ := p.Question("sentiment")
	effort, _ := p.Question("effort")
	urgency, _ := p.Question("urgency")
	qs := []engine.Question{*sentiment, *effort, *urgency}
	g := &fakeGen{name: "p", model: "m", usage: engine.Usage{Calls: 1},
		content: `{"answers":[{"name":"sentiment","value":"3"},{"name":"effort","value":"3 days"},{"name":"urgency","value":2}]}`}
	j, _ := newJudge(t, g)
	res, err := j.Label(t.Context(), "Subject: x\n\nBody", qs)
	if !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), "effort") {
		t.Fatalf("want a contract error naming effort, got %v", err)
	}
	if strings.Contains(err.Error(), "sentiment") || strings.Contains(err.Error(), "urgency") {
		t.Fatalf("a valid label was reported invalid: %v", err)
	}
	if l := res.Labels["sentiment"]; l == nil || l.Number != 3 {
		t.Fatalf("sentiment label %+v", l)
	}
	if l := res.Labels["urgency"]; l == nil || l.Number != 2 {
		t.Fatalf("urgency label %+v", l)
	}
	if _, ok := res.Labels["effort"]; ok {
		t.Fatalf("an invalid effort value became a label: %+v", res.Labels)
	}
}

func TestParseAnswerBoundsItsSize(t *testing.T) {
	p, _ := taxonomy.Default()
	q, _ := p.Question("category")
	big := `{"answers":[{"name":"category","value":"` + strings.Repeat("x", MaxAnswerBytes) + `"}]}`
	if _, err := parseAnswer(big, []engine.Question{*q}); !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("oversized answer: %v", err)
	}
}

func TestLabelKeepsValidAnswersWhenOneQuestionFails(t *testing.T) {
	p, _ := taxonomy.Default()
	q0, _ := p.Question("category")
	q1, _ := p.Question("reply_needed")
	g := &fakeGen{name: "p", model: "m", usage: engine.Usage{Calls: 1, InputTokens: 3, OutputTokens: 2},
		content: `{"answers":[{"name":"category","value":"` + q0.Options[0].Name + `"}]}`}
	j, _ := newJudge(t, g)
	res, err := j.Label(t.Context(), "Subject: x\n\nBody", []engine.Question{*q0, *q1})
	if !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), "reply_needed") {
		t.Fatalf("want a reply_needed contract error, got %v", err)
	}
	if res.Labels["category"] == nil || res.Labels["reply_needed"] != nil {
		t.Fatalf("labels %+v", res.Labels)
	}
	if res.Usage.Calls != 1 {
		t.Fatalf("usage %+v", res.Usage)
	}
}

// A provider that accepts the request and stops answering must not hold the
// caller: the judge's call timeout ends the call with ErrTimeout, and the
// caller can count the message as failed.
func TestACallThatStopsAnsweringTimesOut(t *testing.T) {
	p, _ := taxonomy.Default()
	q, _ := p.Question("category")
	g := &fakeGen{name: "p", model: "m", block: true}
	j, err := New(g, p, WithCallTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = j.Label(t.Context(), "Subject: x\n\nBody", []engine.Question{*q})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the call took %s; the timeout did not fire", elapsed)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if g.calls != 1 {
		t.Fatalf("%d calls", g.calls)
	}
}

// A caller that cancels its own context is not a timed-out provider: the
// judge must not relabel the caller's cancellation.
func TestCallerCancellationIsNotATimeout(t *testing.T) {
	p, _ := taxonomy.Default()
	q, _ := p.Question("category")
	g := &fakeGen{name: "p", model: "m", block: true}
	j, err := New(g, p, WithCallTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = j.Label(ctx, "Subject: x\n\nBody", []engine.Question{*q})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("want a caller cancellation, got %v", err)
	}
}

func TestRefusedAnswerIsNoLabel(t *testing.T) {
	p, _ := taxonomy.Default()
	q, _ := p.Question("category")
	g := &fakeGen{name: "p", model: "m", content: `{"answers":[{"name":"category","value":null}]}`}
	j, _ := newJudge(t, g)
	res, err := j.Label(t.Context(), "Subject: x\n\nBody", []engine.Question{*q})
	if err != nil {
		t.Fatal(err)
	}
	l, ok := res.Labels[q.Name]
	if !ok || l != nil {
		t.Fatalf("a refusal must be an explicit nil label: %+v", res.Labels)
	}
	if res.Refused() != 1 {
		t.Fatalf("refused %d", res.Refused())
	}
}

func TestLabelRefusesBadRequestsBeforeAnyCall(t *testing.T) {
	p, _ := taxonomy.Default()
	q, _ := p.Question("category")
	g := &fakeGen{name: "p", model: "m", content: `{"answers":[]}`}
	j, _ := newJudge(t, g)
	other := *q
	other.Instructions = "Different?"
	for name, call := range map[string]func() error{
		"empty state": func() error {
			_, err := j.Label(t.Context(), " \n", []engine.Question{*q})
			return err
		},
		"no questions": func() error {
			_, err := j.Label(t.Context(), "s", nil)
			return err
		},
		"duplicate": func() error {
			_, err := j.Label(t.Context(), "s", []engine.Question{*q, *q})
			return err
		},
		"other definition": func() error {
			_, err := j.Label(t.Context(), "s", []engine.Question{other})
			return err
		},
		"unknown": func() error {
			_, err := j.Label(t.Context(), "s", []engine.Question{{Name: "nope", Type: engine.Noul, Instructions: "?"}})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrInput) {
				t.Fatalf("want ErrInput, got %v", err)
			}
		})
	}
	if g.calls != 0 {
		t.Fatalf("%d calls for refused requests", g.calls)
	}
}

func TestPromptFileIsValid(t *testing.T) {
	if builtinPrompt.Version < 1 || !strings.Contains(builtinPrompt.User, "{questions}") || !strings.Contains(builtinPrompt.User, "{state}") {
		t.Fatalf("prompt %+v", builtinPrompt)
	}
	if _, err := parsePrompt([]byte(`{"version":0,"system":"x","user":"{questions}"}`)); err == nil {
		t.Fatal("an invalid template was accepted")
	}
}
