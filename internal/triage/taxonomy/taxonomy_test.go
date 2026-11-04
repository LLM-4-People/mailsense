package taxonomy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
)

// legacyFingerprint is the legacy spec id (PORTING §5); the embedded
// taxonomy must reproduce it byte for byte so legacy caches stay comparable.
const legacyFingerprint = "9c9956169b0c"

func TestBuiltInProfileMatchesTheLegacyTaxonomy(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != legacyFingerprint {
		t.Fatalf("fingerprint %s, want legacy %s", p.Fingerprint, legacyFingerprint)
	}
	if len(p.Questions) != 17 || p.Questions[0].Name != "category" || p.Questions[16].Name != "has_signature" {
		t.Fatalf("17 questions in legacy order (has_signature last, alone in chunk 2): %d", len(p.Questions))
	}
	urgency, _ := p.RoleQuestion(RoleUrgency)
	category, _ := p.RoleQuestion(RoleCategory)
	reply, _ := p.RoleQuestion(RoleReply)
	if urgency.Top() != 4 || len(category.Options) != 7 || category.Options[0].Name != "security" || reply.True == "" {
		t.Fatalf("typed questions: %+v %+v", urgency, category)
	}
	if p.BodyChars != 2000 || p.PriorBlend[RoleReply] != 0.3 || p.Threshold("urgent_from_level") != 3 || p.PriorMinSamples != 3 ||
		!p.IncludeHistory || p.MaxExamples != 6 || !p.UsesField("observed_facts") || p.RoleOf("reply_needed") != RoleReply {
		t.Fatalf("profile defaults: %+v", p)
	}
	for _, q := range p.Questions {
		if err := q.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOverridesMergePerKeyAndNullRemoves(t *testing.T) {
	over := `{"_doc": "mine", "body_chars": 1500,
		"questions": {"has_signature": null, "vip": {"type": "noul", "instructions": "Is the sender a VIP?"}},
		"roles": {"signature": null}, "thresholds": {"reply": 0.6}, "context": {"max_examples": 2}}`
	p, err := Parse([]byte(over), "test.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Questions) != 17 || p.Questions[16].Name != "vip" || p.BodyChars != 1500 || p.Threshold("reply") != 0.6 ||
		p.Threshold("suspicious") != 0.5 || p.MaxExamples != 2 || !p.IncludeFacts || p.Source != "test.json" {
		t.Fatalf("merge: %+v", p)
	}
	if _, ok := p.Roles[Role("signature")]; ok {
		t.Fatal("null removes a role")
	}
	if p.Fingerprint == legacyFingerprint {
		t.Fatal("any question change must change the fingerprint (it invalidates the cache)")
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	over := `{"version": 2, "surprise": 1, "body_chars": 0, "prior_min_samples": true, "escalation": {"category": {"min_margin": 0.1}, "boss": {}},
		"insights": {"suspicious_categories": ["nope"], "extra": 1},
		"state_template": "{sender} {nope} {x!r}",
		"questions": {"urgency": {"type": "score", "instructions": "", "criteria": ["only"]},
		              "category": {"type": "choice", "instructions": "x", "criteria": {"a": 1, "b": "x"}},
		              "reply_needed": {"type": "noul", "instructions": "x", "criteria": {"maybe": "?"}},
		              "odd": {"type": "multi", "instructions": "x"}, "extra": {"type": "noul", "instructions": "x", "weight": 2}},
		"roles": {"topic": "missing", "reply": "category", "boss": "x"},
		"thresholds": {"reply": 1.5, "urgent_from_level": 9, "warp": 1, "sentiment_negative_max": "low"},
		"prior_blend": {"reply": true, "topic": 0.5},
		"context": {"include_facts": "yes", "max_examples": -1, "colour": 1}}`
	_, err := Parse([]byte(over), "bad.json")
	if err == nil {
		t.Fatal("invalid profile accepted")
	}
	for _, want := range []string{"version must be 1", `unknown key "surprise"`, "body_chars", "prior_min_samples",
		"unsupported field {x!r}", "questions/urgency: question \"urgency\": instructions must be a non-empty string", "score needs 2-10 levels", "questions/category: choice criteria must be an object of option descriptions",
		"noul criteria keys", "questions/odd: type", `questions/extra: unknown key "weight"`, `roles/topic: "missing"`,
		"roles/reply: question \"category\" must be a noul", `unknown role "boss"`, "thresholds/reply", "thresholds/urgent_from_level: must be an integer in [0, 0], got 9",
		`unknown threshold "warp"`, "thresholds/sentiment_negative_max", "prior_blend/reply", `prior_blend: unknown role "topic"`,
		"context/include_facts", "context/max_examples", `context: unknown key "colour"`, "escalation/category: min_margin was replaced by min_confidence", "escalation/boss: must be an object for a known role",
		`insights/suspicious_categories: "nope" is not an option`, `insights: unknown key "extra"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	again, _ := Parse([]byte(over), "bad.json")
	if _, err2 := Parse([]byte(over), "bad.json"); again != nil || err2.Error() != err.Error() {
		t.Fatal("problems are reported in a deterministic order")
	}
	for _, junk := range []string{`[]`, `{`, `{"questions": {"a": null, "b": 1}}`, `{"a":1,"a":2}`} {
		if _, err := Parse([]byte(junk), "junk"); err == nil {
			t.Errorf("junk %s accepted", junk)
		}
	}
}

// A dropped role keeps its built-in scale for threshold checks (legacy bug:
// a phantom 0..3 scale rejected urgent_from_level 4).
func TestDroppedRoleThresholdUsesTheBuiltInScale(t *testing.T) {
	if _, err := Parse([]byte(`{"roles": {"urgency": null}, "thresholds": {"urgent_from_level": 4}}`), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{"roles": {"urgency": null}, "thresholds": {"urgent_from_level": 5}}`), "x"); err == nil {
		t.Fatal("5 is outside the 0..4 built-in urgency scale")
	}
}

func TestLoadReadsTheConfiguredFile(t *testing.T) {
	cfg := configtest.New(t, nil)
	p, err := Load(cfg)
	if err != nil || p.Source != "" || p.Fingerprint != legacyFingerprint {
		t.Fatalf("missing file = built-in: %v", err)
	}
	path := filepath.Join(cfg.Paths().DataDir, "triage-profile.json")
	if err := os.WriteFile(path, []byte(`{"body_chars": 900}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err = Load(cfg); err != nil || p.BodyChars != 900 || p.Source != path {
		t.Fatalf("file: %v %+v", err, p)
	}
	if err := os.WriteFile(path, []byte(`{"body_chars": 1.5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cfg); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("errors name the file: %v", err)
	}
}

func TestTemplate(t *testing.T) {
	tm, err := ParseTemplate("a {x} {{lit}} {y}}}")
	if err != nil || tm.Render(map[string]string{"x": "1"}) != "a 1 {lit} }" || len(tm.Fields) != 2 {
		t.Fatalf("%v %q", err, tm.Render(map[string]string{"x": "1"}))
	}
	for _, bad := range []string{"{", "}", "{}", "{0}", "{a.b}", "{a[0]}", "{a:>3}", "{a!s}"} {
		if _, err := ParseTemplate(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestLabels(t *testing.T) {
	p, _ := Default()
	cat, _ := p.RoleQuestion(RoleCategory)
	reply, _ := p.RoleQuestion(RoleReply)
	urg, _ := p.RoleQuestion(RoleUrgency)
	ok := []struct {
		q    *engine.Question
		raw  string
		want Label
	}{{cat, `"spam"`, Label{Choice: "spam"}}, {reply, `true`, Label{Number: 1}}, {reply, `"No"`, Label{Number: 0}},
		{reply, `0.25`, Label{Number: 0.25}}, {urg, `4`, Label{Number: 4}}}
	for _, c := range ok {
		var raw any
		_ = json.Unmarshal([]byte(c.raw), &raw)
		got, err := ParseLabel(c.q, raw)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %+v %v", c.q.Name, c.raw, got, err)
		}
		if back, err := DecodeLabel(c.q, got.JSON(c.q.Type)); err != nil || back != got {
			t.Errorf("round trip %s: %+v %v", c.raw, back, err)
		}
	}
	for _, c := range []struct {
		q   *engine.Question
		raw any
	}{{cat, "nope"}, {cat, 1.0}, {reply, 1.5}, {reply, "maybe"}, {urg, 4.5}, {urg, true}} {
		if _, err := ParseLabel(c.q, c.raw); err == nil {
			t.Errorf("%s %v must fail", c.q.Name, c.raw)
		}
	}
}

// A numeric answer may arrive as a quoted JSON number: the string content
// must be exactly a JSON number literal (whitespace around it is trimmed),
// so units, words, NaN/Inf and other float syntax stay contract errors.
func TestQuotedNumericLabels(t *testing.T) {
	p, _ := Default()
	cat, _ := p.RoleQuestion(RoleCategory)
	sentiment, _ := p.Question("sentiment") // score 0..4
	effort, _ := p.Question("effort")       // score 0..3
	reply, _ := p.RoleQuestion(RoleReply)   // noul 0..1
	ok := []struct {
		name string
		q    *engine.Question
		raw  any
		want Label
	}{
		{"score quoted", sentiment, "3", Label{Number: 3}},
		{"score unquoted", sentiment, json.Number("3"), Label{Number: 3}},
		{"score quoted padded", sentiment, " 3 ", Label{Number: 3}},
		{"score quoted unicode whitespace", sentiment, "\u00a03\u2003", Label{Number: 3}},
		{"score quoted decimal", sentiment, "3.0", Label{Number: 3}},
		{"score quoted fraction", sentiment, "3.25", Label{Number: 3.25}},
		{"score negative zero", sentiment, "-0", Label{Number: 0}},
		{"score quoted exponent", effort, "2e0", Label{Number: 2}},
		{"noul quoted one", reply, "1", Label{Number: 1}},
		{"noul quoted zero", reply, "0", Label{Number: 0}},
		{"noul unquoted one", reply, json.Number("1"), Label{Number: 1}},
		{"noul unquoted zero", reply, json.Number("0"), Label{Number: 0}},
		{"noul quoted fraction", reply, "0.25", Label{Number: 0.25}},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseLabel(c.q, c.raw)
			if err != nil || got != c.want {
				t.Fatalf("%q: %+v %v", c.raw, got, err)
			}
		})
	}
	bad := []struct {
		name string
		q    *engine.Question
		raw  any
	}{
		{"score out of range", sentiment, "4.5"},
		{"score exponent out of range", sentiment, "1e3"}, // a number, but above top
		{"score with unit", sentiment, "3 days"},
		{"score NaN", sentiment, "NaN"},
		{"score inf", sentiment, "Inf"},
		{"score empty", sentiment, ""},
		{"score blank", sentiment, "  "},
		{"score null", sentiment, nil},
		{"score plus sign", sentiment, "+3"},
		{"score leading zero", sentiment, "03"},
		{"score bare fraction", sentiment, ".5"},
		{"score trailing point", sentiment, "3."},
		{"score hex", sentiment, "0x1p4"},
		{"score underscore", sentiment, "1_0"},
		{"score overflow", sentiment, "1e999"},
		{"score boolean", sentiment, true},
		{"noul out of range", reply, "1.5"},
		{"noul negative", reply, "-1"},
		{"choice is never a number", cat, "3"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseLabel(c.q, c.raw); err == nil {
				t.Fatalf("%s %v must fail", c.q.Name, c.raw)
			}
		})
	}
}

// Review 2a #7 (repro TestReviewDuplicateRoleTarget; failed first: the
// profile was accepted and RoleOf alternated between reply and cta over
// 200 calls, so blending applied about half the time).
func TestAQuestionPlaysOneRole(t *testing.T) {
	_, err := Parse([]byte(`{"roles":{"cta":"reply_needed"}}`), "x")
	if err == nil || !strings.Contains(err.Error(), "already plays role") {
		t.Fatalf("two roles on one question must be refused: %v", err)
	}
	p, _ := Default()
	for range 50 {
		if p.RoleOf("reply_needed") != RoleReply {
			t.Fatal("RoleOf is fixed")
		}
	}
}

func TestCutoffsAndUncertainty(t *testing.T) {
	p, err := Parse([]byte(`{"thresholds": {"reply": 0.6, "noul_default": 0.3}}`), "x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Cutoff("reply_needed") != 0.6 || p.Cutoff("has_signature") != 0.3 || p.Cutoff("info_sufficiency") != 0.5 {
		t.Fatal("role thresholds, else noul_default")
	}
	low, high := 0.2, 0.9
	values := map[string]Value{
		"category":     {Type: engine.Choice, Confidence: &low}, // below 0.40: flagged
		"topic":        {Type: engine.Choice, Confidence: &low}, // not an escalation role
		"urgency":      {Type: engine.Score, Confidence: &high}, // confident
		"reply_needed": {Type: engine.Noul, Model: 0.55},        // within 0.10 of 0.5
		"suspicious":   {Type: engine.Noul, Model: 0.02},        // decisive
		"stance":       {Type: engine.Choice, Refused: true},    // not an escalation role
	}
	if got := p.Uncertain(values); len(got) != 2 || got[0] != "category" || got[1] != "reply_needed" {
		t.Fatalf("role-aware escalation: %v", got)
	}
	values["urgency"] = Value{Type: engine.Score, Refused: true}
	if got := p.Uncertain(values); len(got) != 3 {
		t.Fatalf("a refused escalation role is uncertain: %v", got)
	}
	q, _ := Parse([]byte(`{"escalation": {"urgency": {}}}`), "x")
	if q == nil || q.Uncertain(map[string]Value{"urgency": {Type: engine.Score, Confidence: &low}})[0] != "urgency" {
		t.Fatal("a rule without min_confidence falls back to thresholds.low_confidence")
	}
	if len(p.SuspiciousCategories) != 2 || p.KnownRelationships[0] != "existing_client" || p.Threshold(ThresholdSentimentPositive) != 2.5 {
		t.Fatalf("Mailsense additions: %+v", p)
	}
	if _, err := Parse([]byte(`{"thresholds": {"sentiment_positive_min": 1}}`), "x"); err == nil {
		t.Fatal("positive below negative must be refused")
	}
}

// Finding (rules review #5): three uncertainty definitions coexisted
// (|p − 0.5| here, a grid sweep in feedback, the side chosen in layer 1).
// One now (internal/selective): a yes/no answer's confidence is the
// probability of the side its cutoff chooses; an abstained answer is
// always uncertain.
func TestUncertaintyUsesTheOneConfidenceDefinition(t *testing.T) {
	p, err := Parse([]byte(`{"thresholds": {"reply": 0.3}}`), "x")
	if err != nil {
		t.Fatal(err)
	}
	// P(yes) 0.32 at cutoff 0.3 chooses yes with confidence 0.32: below
	// 0.60 (|p − 0.5| = 0.18 called it certain)
	if got := p.Uncertain(map[string]Value{"reply_needed": {Type: engine.Noul, Model: 0.32}}); len(got) != 1 {
		t.Fatalf("a yes chosen at 0.32 is certain: %v", got)
	}
	if got := p.Uncertain(map[string]Value{"reply_needed": {Type: engine.Noul, Model: 0.95}}); len(got) != 0 {
		t.Fatalf("a decisive yes is uncertain: %v", got)
	}
	high := 0.99
	if got := p.Uncertain(map[string]Value{"category": {Type: engine.Choice, Abstain: engine.AbstainBelowTarget, Confidence: &high}}); len(got) != 1 {
		t.Fatalf("an abstained answer is certain: %v", got)
	}
}

// A route-level abstention is a deliberate non-answer: with one certified
// question it must not flag every message for escalation. Refusals,
// below-target abstentions and low-confidence answers still do, in
// question order.
func TestUncertaintySkipsDeliberateRouteNonAnswers(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	low, high := 0.2, 0.9
	cases := []struct {
		name   string
		values map[string]Value
		want   []string
	}{
		{"low confidence", map[string]Value{
			"category": {Type: engine.Choice, Confidence: &low},
		}, []string{"category"}},
		{"refused", map[string]Value{
			"reply_needed": {Type: engine.Noul, Model: 0.9, Refused: true},
		}, []string{"reply_needed"}},
		{"below target", map[string]Value{
			"category": {Type: engine.Choice, Confidence: &high, Abstain: engine.AbstainBelowTarget},
		}, []string{"category"}},
		{"route non-answers", map[string]Value{
			"category":     {Type: engine.Choice, Confidence: &high, Abstain: route.AbstainNotCertified},
			"reply_needed": {Type: engine.Noul, Model: 0.9, Abstain: route.AbstainBelowCoverage},
			"urgency":      {Type: engine.Score, Confidence: &high, Abstain: route.AbstainNoAnswer},
		}, nil},
		{"certified high confidence", map[string]Value{
			"category":     {Type: engine.Choice, Confidence: &high},
			"reply_needed": {Type: engine.Noul, Model: 0.9},
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Uncertain(tc.values); !slices.Equal(got, tc.want) {
				t.Fatalf("Uncertain() = %v, want %v", got, tc.want)
			}
		})
	}
}
