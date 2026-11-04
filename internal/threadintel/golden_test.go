package threadintel

// The golden corpus pins the resolver's full output: every field of
// Assessment, the state, the actor, the owner-facing projection, the
// topic/subtopic path, the reply target and the visibility. The optimized
// resolver (parsed-evidence memo and cache) must reproduce it byte for byte.
//
// Regenerate after an intentional behavior change:
//
//	go test ./internal/threadintel -run '^TestResolveGolden$' -update-golden
//
// A change to the golden file is a change to thread truth; review it as
// such, never as a test fixup.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden_assessments.json")

// goldenCase is one named thread and the options it resolves under.
type goldenCase struct {
	Name    string
	Thread  []Message
	Options Options
}

// goldenEntry is the recorded result: the full Assessment in Go syntax so
// every field, including a zero one, is part of the comparison.
type goldenEntry struct {
	Name       string  `json:"name"`
	Options    Options `json:"options"`
	Assessment string  `json:"assessment"`
}

func goldenAt(minute int) time.Time {
	return time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC).Add(time.Duration(minute) * time.Minute)
}

func goldenInbound(id int64, minute int, body string) Message {
	return Message{RowID: id, ConversationID: "C-golden", Subject: "Golden", SenderName: "Client",
		SenderAddress: "client@example.test", Role: mail.RoleInbox, Verdict: scope.Owner,
		Reason: scope.ReasonRecipientMatch, AddressedToMe: mail.AddressedTo, At: goldenAt(minute), Body: body}
}

func goldenOwner(id int64, minute int, body string) Message {
	return Message{RowID: id, ConversationID: "C-golden", Subject: "Golden", SenderName: "Owner",
		SenderAddress: "owner@example.test", Role: mail.RoleSent, Verdict: scope.Owner,
		Reason: scope.ReasonSentByOwner, At: goldenAt(minute), Body: body, OwnerFacing: true}
}

func goldenOther(id int64, minute int, body string) Message {
	return Message{RowID: id, ConversationID: "C-golden", Subject: "Golden", SenderName: "Colleague",
		SenderAddress: "colleague@example.test", Role: mail.RoleSent, Verdict: scope.Shared,
		Reason: scope.ReasonSentByOther, At: goldenAt(minute), Body: body}
}

// goldenCorpus is deterministic: byte slices, no map iteration, fixed times.
// It covers every state and reason, the parsed-evidence fast and slow paths,
// protocol-data kinds, identity/role combinations, ordering ties and the
// resolution options.
func goldenCorpus() []goldenCase {
	ask := "Could you review the attached plan by Friday?"
	long := "This is a substantive sentence about the plan. "
	cases := []goldenCase{
		{Name: "empty", Thread: nil},
		{Name: "single_ask", Thread: []Message{goldenInbound(1, 0, ask)}, Options: Options{}},
		{Name: "inbound_ask_signal", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, ask)
			m.ReplyNeeded, m.IsQuestion, m.AsksFor = true, true, "answer_question"
			return m
		}()}},
		{Name: "inbound_cc_only", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, "FYI")
			m.AddressedToMe = mail.AddressedCc
			return m
		}()}},
		{Name: "inbound_cc_with_reply_needed", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, ask)
			m.AddressedToMe = mail.AddressedCc
			m.ReplyNeeded = true
			return m
		}()}},
		{Name: "automated_inbound_ask", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, ask)
			m.Automated, m.ReplyNeeded, m.AsksFor = true, true, "answer_question"
			return m
		}()}},
		{Name: "informational_question", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, "Did you request this?")
			m.IsQuestion, m.AsksFor = true, "nothing_fyi"
			return m
		}()}},
		{Name: "visibility_gap_many_to", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, ask)
			m.ManyTo, m.ReplyNeeded = true, true
			return m
		}()}, Options: Options{Visibility: Partial}},
		{Name: "visibility_gap_single_to", Thread: []Message{func() Message {
			m := goldenInbound(1, 0, ask)
			m.ManyTo, m.ReplyNeeded = false, true
			return m
		}()}, Options: Options{Visibility: Partial}},
		{Name: "owner_asks_other", Thread: []Message{func() Message {
			m := goldenOwner(2, 1, "Please send the signed deck.")
			m.AsksFor = "information_document"
			return m
		}()}},
		{Name: "owner_asks_other_mixed_case", Thread: []Message{func() Message {
			m := goldenOwner(2, 1, "Please send the signed deck.")
			m.AsksFor = "  Approval_Decision  "
			return m
		}()}},
		{Name: "owner_closing_note", Thread: []Message{func() Message {
			m := goldenOwner(2, 1, "Thanks, all set.")
			m.IsQuestion, m.AsksFor = true, "nothing_fyi"
			return m
		}()}},
		{Name: "owner_reply_headers", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Done, the plan is attached.")
				m.InReplyTo = "<m1@example.test>"
				return m
			}(),
		}},
		{Name: "owner_reply_structural", Thread: []Message{
			func() Message {
				m := goldenInbound(1, 0, ask)
				m.ReplyNeeded = true
				return m
			}(),
			goldenOwner(2, 1, "Done."),
		}},
		{Name: "owner_reply_last_verb", Thread: []Message{
			func() Message {
				m := goldenInbound(1, 0, ask)
				m.LastVerbExecuted = 102
				return m
			}(),
			goldenOwner(2, 1, "Done."),
		}},
		{Name: "other_reply", Thread: []Message{
			goldenInbound(1, 0, ask),
			goldenOther(2, 1, "I'll take care of it."),
		}},
		{Name: "other_owner_shared_store", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOther(2, 1, "I'll take care of it.")
				m.Verdict, m.Reason, m.OtherOwner = scope.Owner, scope.ReasonSentByOwner, true
				return m
			}(),
		}},
		{Name: "sent_role_without_verdict", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOther(2, 1, "I'll take care of it.")
				m.Verdict, m.Reason = "", ""
				return m
			}(),
		}},
		{Name: "auto_reply_only", Thread: []Message{
			func() Message {
				m := goldenOwner(2, 1, "I am away until Monday.")
				m.Headers = "Auto-Submitted: auto-replied\n"
				return m
			}(),
		}},
		{Name: "auto_reply_then_ask", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "I am away until Monday.")
				m.Headers = "Auto-Submitted: auto-replied\n"
				return m
			}(),
		}},
		{Name: "x_autoreply_header", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Away.")
				m.Headers = "X-Autoreply: yes\n"
				return m
			}(),
		}},
		{Name: "x_autorespond_header", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Away.")
				m.Headers = "X-Autorespond: yes\n"
				return m
			}(),
		}},
		{Name: "outlook_oof_template", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Away.")
				m.MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
				return m
			}(),
		}},
		{Name: "read_receipt_class", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "")
				m.MessageClass = "IPM.Note.ReadReceipt"
				return m
			}(),
		}},
		{Name: "delivery_report_header", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Delivered.")
				m.Headers = "Content-Type: multipart/report; report-type=delivery-status\n"
				return m
			}(),
		}},
		{Name: "external_answer_closes_wait", Thread: []Message{
			func() Message {
				m := goldenOwner(1, 0, "Could you confirm the delivery date?")
				m.AsksFor = "answer_question"
				return m
			}(),
			goldenInbound(2, 1, "Yes, the delivery is confirmed for Friday."),
		}},
		{Name: "external_notice_after_owner_close", Thread: []Message{
			goldenOwner(1, 0, "Thanks, all set."),
			goldenInbound(2, 1, "Our office is closed on Monday."),
		}},
		{Name: "owner_answer_then_new_inbound", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Done.")
				m.InReplyTo = "<m1@example.test>"
				return m
			}(),
			goldenInbound(3, 2, "One more question: can we meet Tuesday?"),
		}},
		{Name: "topic_fallback", Thread: []Message{
			func() Message {
				m := goldenInbound(1, 0, ask)
				m.Topic, m.Subtopic = "project", "plan"
				return m
			}(),
			func() Message {
				m := goldenOther(2, 1, "I'll take care of it.")
				m.Topic, m.Subtopic = "", ""
				return m
			}(),
		}},
		{Name: "owner_facing_projection", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOther(2, 1, "I'll take care of it.")
				m.OwnerFacing = true
				return m
			}(),
		}},
		{Name: "owner_actor_projection", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Done.")
				m.InReplyTo = "<m1@example.test>"
				return m
			}(),
		}},
		{Name: "tie_orders_by_row_id", Thread: []Message{
			func() Message {
				m := goldenOther(9, 1, "Second.")
				return m
			}(),
			func() Message {
				m := goldenOwner(3, 1, "First.")
				m.AsksFor = "task_work"
				return m
			}(),
		}},
		{Name: "unsorted_input", Thread: []Message{
			goldenOther(3, 2, "I have it."),
			goldenInbound(1, 0, ask),
			goldenOwner(2, 1, "Working on it."),
		}},
		{Name: "empty_body_no_attachment", Thread: []Message{
			func() Message {
				m := goldenInbound(2, 1, "")
				return m
			}(),
		}},
		{Name: "all_trivial_with_auto_reply", Thread: []Message{
			func() Message {
				m := goldenInbound(1, 0, "Thanks")
				return m
			}(),
			func() Message {
				m := goldenOwner(2, 1, "+1")
				m.Headers = "Auto-Submitted: auto-generated\n"
				return m
			}(),
		}},
		{Name: "all_trivial_no_auto_reply", Thread: []Message{
			func() Message {
				m := goldenInbound(1, 0, "ok")
				return m
			}(),
		}},
		{Name: "attachment_trivial_cover", Thread: []Message{
			goldenInbound(1, 0, ask),
			func() Message {
				m := goldenOwner(2, 1, "Thanks!")
				m.HasAttachments = true
				m.InReplyTo = "<m1@example.test>"
				return m
			}(),
		}},
	}
	bodies := []string{
		"", "   ", "ok", "OK", "Thanks", "THANK YOU", "thank you", "bien recu", "bien reçu",
		"received", "recu", "reçu", "noted", "+1", "👍", "ｔｈａｎｋｓ", "Thanks!",
		long, "  " + long + "  ",
		"é", "日本語", "ａｂｃ", "café", "cafe\u0301",
		"short note",
	}
	for i, body := range bodies {
		cases = append(cases, goldenCase{
			Name:   fmt.Sprintf("body_%02d", i),
			Thread: []Message{goldenInbound(int64(i+1), 0, body)},
		})
	}
	for _, min := range []int{0, 1, 2, 5, 50, 64, 65, 100} {
		cases = append(cases, goldenCase{
			Name:    fmt.Sprintf("min_body_runes_%d", min),
			Thread:  []Message{goldenInbound(1, 0, long+long)},
			Options: Options{MinBodyRunes: min},
		})
	}
	classes := []string{
		"", "IPM.Note", "IPM.Note.Rules.OofTemplate.Microsoft", "IPM.Note.ReadReceipt",
		"REPORT.IPM.Note.NDR", "IPM.Schedule.Meeting.Request", "IPM.Schedule.Meeting.Resp.Pos",
		"IPM.Note.Rules.AutoReply.Microsoft", "IPM.Note.Recall",
	}
	for i, class := range classes {
		cases = append(cases, goldenCase{
			Name: fmt.Sprintf("class_%02d", i),
			Thread: []Message{
				goldenInbound(1, 0, ask),
				func() Message {
					m := goldenOwner(2, 1, "Noted.")
					m.MessageClass = class
					return m
				}(),
			},
		})
	}
	headers := []string{
		"", "X-Autoreply: yes", "X-Autorespond: yes", "Auto-Submitted: auto-replied",
		"Auto-Submitted: auto-generated", "Content-Type: multipart/report; report-type=feedback-report",
		"Content-Type: text/calendar; method=REQUEST", "Subject: folded\nX-Autoreply: yes",
		"X-Other: 1\nX-Autoreply: yes\nX-Other: 2",
	}
	for i, header := range headers {
		cases = append(cases, goldenCase{
			Name: fmt.Sprintf("header_%02d", i),
			Thread: []Message{
				goldenInbound(1, 0, ask),
				func() Message {
					m := goldenOwner(2, 1, "Noted.")
					m.Headers = header
					return m
				}(),
			},
		})
	}
	for _, vis := range []Visibility{Complete, Partial, UnknownVisibility} {
		for _, state := range []string{"no_messages", "inbound", "owner", "other"} {
			var thread []Message
			switch state {
			case "inbound":
				m := goldenInbound(1, 0, ask)
				m.ReplyNeeded = true
				thread = []Message{m}
			case "owner":
				thread = []Message{goldenOwner(1, 0, "Please send it.")}
				thread[0].AsksFor = "information_document"
			case "other":
				thread = []Message{goldenInbound(1, 0, ask), goldenOther(2, 1, "Done.")}
			}
			cases = append(cases, goldenCase{
				Name:    fmt.Sprintf("visibility_%s_%s", vis, state),
				Thread:  thread,
				Options: Options{Visibility: vis},
			})
		}
	}
	return cases
}

func TestResolveGolden(t *testing.T) {
	path := filepath.Join("testdata", "golden_assessments.json")
	cases := goldenCorpus()
	got := make([]goldenEntry, len(cases))
	for i, c := range cases {
		got[i] = goldenEntry{
			Name:       c.Name,
			Options:    c.Options,
			Assessment: fmt.Sprintf("%#v", Resolve(c.Thread, c.Options)),
		}
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d golden assessments", len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden): %v", err)
	}
	if string(want) != string(data) {
		for i := range got {
			var w []goldenEntry
			if err := json.Unmarshal(want, &w); err != nil {
				t.Fatal(err)
			}
			if i < len(w) && w[i] != got[i] {
				t.Fatalf("assessment %d (%s) changed:\n got %s\nwant %s", i, got[i].Name, got[i].Assessment, w[i].Assessment)
			}
		}
		t.Fatalf("golden set differs in size or ordering: got %d entries", len(got))
	}
}
