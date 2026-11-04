package priority

import (
	"math"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// TestLeanRouteSignalsMatchFullDecode is the consumer-side parity table for
// the store's lean route decode. The store now materializes only the fields
// the ranking reads (a score's distribution included, every other
// distribution, confidence, layer and rule dropped); this table proves that
// shape produces the same Signals, reasons and score as the full
// route.Answer the store returned before. The lean fixture mirrors
// decodePriorityRouteAnswer; internal/store's decode test pins the decoder's
// exact output.
func TestLeanRouteSignalsMatchFullDecode(t *testing.T) {
	p := defaultProfile(t)
	keys := neededQuestions(p)
	urgency := p.QuestionName(taxonomy.RoleUrgency)
	category := p.QuestionName(taxonomy.RoleCategory)
	reply := p.QuestionName(taxonomy.RoleReply)
	missing := p.QuestionName(taxonomy.RoleInfo)
	if missing == "" {
		missing = p.QuestionName(taxonomy.RoleEffort)
	}

	cases := []struct {
		name   string
		mutate func(map[string]route.Answer)
		check  func(*testing.T, Signals)
	}{
		{
			name: "fresh",
			check: func(t *testing.T, sig Signals) {
				if sig.Urgency != 2 || sig.ReplyNeeded != 1 {
					t.Fatalf("fresh signals %+v", sig)
				}
			},
		},
		{
			name: "refused",
			mutate: func(all map[string]route.Answer) {
				all[reply] = route.Answer{Answer: engine.Answer{Type: engine.Noul, Refused: true, Value: 0.9},
					Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.ReplyNeeded != 0 {
					t.Fatalf("a refused answer produced reply %d", sig.ReplyNeeded)
				}
			},
		},
		{
			name: "abstained",
			mutate: func(all map[string]route.Answer) {
				all[reply] = route.Answer{Answer: engine.Answer{Type: engine.Noul, Abstain: route.AbstainNotCertified,
					Probabilities: map[string]float64{engine.NoulNo: 0.4, engine.NoulYes: 0.6}}}
				all[urgency] = route.Answer{Answer: engine.Answer{Type: engine.Score, Abstain: engine.AbstainBelowTarget,
					Probabilities: map[string]float64{"0": 0.1, "1": 0.2, "2": 0.3, "3": 0.2, "4": 0.2}}}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.ReplyNeeded != 0 || sig.Urgency != 0 {
					t.Fatalf("an abstained answer produced signals %+v", sig)
				}
			},
		},
		{
			name: "missing",
			mutate: func(all map[string]route.Answer) {
				delete(all, category)
				delete(all, urgency)
				delete(all, missing)
			},
			check: func(t *testing.T, sig Signals) {
				if sig.Category != "" || sig.Urgency != 0 {
					t.Fatalf("a missing answer produced signals %+v", sig)
				}
			},
		},
		{
			name: "type mismatched",
			mutate: func(all map[string]route.Answer) {
				all[reply] = route.Answer{Answer: engine.Answer{Type: engine.Score, Value: 0.9,
					Probabilities: map[string]float64{"0": 0.1, "1": 0.9}}, Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.ReplyNeeded != 0 {
					t.Fatalf("a type-mismatched answer produced reply %d", sig.ReplyNeeded)
				}
			},
		},
		{
			// The case the lean decode must preserve: the level is the class
			// the distribution chooses (4), not round(Value) (2). Dropping a
			// score's distribution would change the ranking here.
			name: "score with a distribution that decides another class",
			mutate: func(all map[string]route.Answer) {
				all[urgency] = route.Answer{Answer: engine.Answer{Type: engine.Score, Value: 1.6,
					Probabilities: map[string]float64{"0": 0.02, "1": 0.08, "2": 0.10, "3": 0.20, "4": 0.60},
					Confidence:    probabilityPtr(0.60)}, Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.Urgency != 4 {
					t.Fatalf("urgency %d, want the distribution's class 4 (round(value) is 2)", sig.Urgency)
				}
				stripped := map[string]route.Answer{urgency: {Answer: engine.Answer{Type: engine.Score, Value: 1.6}}}
				answers, _, err := answersFor(p, keys, stripped, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := signals(p, answers).Urgency; got != 2 {
					t.Fatalf("dropping the distribution produced urgency %d, want round(value) 2", got)
				}
			},
		},
		{
			name: "score without a distribution",
			mutate: func(all map[string]route.Answer) {
				all[urgency] = route.Answer{Answer: engine.Answer{Type: engine.Score, Value: 3},
					Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.Urgency != 3 {
					t.Fatalf("urgency %d, want round(value) 3", sig.Urgency)
				}
			},
		},
		{
			name: "score with a missing level key",
			mutate: func(all map[string]route.Answer) {
				all[urgency] = route.Answer{Answer: engine.Answer{Type: engine.Score, Value: 2,
					Probabilities: map[string]float64{"0": 0.9, "1": 0.1}, Confidence: probabilityPtr(0.9)},
					Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.Urgency != 2 {
					t.Fatalf("urgency %d, want round(value) 2 when the distribution is unusable", sig.Urgency)
				}
			},
		},
		{
			name: "score with a wrong-length distribution",
			mutate: func(all map[string]route.Answer) {
				all[urgency] = route.Answer{Answer: engine.Answer{Type: engine.Score, Value: 2,
					Probabilities: map[string]float64{"0": 0.05, "1": 0.05, "2": 0.10, "3": 0.60, "4": 0.10, "5": 0.10},
					Confidence:    probabilityPtr(0.60)}, Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.Urgency != 3 {
					t.Fatalf("urgency %d, want the class 3 the first five levels choose", sig.Urgency)
				}
			},
		},
		{
			name: "score with a non-finite distribution",
			mutate: func(all map[string]route.Answer) {
				all[urgency] = route.Answer{Answer: engine.Answer{Type: engine.Score, Value: 2,
					Probabilities: map[string]float64{"0": math.NaN(), "1": 0.3, "2": 0.3, "3": 0.2, "4": 0.2},
					Confidence:    probabilityPtr(0.3)}, Layer: route.Layer1, Rule: "key"}
			},
			check: func(t *testing.T, sig Signals) {
				if sig.Urgency != 0 {
					t.Fatalf("urgency %d, want the deterministic class 0 the existing consumer chooses", sig.Urgency)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := fullParityAnswers(p)
			if tc.mutate != nil {
				tc.mutate(full)
			}
			lean := leanRouteAnswers(p, full)

			fullAnswers, fullPreview, err := answersFor(p, keys, full, nil)
			if err != nil {
				t.Fatal(err)
			}
			leanAnswers, leanPreview, err := answersFor(p, keys, lean, nil)
			if err != nil {
				t.Fatal(err)
			}
			fullSig, leanSig := signals(p, fullAnswers), signals(p, leanAnswers)
			if !reflect.DeepEqual(fullSig, leanSig) {
				t.Fatalf("signals differ between the full and lean answers:\n full %+v\n lean %+v", fullSig, leanSig)
			}
			if fullPreview != leanPreview {
				t.Fatalf("preview flag %v vs %v", fullPreview, leanPreview)
			}
			if tc.check != nil {
				tc.check(t, fullSig)
			}

			fullRow, err := signalsFor(p, full, nil)
			if err != nil {
				t.Fatal(err)
			}
			leanRow, err := signalsFor(p, lean, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fullRow, leanRow) {
				t.Fatalf("row signals differ:\n full %+v\n lean %+v", fullRow, leanRow)
			}
			if !reflect.DeepEqual(fullSig, fullRow.sig) {
				t.Fatalf("signalsFor disagrees with signals:\n signals %+v\n signalsFor %+v", fullSig, fullRow.sig)
			}

			in := Input{Message: testMessage(receivedAgo(0)), Profile: p, Now: testNow}
			at, dated := mail.Parse(in.Message.ReceivedAt)
			fullItem, fullOK := scoreRowSignals(in, fullRow, at, dated, false)
			leanItem, leanOK := scoreRowSignals(in, leanRow, at, dated, false)
			if fullOK != leanOK {
				t.Fatalf("ranking inclusion differs: full %v, lean %v", fullOK, leanOK)
			}
			if !fullOK {
				return
			}
			if fullItem.Score != leanItem.Score || !reflect.DeepEqual(fullItem.Reasons, leanItem.Reasons) ||
				fullItem.Signals != leanItem.Signals {
				t.Fatalf("ranked item differs:\n full %+v\n lean %+v", fullItem, leanItem)
			}
		})
	}
}

// fullParityAnswers is a complete route answer map for every profile
// question, the way the store held it before the lean decode: a score and a
// choice answer carry their confidence and full distribution, and every
// route answer names its layer and rule.
func fullParityAnswers(p *taxonomy.Profile) map[string]route.Answer {
	out := make(map[string]route.Answer, len(p.Questions))
	for i := range p.Questions {
		q := &p.Questions[i]
		switch q.Type {
		case engine.Noul:
			value := 0.1
			switch q.Name {
			case p.QuestionName(taxonomy.RoleReply), p.QuestionName(taxonomy.RoleQuestion):
				value = 0.9
			case p.QuestionName(taxonomy.RoleDecision):
				value = 0.7
			case p.QuestionName(taxonomy.RoleInfo):
				value = 0.8
			}
			out[q.Name] = route.Answer{Answer: engine.Answer{Type: engine.Noul, Value: value},
				Layer: route.LayerOndelle, Rule: "ondelle"}
		case engine.Choice:
			chosen := q.Options[0].Name
			switch q.Name {
			case p.QuestionName(taxonomy.RoleCategory):
				chosen = "work"
			case p.QuestionName(taxonomy.RoleTopic):
				chosen = "project_work"
			case p.QuestionName(taxonomy.RoleAsksFor):
				chosen = "answer_question"
			case p.QuestionName(taxonomy.RoleRelationship):
				chosen = "existing_client"
			case p.QuestionName(taxonomy.RoleStance):
				chosen = "asks_questions"
			}
			out[q.Name] = parityChoiceAnswer(q, chosen)
		case engine.Score:
			probs := make(map[string]float64, len(q.Levels))
			peak := min(2, len(q.Levels)-1)
			rest := 0.5 / float64(len(q.Levels)-1)
			for level := range q.Levels {
				if level == peak {
					probs[engine.LevelKey(level)] = 0.5
				} else {
					probs[engine.LevelKey(level)] = rest
				}
			}
			out[q.Name] = parityScoreAnswer(float64(peak), probs)
		}
	}
	return out
}

// parityChoiceAnswer is one choice answer with a confidence and a full
// option distribution.
func parityChoiceAnswer(q *engine.Question, chosen string) route.Answer {
	probs := make(map[string]float64, len(q.Options))
	rest := 0.45 / float64(len(q.Options)-1)
	for _, o := range q.Options {
		if o.Name == chosen {
			probs[o.Name] = 0.55
			continue
		}
		probs[o.Name] = rest
	}
	return route.Answer{Answer: engine.Answer{Type: engine.Choice, Choice: chosen, Probabilities: probs,
		Confidence: probabilityPtr(0.55)}, Layer: route.Layer1, Rule: "key"}
}

// parityScoreAnswer is one score answer with a confidence and a full level
// distribution.
func parityScoreAnswer(value float64, probs map[string]float64) route.Answer {
	confidence := 0.0
	for _, p := range probs {
		confidence = max(confidence, p)
	}
	return route.Answer{Answer: engine.Answer{Type: engine.Score, Value: value, Probabilities: probs,
		Confidence: probabilityPtr(confidence)}, Layer: route.LayerOndelle, Rule: "ondelle"}
}

// leanRouteAnswers is the shape the store's lean route decode returns: the
// fields the ranking reads, with a score's distribution kept and every
// other distribution, confidence, layer and rule dropped.
func leanRouteAnswers(p *taxonomy.Profile, full map[string]route.Answer) map[string]route.Answer {
	out := make(map[string]route.Answer, len(full))
	for name, a := range full {
		lean := route.Answer{Answer: engine.Answer{
			Type: a.Type, Refused: a.Refused, Value: a.Value, Choice: a.Choice, Abstain: a.Abstain,
		}}
		if q, ok := p.Question(name); ok && q.Type == engine.Score {
			lean.Probabilities = a.Probabilities
		}
		out[name] = lean
	}
	return out
}

func probabilityPtr(v float64) *float64 { return &v }

// TestLeanRouteParityCoversTheRankingSelection guards the parity fixture:
// every question the ranking reads must be present, so a new role cannot
// slip into the table's blind spot.
func TestLeanRouteParityCoversTheRankingSelection(t *testing.T) {
	p := defaultProfile(t)
	full := fullParityAnswers(p)
	for _, key := range neededQuestions(p) {
		if _, ok := full[key.Name]; !ok {
			t.Fatalf("the parity fixture lacks ranking question %q", key.Name)
		}
	}
	if len(full) != len(p.Questions) {
		t.Fatalf("the parity fixture has %d answers, want every one of the profile's %d", len(full), len(p.Questions))
	}
}

// TestRowSignalsDeriveTheActionFacts is the contract guard for the derived
// facts one row carries. signalsFor derives bulk, wantsReply and the entry
// booleans once from the thresholded signals; the action classifier's own
// view is derived at the point of use (actionSignals(sig)), so this table
// proves the retained facts stay equal to the canonical derivations across
// every answer state the ranking handles, including the bulk overrides.
func TestRowSignalsDeriveTheActionFacts(t *testing.T) {
	p := defaultProfile(t)
	base := func() map[string]route.Answer { return fullParityAnswers(p) }
	set := func(name string, a route.Answer) func(map[string]route.Answer) {
		return func(all map[string]route.Answer) { all[name] = a }
	}
	del := func(name string) func(map[string]route.Answer) {
		return func(all map[string]route.Answer) { delete(all, name) }
	}

	cases := []struct {
		name   string
		mutate func(map[string]route.Answer)
	}{
		{"complete", nil},
		{"reply in a bulk category", set("category", choiceAns("category", "marketing"))},
		{"suspicion overrides the bulk veto", func(all map[string]route.Answer) {
			all["category"] = choiceAns("category", "marketing")
			all["suspicious"] = noul("suspicious", 0.9)
		}},
		{"automated service relationship", set("relationship", choiceAns("relationship", "automated_service"))},
		{"bulk category under an automated relationship", func(all map[string]route.Answer) {
			all["category"] = choiceAns("category", "notifications")
			all["relationship"] = choiceAns("relationship", "automated_service")
		}},
		{"fyi dominates the reply answer", set("asks_for", choiceAns("asks_for", "nothing_fyi"))},
		{"low-confidence reply", set("reply_needed", noul("reply_needed", 0.2))},
		{"missing reply", del("reply_needed")},
		{"abstained reply", set("reply_needed", notCertified(engine.Noul))},
		{"below-target reply", set("reply_needed", route.Answer{Answer: engine.Answer{Type: engine.Noul, Abstain: engine.AbstainBelowTarget}})},
		{"refused reply", set("reply_needed", route.Answer{Answer: engine.Answer{Type: engine.Noul, Refused: true}})},
		{"type-mismatched reply", set("reply_needed", score("reply_needed", 1))},
		{"non-finite reply", set("reply_needed", noul("reply_needed", math.NaN()))},
		{"out-of-range reply", set("reply_needed", noul("reply_needed", 2))},
		{"spam", set("category", choiceAns("category", "spam"))},
		{"notifications", set("category", choiceAns("category", "notifications"))},
		{"missing category", del("category")},
		{"missing asks_for", del("asks_for")},
	}

	sawBulk, sawWanted := false, false
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routeAnswers := base()
			if tc.mutate != nil {
				tc.mutate(routeAnswers)
			}
			rs, err := signalsFor(p, routeAnswers, nil)
			if err != nil {
				t.Fatal(err)
			}
			if want := action.Bulk(actionSignals(rs.sig)); rs.bulk != want {
				t.Fatalf("bulk %v, want action.Bulk(actionSignals(sig)) %v", rs.bulk, want)
			}
			if want := replyOwed(rs.sig) && !action.Bulk(actionSignals(rs.sig)); rs.wantsReply != want {
				t.Fatalf("wantsReply %v, want %v", rs.wantsReply, want)
			}
			if want := wantsResponse(rs.sig, rs.bulk); rs.wantsReply != want {
				t.Fatalf("wantsReply %v, want wantsResponse(sig, bulk) %v", rs.wantsReply, want)
			}
			sawBulk = sawBulk || rs.bulk
			sawWanted = sawWanted || rs.wantsReply
			if rs.question != (rs.sig.IsQuestion == 1) || rs.cta != (rs.sig.CallToAction == 1) {
				t.Fatalf("question/cta %v/%v disagree with signals", rs.question, rs.cta)
			}
		})
	}
	if !sawBulk || !sawWanted {
		t.Fatalf("the table must exercise both a bulk row (%v) and a wanted reply (%v)", sawBulk, sawWanted)
	}
}

// TestSignalsSubjectMatchesFromAnswers proves the ranking's subject is the
// same fact the detail screen derives: signals() now builds it from the
// thresholded values it already extracted, not by reading the answers again.
func TestSignalsSubjectMatchesFromAnswers(t *testing.T) {
	p := defaultProfile(t)
	keys := neededQuestions(p)
	base := func() map[string]route.Answer { return fullParityAnswers(p) }
	set := func(name string, a route.Answer) func(map[string]route.Answer) {
		return func(all map[string]route.Answer) { all[name] = a }
	}
	cases := []struct {
		name   string
		mutate func(map[string]route.Answer)
	}{
		{"complete", nil},
		{"billing by category", func(all map[string]route.Answer) {
			all["topic"] = choiceAns("topic", "billing_account")
			all["category"] = choiceAns("category", "finance")
			all["asks_for"] = choiceAns("asks_for", "nothing_fyi")
			all["decision_needed"] = noul("decision_needed", 0.1)
		}},
		{"decision rule", func(all map[string]route.Answer) {
			all["topic"] = choiceAns("topic", "meeting_scheduling")
			all["asks_for"] = choiceAns("asks_for", "nothing_fyi")
		}},
		{"reply rule", func(all map[string]route.Answer) {
			all["topic"] = choiceAns("topic", "support_issue")
			all["asks_for"] = choiceAns("asks_for", "nothing_fyi")
			all["decision_needed"] = noul("decision_needed", 0.1)
		}},
		{"fyi newsletter", func(all map[string]route.Answer) {
			all["topic"] = choiceAns("topic", "news_updates")
			all["asks_for"] = choiceAns("asks_for", "nothing_fyi")
			all["decision_needed"] = noul("decision_needed", 0.1)
			all["reply_needed"] = noul("reply_needed", 0.1)
		}},
		{"missing topic", func(all map[string]route.Answer) { delete(all, "topic") }},
		{"abstained topic", set("topic", notCertified(engine.Choice))},
		{"refused topic", set("topic", route.Answer{Answer: engine.Answer{Type: engine.Choice, Refused: true}})},
		{"type-mismatched topic", set("topic", score("topic", 1))},
		{"unknown topic", set("topic", choiceAns("topic", "legal_review"))},
		{"normalized topic", set("topic", choiceAns("topic", " Project_Work "))},
		{"missing asks_for", func(all map[string]route.Answer) { delete(all, "asks_for") }},
		{"refused category", set("category", route.Answer{Answer: engine.Answer{Type: engine.Choice, Refused: true}})},
		{"low-confidence reply", set("reply_needed", noul("reply_needed", 0.2))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routeAnswers := base()
			if tc.mutate != nil {
				tc.mutate(routeAnswers)
			}
			answers, _, err := answersFor(p, keys, routeAnswers, nil)
			if err != nil {
				t.Fatal(err)
			}
			sig := signals(p, answers)
			if want := subject.FromAnswers(p, answers); !reflect.DeepEqual(sig.Subject, want) {
				t.Fatalf("signals subject %+v, want FromAnswers %+v", sig.Subject, want)
			}
		})
	}
}

// TestSignalsSubjectFollowsTheNoulThresholdAtTheRangeEdge pins the one edge
// where signals() and subject.FromAnswers intentionally differ: FromAnswers
// accepts a Noul above 1 as yes, answerOf rejects it, and the ranking follows
// its thresholded signals.
func TestSignalsSubjectFollowsTheNoulThresholdAtTheRangeEdge(t *testing.T) {
	p := defaultProfile(t)
	routeAnswers := fullParityAnswers(p)
	routeAnswers["topic"] = choiceAns("topic", "support_issue")
	routeAnswers["asks_for"] = choiceAns("asks_for", "nothing_fyi")
	routeAnswers["decision_needed"] = noul("decision_needed", 0.1)
	routeAnswers["reply_needed"] = noul("reply_needed", 2)
	answers, _, err := answersFor(p, neededQuestions(p), routeAnswers, nil)
	if err != nil {
		t.Fatal(err)
	}
	sig := signals(p, answers)
	fromAnswers := subject.FromAnswers(p, answers)
	if sig.ReplyNeeded != 0 {
		t.Fatalf("an out-of-range Noul produced reply %d", sig.ReplyNeeded)
	}
	if fromAnswers.Basis != "reply" || fromAnswers.Subtopic != "troubleshooting" {
		t.Fatalf("FromAnswers no longer accepts the raw Noul: %+v", fromAnswers)
	}
	if sig.Subject.Basis != "default" || sig.Subject.Subtopic != "incident" {
		t.Fatalf("signals did not follow its thresholded answer: %+v", sig.Subject)
	}
}

// TestScratchAnswerMapDoesNotLeakBetweenRows pins the build's reused merge
// map: deriving a sparse row after a complete one must equal a fresh decode,
// and a preview flag from an earlier row must not survive the clear.
func TestScratchAnswerMapDoesNotLeakBetweenRows(t *testing.T) {
	p := defaultProfile(t)
	keys := neededQuestions(p)
	complete := fullParityAnswers(p)
	sparse := map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}
	previewRow := fullParityAnswers(p)
	previewRow["reply_needed"] = notCertified(engine.Noul)
	preview := map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}

	cases := []struct {
		name    string
		route   map[string]route.Answer
		preview map[string]engine.Answer
	}{
		{"complete", complete, nil},
		{"sparse after complete", sparse, nil},
		{"previewed after sparse", previewRow, preview},
		{"sparse after preview", sparse, nil},
	}
	scratch := make(map[string]engine.Answer, len(keys))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := signalsForKeysInto(p, keys, tc.route, tc.preview, scratch)
			if err != nil {
				t.Fatal(err)
			}
			want, err := signalsFor(p, tc.route, tc.preview)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("reused scratch map differs from a fresh decode:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}
