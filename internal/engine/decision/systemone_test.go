package decision

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
)

func TestSystemoneChunksMergesAndAccounts(t *testing.T) {
	qs := taxonomy17()
	srv := newServer(t, chunkReplies(qs, 16)...)
	h := newAdapter(t, provider(srv, "systemone"))
	usage, _ := events.Subscribe(h.bus, engine.TopicUsage, 8)
	res, err := h.ad.Decide(t.Context(), "From: a\nSubject: b", qs)
	if err != nil {
		t.Fatal(err)
	}
	if srv.hits() != 2 || len(res.Answers) != 17 {
		t.Fatalf("17 questions at a cap of 16 = 2 calls: hits %d answers %d", srv.hits(), len(res.Answers))
	}
	if res.Usage.Calls != 2 || res.Usage.InputTokens != 954 || res.Usage.EstimatedUSD != 954*0.05/1e6 {
		t.Fatalf("usage summed over chunks: %+v", res.Usage)
	}
	first, second := srv.bodies[0], srv.bodies[1]
	if len(first["questions"].(map[string]any)) != 16 || len(second["questions"].(map[string]any)) != 1 ||
		first["state"] != second["state"] || first["model"] != "m-1" || srv.auth[0] != "Bearer sk-test-0123456789abcdef" {
		t.Fatalf("chunk requests: %v / %v", first, second)
	}
	if _, ok := second["questions"].(map[string]any)["q16"]; !ok {
		t.Fatal("chunks keep question order: the 17th goes alone in call 2")
	}
	q0 := first["questions"].(map[string]any)["q0"].(map[string]any)
	q1 := first["questions"].(map[string]any)["q1"].(map[string]any)
	q2 := first["questions"].(map[string]any)["q2"].(map[string]any)
	if q0["criteria"].(map[string]any)["true"] != "yes" || q1["criteria"].(map[string]any)["a"] != nil ||
		q1["criteria"].(map[string]any)["b"] != "bee" || len(q2["criteria"].([]any)) != 3 {
		t.Fatalf("criteria shapes: %v %v %v", q0, q1, q2)
	}
	a := res.Answers["q2"]
	if a.Value != 1.86 || a.Probabilities["2"] != 0.87 || *a.Confidence != 0.79 || res.Answers["q0"].Confidence != nil {
		t.Fatalf("answers: %+v / %+v", a, res.Answers["q0"])
	}
	if len(h.sink.recs) != 2 || h.sink.recs[0].Failed || (<-usage.C).InputTokens != 477 {
		t.Fatalf("every call is persisted and published: %+v", h.sink.recs)
	}
}

func TestEncodingsKeepQuestionAndOptionOrder(t *testing.T) {
	qs := taxonomy17()[:3]
	b, _ := systemone{}.encode(&engine.Provider{Model: "m"}, "s", qs)
	if !strings.Contains(string(b), `{"model":"m","state":"s","questions":{"q0":`) || !strings.Contains(string(b), `"criteria":{"b":"bee","a":null}`) {
		t.Fatalf("systemone order: %s", b)
	}
	b, _ = openAI{}.encode(&engine.Provider{Model: "m"}, "s", qs)
	if !strings.Contains(string(b), `"choices":[{"value":"b","description":"bee"},{"value":"a"}]`) {
		t.Fatalf("openai order: %s", b)
	}
}

func TestSystemoneJunkAnswersAreContractErrors(t *testing.T) {
	qs := taxonomy17()[:3] // noul q0, choice q1, score q2
	good := `"q0":{"type":"noul","noul":0.5},"q1":{"type":"choice","choice":"a","probabilities":{"a":1,"b":0},"confidence":1},"q2":{"type":"score","score":2,"probabilities":{"0":0,"1":0,"2":1},"confidence":1}`
	cases := map[string]string{
		"missing answer":         `{"answers":{"q0":{"type":"noul","noul":0.5}}}`,
		"answer not asked":       `{"answers":{` + good + `,"zz":{"type":"noul","noul":1}}}`,
		"bool as number":         strings.Replace(`{"answers":{`+good+`}}`, `"noul":0.5`, `"noul":true`, 1),
		"string as number":       strings.Replace(`{"answers":{`+good+`}}`, `"noul":0.5`, `"noul":"0.5"`, 1),
		"probability above 1":    strings.Replace(`{"answers":{`+good+`}}`, `"noul":0.5`, `"noul":1.5`, 1),
		"negative":               strings.Replace(`{"answers":{`+good+`}}`, `"noul":0.5`, `"noul":-0.1`, 1),
		"wrong type":             strings.Replace(`{"answers":{`+good+`}}`, `"type":"noul"`, `"type":"score"`, 1),
		"score above top":        strings.Replace(`{"answers":{`+good+`}}`, `"score":2`, `"score":2.5`, 1),
		"unknown choice":         strings.Replace(`{"answers":{`+good+`}}`, `"choice":"a"`, `"choice":"c"`, 1),
		"choice not in probs":    strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"a":1,"b":0}`, `"probabilities":{"b":1}`, 1),
		"unknown option prob":    strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"a":1,"b":0}`, `"probabilities":{"a":1,"x":0}`, 1),
		"level key out of scale": strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"0":0,"1":0,"2":1}`, `"probabilities":{"3":1}`, 1),
		"incomplete options":     strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"a":1,"b":0}`, `"probabilities":{"a":1}`, 1),
		"sum not one":            strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"a":1,"b":0}`, `"probabilities":{"a":0.6,"b":0.6}`, 1),
		"choice not argmax":      strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"a":1,"b":0}`, `"probabilities":{"a":0.05,"b":0.95}`, 1),
		"score not expected":     strings.Replace(`{"answers":{`+good+`}}`, `"probabilities":{"0":0,"1":0,"2":1}`, `"probabilities":{"0":1,"1":0,"2":0}`, 1),
		"confidence missing":     strings.Replace(`{"answers":{`+good+`}}`, `,"confidence":1}`, `}`, 1),
		"answers not object":     `{"answers":[]}`,
		"answers absent":         `{"usage":{"input_tokens":1}}`,
		"body is an array":       `[]`,
		"not json":               `<html>gateway</html>`,
		"trailing junk":          `{"answers":{` + good + `}} }`,
		"bad usage":              `{"answers":{` + good + `},"usage":{"input_tokens":-3}}`,
		"empty body":             ``,
	}
	for name, body := range cases {
		srv := newServer(t, reply{200, body})
		h := newAdapter(t, provider(srv, "systemone"))
		res, err := h.ad.Decide(t.Context(), "state", qs)
		if !errors.Is(err, ErrContract) {
			t.Errorf("%s: want ErrContract, got %v", name, err)
		}
		if len(res.Answers) != 0 || res.Usage.Calls != 1 || srv.hits() != 1 {
			t.Errorf("%s: no partial answers, the call is counted and never retried (%d hits)", name, srv.hits())
		}
	}
	// The valid form of the same payload passes, and a missing usage falls back to the estimate.
	srv := newServer(t, reply{200, `{"answers":{` + good + `}}`})
	h := newAdapter(t, provider(srv, "systemone"))
	res, err := h.ad.Decide(t.Context(), "state", qs)
	if err != nil || res.Usage.InputTokens == 0 || res.Answers["q2"].Value != 2 {
		t.Fatalf("valid payload: %v %+v", err, res.Usage)
	}
}

func TestCloudflareEnvelopeAndPathTemplate(t *testing.T) {
	qs := taxonomy17()[:2]
	inner := systemoneAnswers(qs, 50)
	srv := newServer(t, reply{200, `{"result":` + inner + `,"success":true,"errors":[],"messages":[]}`},
		reply{200, `{"result":null,"success":false,"errors":[{"code":5007,"message":"No such model"}]}`})
	p := provider(srv, "systemone")
	p.BaseURL, p.Path, p.Envelope = srv.URL+"/client/v4/accounts/acc 1/ai", "/run/@cf/cloudflare/{model}", engine.EnvelopeCloudflare
	h := newAdapter(t, p)
	res, err := h.ad.Decide(t.Context(), "state", qs)
	if err != nil || len(res.Answers) != 2 || res.Usage.InputTokens != 50 {
		t.Fatalf("envelope: %v %+v", err, res)
	}
	if srv.paths[0] != "/client/v4/accounts/acc 1/ai/run/@cf/cloudflare/m-1" {
		t.Fatalf("path %q", srv.paths[0])
	}
	if _, err := h.ad.Decide(t.Context(), "state", qs); !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), "No such model") {
		t.Fatalf("success=false is an error with the provider's message: %v", err)
	}
}

// Example provider's /v1/systemone (probed live 2026-10-14): the systemone shape
// plus id/object/created, truncated, energy and cost. The reported charge
// (energy-metered) is what the call is accounted at; a truncated state and
// a malformed charge are contract errors; its 400 carries a detail string.
func TestExampleProviderResponseIsAccountedAtItsReportedCharge(t *testing.T) {
	qs := []engine.Question{{Name: "is_test", Type: engine.Noul, Instructions: "Is it a test?"},
		{Name: "kind", Type: engine.Choice, Instructions: "Kind?", Options: []engine.Option{{Name: "test"}, {Name: "work"}, {Name: "spam"}}},
		{Name: "urgency", Type: engine.Score, Instructions: "Urgent?", Levels: []string{"low", "medium", "high"}}}
	verbatim := `{"id":"systemone-021c","object":"systemone","created":1792033490,"model":"aubelle-flash","answers":{
	"is_test":{"type":"noul","noul":0.97},
	"kind":{"type":"choice","choice":"test","confidence":0.9905,"probabilities":{"test":0.9905,"work":0.0054,"spam":0.0041}},
	"urgency":{"type":"score","score":0.03,"confidence":0.9759,"legend":{"0":"low","1":"medium","2":"high"},"probabilities":{"0":0.9759,"1":0.0183,"2":0.0058}}},
	"truncated":false,"usage":{"input_tokens":308,"output_tokens":0},
	"energy":{"energy_joules":12.13,"energy_kwh":0.000003368},"cost":{"request_cost_usd":0.000034,"cache_savings_usd":0.0}}`
	srv := newServer(t, reply{200, verbatim})
	h := newAdapter(t, provider(srv, "systemone"))
	res, err := h.ad.Decide(t.Context(), "state", qs)
	if err != nil || res.Answers["kind"].Choice != "test" || res.Usage.InputTokens != 308 || res.Usage.EstimatedUSD != 0.000034 {
		t.Fatalf("verbatim Example provider answer: %v %+v", err, res)
	}
	if rec := h.sink.recs[0]; !rec.Billed || rec.EstimatedUSD != 0.000034 {
		t.Fatalf("the reported charge is recorded as billed: %+v", rec)
	}
	for name, body := range map[string]string{
		"truncated": strings.Replace(verbatim, `"truncated":false`, `"truncated":true`, 1),
		"bad cost":  strings.Replace(verbatim, `"request_cost_usd":0.000034`, `"request_cost_usd":"0.1"`, 1),
		"neg cost":  strings.Replace(verbatim, `"request_cost_usd":0.000034`, `"request_cost_usd":-1`, 1),
	} {
		h := newAdapter(t, provider(newServer(t, reply{200, body}), "systemone"))
		if res, err := h.ad.Decide(t.Context(), "state", qs); !errors.Is(err, ErrContract) || len(res.Answers) != 0 {
			t.Fatalf("%s: must be a contract error: %v", name, err)
		}
	}
	h = newAdapter(t, provider(newServer(t, reply{400, `{"detail":"'questions' exceeds the maximum of 64 questions"}`}), "systemone"))
	if _, err := h.ad.Decide(t.Context(), "state", qs); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "maximum of 64") {
		t.Fatalf("Example provider 400 detail string: %v", err)
	}
}
