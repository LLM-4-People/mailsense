package decision

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

func openAIProvider(srv *server) engine.Provider {
	p := provider(srv, "openai_decisions")
	p.Path, p.MaxQuestions = "/decisions", 200
	return p
}

const openAIGood = `{"model":"gpt-6-luna","answers":[
 {"type":"predicate","name":"q0","probability":0.25},
 {"type":"choice","name":"q1","choice":"b","probabilities":[{"value":"a","probability":0.1},{"value":"b","probability":0.9}],"confidence":0.8},
 {"type":"score","name":"q2","score":0.4,"probabilities":[{"value":0,"label":"0","probability":0.6},{"value":1,"label":"1","probability":0.4},{"value":2,"label":"2","probability":0}],"confidence":0.6}],
 "usage":{"input_tokens":321,"output_tokens":0}}`

func TestOpenAIDecisionsCodec(t *testing.T) {
	qs := taxonomy17()[:3]
	srv := newServer(t, reply{200, openAIGood})
	h := newAdapter(t, openAIProvider(srv))
	res, err := h.ad.Decide(t.Context(), "Subject: x", qs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "gpt-6-luna" || res.Answers["q0"].Value != 0.25 || res.Answers["q1"].Choice != "b" ||
		res.Answers["q2"].Probabilities["1"] != 0.4 || res.Usage.InputTokens != 321 {
		t.Fatalf("decoded %+v", res)
	}
	body := srv.bodies[0]
	questions := body["questions"].([]any)
	q0, q1, q2 := questions[0].(map[string]any), questions[1].(map[string]any), questions[2].(map[string]any)
	if body["input"] != "Subject: x" || q0["type"] != "predicate" || q0["instructions"] != "Is it?\nYes: yes\nNo: no" ||
		len(q1["choices"].([]any)) != 2 || q2["levels"].([]any)[2].(map[string]any)["description"] != "lots" {
		t.Fatalf("encoded %v", body)
	}
}

func TestOpenAIRefusalAndJunk(t *testing.T) {
	qs := taxonomy17()[:3]
	refusal := strings.Replace(openAIGood, `{"type":"predicate","name":"q0","probability":0.25}`, `{"type":"refusal","name":"q0"}`, 1)
	srv := newServer(t, reply{200, refusal})
	h := newAdapter(t, openAIProvider(srv))
	res, err := h.ad.Decide(t.Context(), "s", qs)
	if err != nil || !res.Answers["q0"].Refused {
		t.Fatalf("a refusal is a valid answer without a value: %v %+v", err, res.Answers["q0"])
	}
	junk := map[string]string{
		"too few answers":     `{"answers":[{"type":"predicate","probability":0.2}]}`,
		"out of order":        strings.Replace(openAIGood, `"name":"q0"`, `"name":"q1"`, 1),
		"predicate as choice": strings.Replace(openAIGood, `"type":"predicate"`, `"type":"choice"`, 1),
		"answers as object":   `{"answers":{}}`,
		"bool probability":    strings.Replace(openAIGood, `"probability":0.25`, `"probability":false`, 1),
		"unknown level label": strings.Replace(openAIGood, `"label":"1"`, `"label":"high"`, 1),
		"repeated option":     strings.Replace(openAIGood, `{"value":"a","probability":0.1}`, `{"value":"b","probability":0.1}`, 1),
		"probabilities junk":  strings.Replace(openAIGood, `"probabilities":[{"value":"a"`, `"probabilities":7,"x":[{"value":"a"`, 1),
	}
	for name, body := range junk {
		srv := newServer(t, reply{200, body})
		h := newAdapter(t, openAIProvider(srv))
		if _, err := h.ad.Decide(t.Context(), "s", qs); !errors.Is(err, ErrContract) {
			t.Errorf("%s: want ErrContract, got %v", name, err)
		}
	}
	srv = newServer(t, reply{429, `{"error":{"type":"insufficient_quota","code":"monthly_spend_limit_exceeded","message":"limit"}}`})
	p := openAIProvider(srv)
	p.Retry.QuotaCodes = []string{"spend_limit_exceeded"}
	h = newAdapter(t, p)
	if _, err := h.ad.Decide(t.Context(), "s", qs); !errors.Is(err, ErrQuota) || srv.hits() != 1 {
		t.Fatalf("spend limit: %v hits %d", err, srv.hits())
	}
}

func TestNoulWithoutCriteriaKeepsInstructions(t *testing.T) {
	q := engine.Question{Name: "n", Type: engine.Noul, Instructions: "Plain?"}
	b, _ := openAI{}.encode(&engine.Provider{Model: "m", NoulFold: "{instructions} T={true}"}, "s", []engine.Question{q})
	if !strings.Contains(string(b), `"instructions":"Plain?"`) {
		t.Fatalf("%s", b)
	}
	b, _ = systemone{}.encode(&engine.Provider{Model: "m"}, "s", []engine.Question{q})
	if strings.Contains(string(b), "criteria") {
		t.Fatalf("no criteria object without criteria: %s", b)
	}
}
