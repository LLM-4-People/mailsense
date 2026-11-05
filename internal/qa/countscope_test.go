package qa

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// countScopeWorld is the count-scope regression fixture: a configured
// generator whose freeform answer is grounded in one seeded message, plus the
// mailbox-wide urgent total the leak used to answer a scoped count from.
func countScopeWorld(t *testing.T) *testWorld {
	t.Helper()
	w := newWorld(t, false)
	planFallback(w)
	w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
	w.prio.totals[priority.FilterUrgent] = 7
	w.prio.totals[priority.FilterAll] = 9
	return w
}

// TestScopedCountWithAGeneratorNeverAnswersMailboxWide pins the confirmed
// leak: with a generator configured, a count that names a person, a company or
// another unlisted scope must not fall through to the structured branch and
// answer the mailbox-wide priority totals. The wording alone decides; the
// configured generator answers through the ordinary freeform path instead.
func TestScopedCountWithAGeneratorNeverAnswersMailboxWide(t *testing.T) {
	cases := []struct {
		name     string
		question string
		language string
	}{
		{"person scope", "Number of urgent messages from Bob?", "en"},
		{"company scope", "Amount of deadlines Acme has?", "en"},
		{"French person scope", "Combien de messages urgents de Bob ?", "fr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := countScopeWorld(t)

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Route != RouteAuto || view.Mode != ModeGenerated || view.Intent != IntentFreeform {
				t.Fatalf("scoped count answered as route %q mode %q intent %q; want auto/generated/freeform",
					view.Route, view.Mode, view.Intent)
			}
			if len(view.Counts) != 0 {
				t.Fatalf("a scoped count answered mailbox-wide counts %v", view.Counts)
			}
			if w.prio.calls != 0 {
				t.Fatalf("a scoped count read the priority ranking %d times (askStructured ran)", w.prio.calls)
			}
			if w.prio.topicsCalls != 0 {
				t.Fatalf("a scoped count read the topic census %d times", w.prio.topicsCalls)
			}
			if w.gen.calls != 1 || w.search.calls != 1 {
				t.Fatalf("generator=%d search=%d, want 1/1", w.gen.calls, w.search.calls)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
				t.Fatalf("citations = %+v, want the retrieved message 1", view.Citations)
			}
		})
	}
}

// TestAcceptedCountWithAGeneratorStaysStructured pins the other half of the
// gate: an accepted plain count (one priority category, or one catalogue
// topic) is answered deterministically with no provider call even when a
// generator is configured, on Auto and on Local.
func TestAcceptedCountWithAGeneratorStaysStructured(t *testing.T) {
	t.Run("auto priority category", func(t *testing.T) {
		w := countScopeWorld(t)

		view, err := w.svc.Ask(t.Context(), "How many urgent messages?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Route != RouteAuto || view.Mode != ModeStructured || view.Intent != IntentUrgent {
			t.Fatalf("view = route %q mode %q intent %q; want auto/structured/urgent",
				view.Route, view.Mode, view.Intent)
		}
		if got := view.Counts["urgent"]; got != 7 {
			t.Fatalf("counts[urgent] = %d, want the seeded 7 (counts %v)", got, view.Counts)
		}
		if w.gen.calls != 0 {
			t.Fatalf("an accepted count called the generator %d times", w.gen.calls)
		}
		if len(view.Citations) != 1 || view.Citations[0].MessageID != 42 {
			t.Fatalf("citations = %+v, want the seeded urgent message 42", view.Citations)
		}
	})

	t.Run("auto topic census", func(t *testing.T) {
		w := countScopeWorld(t)
		w.prio.topics = topicTestCensus()
		w.store.summaries[9] = store.Summary{RowID: 9, Subject: "subject", SenderName: "sender",
			SenderAddress: "sender@example.com", Instant: "2026-10-25T10:00:00Z"}

		view, err := w.svc.Ask(t.Context(), "How many messages about invoices?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Route != RouteAuto || view.Mode != ModeStructured || view.Intent != IntentTopic {
			t.Fatalf("view = route %q mode %q intent %q; want auto/structured/topic",
				view.Route, view.Mode, view.Intent)
		}
		if view.Topic == nil || view.Topic.Topic != "billing_account" || view.Topic.Subtopic != "invoice" ||
			view.Topic.Count != 2 {
			t.Fatalf("topic = %+v, want billing_account/invoice count 2", view.Topic)
		}
		if len(view.Counts) != 0 {
			t.Fatalf("a topic answer carried mailbox counts %v; counts stays empty for topic", view.Counts)
		}
		if w.gen.calls != 0 {
			t.Fatalf("an accepted topic count called the generator %d times", w.gen.calls)
		}
		if w.prio.topicsCalls != 1 {
			t.Fatalf("census reads = %d, want 1", w.prio.topicsCalls)
		}
	})

	t.Run("local topic census", func(t *testing.T) {
		w := countScopeWorld(t)
		w.prio.topics = topicTestCensus()
		w.store.summaries[9] = store.Summary{RowID: 9, Subject: "subject", SenderName: "sender",
			SenderAddress: "sender@example.com", Instant: "2026-10-25T10:00:00Z"}

		view, err := w.svc.AskConversation(t.Context(), AskRequest{
			Question: "Combien de factures ?", Language: "fr", Route: RouteLocal,
		})
		if err != nil {
			t.Fatal(err)
		}
		if view.Route != RouteLocal || view.Mode != ModeStructured || view.Intent != IntentTopic {
			t.Fatalf("view = route %q mode %q intent %q; want local/structured/topic",
				view.Route, view.Mode, view.Intent)
		}
		if view.Topic == nil || view.Topic.Topic != "billing_account" || view.Topic.Subtopic != "invoice" ||
			view.Topic.Count != 2 {
			t.Fatalf("topic = %+v, want billing_account/invoice count 2", view.Topic)
		}
		if w.gen.calls != 0 {
			t.Fatalf("an accepted Local topic count called the generator %d times", w.gen.calls)
		}
	})
}

// TestLocalScopedCountStaysFreeformWithoutCounts pins the Local route arm of
// the gate: a scoped count never reaches answerLocalStructured's mailbox-wide
// totals; it follows answerLocalFreeform and returns the retrieved sources
// with the local notice and no counts.
func TestLocalScopedCountStaysFreeformWithoutCounts(t *testing.T) {
	w := countScopeWorld(t)

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "Number of urgent messages from Bob?", Language: "en", Route: RouteLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Route != RouteLocal || view.Mode != ModeRetrieval || view.Intent != IntentFreeform ||
		view.Notice != NoticeLocalNoAnswer {
		t.Fatalf("view = route %q mode %q intent %q notice %q; want local/retrieval/freeform/%q",
			view.Route, view.Mode, view.Intent, view.Notice, NoticeLocalNoAnswer)
	}
	if len(view.Counts) != 0 {
		t.Fatalf("a Local scoped count produced counts %v", view.Counts)
	}
	if w.prio.calls != 0 {
		t.Fatalf("a Local scoped count read the priority ranking %d times (answerLocalStructured ran)", w.prio.calls)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Local called the generator %d times", w.gen.calls)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("citations = %+v, want the retrieved message 1", view.Citations)
	}
}

// TestRemoteScopedCountFollowsTheFreeformPath pins the Remote route arm: a
// scoped count is answered through the freeform retrieval path with the
// configured generator, never through the structured branch that fed the
// mailbox-wide priority counts into the prompt.
func TestRemoteScopedCountFollowsTheFreeformPath(t *testing.T) {
	w := countScopeWorld(t)

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "Number of urgent messages from Bob?", Language: "en", Route: RouteRemote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Route != RouteRemote || view.Mode != ModeGenerated || view.Intent != IntentFreeform {
		t.Fatalf("view = route %q mode %q intent %q; want remote/generated/freeform",
			view.Route, view.Mode, view.Intent)
	}
	if len(view.Counts) != 0 {
		t.Fatalf("a Remote scoped count produced mailbox-wide counts %v", view.Counts)
	}
	if w.prio.calls != 0 {
		t.Fatalf("a Remote scoped count read the priority ranking %d times", w.prio.calls)
	}
	if w.gen.calls != 1 || w.search.calls != 1 {
		t.Fatalf("generator=%d search=%d, want 1/1", w.gen.calls, w.search.calls)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("citations = %+v, want the retrieved message 1", view.Citations)
	}
}

// TestCountScopeDecisionFollowsWordingNotGeneratorAvailability pins the
// discriminator itself: the same accepted plain count answers structured and
// the same scoped count stays freeform whether or not a generator is
// configured. Generator availability may change how a rejected count is
// answered (a written answer with one, retrieval without), but it never
// changes whether the mailbox-wide totals may answer.
func TestCountScopeDecisionFollowsWordingNotGeneratorAvailability(t *testing.T) {
	for _, withGen := range []bool{true, false} {
		name := "without generator"
		if withGen {
			name = "with generator"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("accepted count is structured", func(t *testing.T) {
				w := countScopeWorld(t)
				if !withGen {
					w.svc.source = &fakeSource{err: ErrUnavailable}
				}

				view, err := w.svc.Ask(t.Context(), "How many urgent messages?", "en")
				if err != nil {
					t.Fatal(err)
				}
				if view.Mode != ModeStructured || view.Intent != IntentUrgent || view.Counts["urgent"] != 7 {
					t.Fatalf("accepted count = %+v, want structured/urgent count 7", view)
				}
				if w.gen.calls != 0 {
					t.Fatalf("accepted count called the generator %d times", w.gen.calls)
				}
			})

			t.Run("scoped count is freeform", func(t *testing.T) {
				w := countScopeWorld(t)
				if !withGen {
					w.svc.source = &fakeSource{err: ErrUnavailable}
				}

				view, err := w.svc.Ask(t.Context(), "Number of urgent messages from Bob?", "en")
				if err != nil {
					t.Fatal(err)
				}
				if view.Mode == ModeStructured || view.Mode == ModeComputed || view.Intent != IntentFreeform {
					t.Fatalf("scoped count = %+v, want a freeform non-structured answer", view)
				}
				if len(view.Counts) != 0 {
					t.Fatalf("scoped count produced mailbox-wide counts %v", view.Counts)
				}
				if w.prio.calls != 0 {
					t.Fatalf("scoped count read the priority ranking %d times", w.prio.calls)
				}
				if withGen {
					if view.Mode != ModeGenerated || w.gen.calls != 1 {
						t.Fatalf("with a generator the scoped count = mode %q calls %d, want generated/1",
							view.Mode, w.gen.calls)
					}
				} else if w.gen.calls != 0 {
					t.Fatalf("without a generator the scoped count called it %d times", w.gen.calls)
				}
			})
		})
	}
}

// TestProviderlessCountIntentScopeRows pins the one scope owner directly: a
// plain count of one priority category or one catalogue topic is accepted, and
// person, company, date, compound or cue-less wording is denied. The decision
// is pure wording plus the scope reads; it never reads the ranking, the census
// or the generator.
func TestProviderlessCountIntentScopeRows(t *testing.T) {
	accepted := []struct {
		question string
		intent   string
		topic    string
		subtopic string
	}{
		{"How many urgent messages?", IntentUrgent, "", ""},
		{"Number deadlines", IntentDeadline, "", ""},
		{"Amount of decisions", IntentDecision, "", ""},
		{"How many messages about invoices?", IntentTopic, "billing_account", "invoice"},
	}
	for _, tc := range accepted {
		t.Run("accepted "+tc.question, func(t *testing.T) {
			w := newWorld(t, false)
			w.prio.topics = topicTestCensus()

			intent, phrase, ok := w.svc.providerlessCountIntent(t.Context(), tc.question)
			if !ok {
				t.Fatalf("providerlessCountIntent(%q) was denied", tc.question)
			}
			if tc.intent == IntentTopic {
				if intent != IntentTopic || phrase.Topic != tc.topic || phrase.Subtopic != tc.subtopic {
					t.Fatalf("decision = %q %+v, want topic %s/%s", intent, phrase, tc.topic, tc.subtopic)
				}
			} else {
				if intent != tc.intent || phrase.Topic != "" || phrase.Subtopic != "" {
					t.Fatalf("decision = %q %+v, want structured %q", intent, phrase, tc.intent)
				}
			}
			if w.gen.calls != 0 || w.prio.calls != 0 || w.prio.topicsCalls != 0 {
				t.Fatalf("the scope decision touched generator=%d ranking=%d census=%d",
					w.gen.calls, w.prio.calls, w.prio.topicsCalls)
			}
		})
	}

	denied := []struct {
		name     string
		question string
	}{
		{"person scope", "Number of urgent messages from Bob?"},
		{"company scope", "Amount of deadlines Acme has?"},
		{"date scope", "How many invoices this week?"},
		{"French person scope", "Combien de messages urgents de Bob ?"},
		{"priority count", "How many priority messages?"},
		{"topic plus category", "How many urgent invoices?"},
		{"two topics", "How many invoices and billing messages?"},
		{"no count cue", "Show me invoices"},
	}
	for _, tc := range denied {
		t.Run("denied "+tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.prio.topics = topicTestCensus()

			intent, phrase, ok := w.svc.providerlessCountIntent(t.Context(), tc.question)
			if ok {
				t.Fatalf("providerlessCountIntent(%q) accepted %q %+v", tc.question, intent, phrase)
			}
			if w.gen.calls != 0 || w.prio.calls != 0 {
				t.Fatalf("the denied decision touched generator=%d ranking=%d", w.gen.calls, w.prio.calls)
			}
		})
	}
}
