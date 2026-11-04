package qa

import (
	"errors"
	"testing"
)

func TestGroundAnswerRejectsMixedInvalidMarkers(t *testing.T) {
	msgs := []contextMessage{{ID: 1}, {ID: 2}}
	if answer, citations := groundAnswer("First [1]. Second [99].", msgs, nil); answer != "" || len(citations) != 0 {
		t.Fatalf("an answer with one invalid marker was grounded: %q %+v", answer, citations)
	}
	if answer, citations := groundAnswer("First [999999999999999999999].", msgs, nil); answer != "" || len(citations) != 0 {
		t.Fatalf("an overflowing marker was grounded: %q %+v", answer, citations)
	}
	answer, citations := groundAnswer("First [2]. Second [1]. [2]", msgs, nil)
	if answer != "First [1]. Second [2]. [1]" {
		t.Fatalf("valid markers were not renumbered: %q", answer)
	}
	if len(citations) != 2 {
		t.Fatalf("citations = %d, want 2", len(citations))
	}
}

// A reply whose fenced object fails the contract is JSON, not prose: the
// [n] inside its own "used" array must not ground it as an answer.
func TestFencedJSONIsNeverShownAsProse(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	w.gen.content = "```json\n{\"route\":\"answer\",\"answer\":\"Claim [1]\",\"used\":[1],}\n```"
	view, err := w.svc.Ask(t.Context(), "What did the fenced plan claim about the invoice?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeGenerated {
		t.Fatalf("malformed fenced JSON was shown as prose: %+v", view)
	}
	if view.Notice != NoticeGenerationFailed {
		t.Fatalf("notice = %q, want %q", view.Notice, NoticeGenerationFailed)
	}
}

// A fenced plain-text answer is still usable, with the fence removed.
func TestFencedProseIsGroundedWithoutFences(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	w.gen.content = "```\nThe invoice was approved [1].\n```"
	view, err := w.svc.Ask(t.Context(), "What does the fenced prose say about the invoice?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Answer != "The invoice was approved [1]." {
		t.Fatalf("fenced prose was not grounded cleanly: %+v", view)
	}
	if len(view.Citations) != 1 {
		t.Fatalf("citations = %d, want 1", len(view.Citations))
	}
}

// TestGroundAnswerSplitsGroupedMarkers: models often group their sources as
// "[1, 2]". The UI chips only a single-index marker, so an un-split bracket
// stayed literal prose and its second source never reached the citation list.
func TestGroundAnswerSplitsGroupedMarkers(t *testing.T) {
	msgs := []contextMessage{{ID: 1}, {ID: 2}, {ID: 3}}
	cases := []struct {
		name     string
		answer   string
		want     string
		citation int
	}{
		{"comma list", "Approved [1, 2].", "Approved [1][2].", 2},
		{"semicolon list", "Approved [1; 3].", "Approved [1][2].", 2},
		{"spaces and reversal", "Approved [2 , 1].", "Approved [1][2].", 2},
		{"repeated index", "Approved [1, 1].", "Approved [1].", 1},
		{"single marker unchanged", "Approved [3].", "Approved [1].", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, citations := groundAnswer(tc.answer, msgs, nil)
			if answer != tc.want {
				t.Fatalf("answer = %q, want %q", answer, tc.want)
			}
			if len(citations) != tc.citation {
				t.Fatalf("citations = %d, want %d", len(citations), tc.citation)
			}
		})
	}
	// One invalid index in a grouped marker refuses the whole answer, exactly
	// as a lone invalid marker does: the claim cannot be separated from it.
	if answer, citations := groundAnswer("Approved [1, 9].", msgs, nil); answer != "" || len(citations) != 0 {
		t.Fatalf("a grouped marker outside the context was grounded: %q %+v", answer, citations)
	}
	// A range is not a marker the prompts ask for; it stays prose rather than
	// inventing sources the model did not cite.
	if answer, _ := groundAnswer("Approved [1-3].", msgs, nil); answer != "Approved [1-3]." {
		t.Fatalf("a range was rewritten: %q", answer)
	}
}

// TestRetrievalNoticeIsOneMapping: every planner-context caller must classify
// a retrieval failure the same way. A timeout is a warm index that missed one
// request's budget, never "the index is warming"; an unrelated error is not a
// retrieval notice at all.
func TestRetrievalNoticeIsOneMapping(t *testing.T) {
	cases := []struct {
		err    error
		notice string
		reason string
		ok     bool
	}{
		{errSearchTimeout, NoticeRetrievalTimeout, "retrieval_timeout", true},
		{errSearchWarming, NoticeIndexWarming, "semantic_index_warming", true},
		{errors.New("other"), "", "", false},
		{nil, "", "", false},
	}
	for _, tc := range cases {
		notice, reason, ok := retrievalNotice(tc.err)
		if notice != tc.notice || reason != tc.reason || ok != tc.ok {
			t.Fatalf("retrievalNotice(%v) = %q, %q, %v; want %q, %q, %v",
				tc.err, notice, reason, ok, tc.notice, tc.reason, tc.ok)
		}
	}
}
