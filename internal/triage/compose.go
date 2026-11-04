package triage

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// stateHashChars is the legacy state-hash length (hex digits).
const stateHashChars = 16

// StateHash is the legacy state hash: SHA-256 of the state, 16 hex digits.
func StateHash(state string) string { return digest.Hex([]byte(state), stateHashChars) }

// Composer builds decision states (classify.StateComposer). A state is
// CAUSAL: every context block (sender, domain and overall standing, reply
// examples, label examples) comes only from data dated strictly before the
// message's own instant (mail.Instant), bucketed, without the message
// itself — so later mail, replies, labels and the time.zone setting can
// never change an existing state.
type Composer struct {
	profile *taxonomy.Profile
	rules   *Rules
	history *threads.History
	labels  []feedback.Label
	norm    *mail.SubjectNormalizer
	domains mail.Domains
	scales  scales
	// evidence are the evidence facts by row (triage.facts.evidence; nil
	// when off): causal and bucketed like every block of the state.
	evidence map[int64]string
}

// State renders the state of a message (row id rowID).
func (c *Composer) State(rowID int64, m *mail.Message) string {
	p := c.profile
	body := mail.Head(m.Body, p.BodyChars) // the one truncation of the state (PORTING §10)
	v := map[string]string{"sender": c.sender(m), "sender_name": m.SenderName, "sender_address": m.SenderAddress,
		"received_at": m.ReceivedAt, "subject": m.Subject, "body_preview": mail.Strip(body), "folder": string(m.FolderRole),
		"to_addresses": addresses(m, mail.To), "cc_addresses": addresses(m, mail.Cc)}
	if p.UsesField("observed_facts") && p.IncludeFacts {
		own := mail.Head(mail.CleanBody(m.Body), p.BodyChars) // facts describe the message's own text
		v["observed_facts"] = measure(m, own, c.norm, c.rules).render(c.rules, c.evidence != nil)
		if e := c.evidence[rowID]; e != "" {
			v["observed_facts"] += "\n" + e
		}
	}
	if at, dated := m.Instant(); dated {
		if p.UsesField("history_context") && p.IncludeHistory {
			v["history_context"] = c.historyAt(m.SenderAddress, at)
		}
		if p.UsesField("examples") && p.IncludeExamples {
			v["examples"] = c.examples(rowID, m, at)
		}
	}
	out := p.Template().Render(v)
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return mail.Strip(out)
}

func (c *Composer) sender(m *mail.Message) string {
	switch {
	case m.SenderName != "" && m.SenderAddress != "":
		return m.SenderName + " <" + m.SenderAddress + ">"
	case m.SenderName != "":
		return m.SenderName
	case m.SenderAddress != "":
		return m.SenderAddress
	}
	return c.rules.Phrases["unknown_sender"]
}

func addresses(m *mail.Message, kind mail.RecipientKind) string {
	var out []string
	for _, r := range m.Recipients {
		if r.Kind == kind && r.Address != "" {
			out = append(out, r.Address)
		}
	}
	return strings.Join(out, ", ")
}

// historyAt renders the standing toward the sender (else its domain) and
// overall, as it was at the message's instant (history.context_for, in bands).
func (c *Composer) historyAt(sender string, at time.Time) string {
	var lines []string
	if pt, ok := c.history.Sender(sender, at); ok {
		lines = append(lines, c.standingLine("history_sender", pt))
	} else if pt, ok := c.history.Domain(sender, at); ok {
		lines = append(lines, c.standingLine("history_domain", pt))
	}
	if pt, ok := c.history.Overall(at); ok {
		lines = append(lines, c.standingLine("history_overall", pt))
	}
	if len(lines) == 0 {
		return ""
	}
	return c.rules.phrase("history", map[string]string{"lines": strings.Join(lines, " ")})
}

func (c *Composer) standingLine(name string, pt threads.Point) string {
	speed := ""
	if pt.Bucket >= 0 {
		speed = c.rules.phrase("history_speed", map[string]string{"speed": c.scales.speedLabels[c.scales.coarse(pt.Bucket, c.scales.speed)]})
	}
	return c.rules.phrase(name, map[string]string{"reply": c.rules.Reply.Label(mathx.Ratio(pt.Replied, pt.Received)),
		"volume": c.rules.Volume.Label(float64(pt.Received)), "speed": speed})
}

// examples renders the owner's replies to the sender and the labels of
// the sender's (or domain's) other mail, all from before the message.
// SelectExamples applies feedback.Teaching, so a judge answer never reaches
// the prompt.
func (c *Composer) examples(rowID int64, m *mail.Message, at time.Time) string {
	var lines []string
	pt, _ := c.history.Sender(m.SenderAddress, at)
	for _, ex := range pt.Examples {
		if ex.MessageID == rowID {
			continue
		}
		line := c.rules.phrase("example_reply", map[string]string{"minutes": strconv.Itoa(mathx.RoundInt(ex.RespondedMinutes)), "subject": ex.Subject})
		if p := mail.Strip(ex.ReplyPreview); p != "" {
			line += c.rules.phrase("example_reply_preview", map[string]string{"preview": p})
		}
		lines = append(lines, line)
	}
	for _, l := range feedback.SelectExamples(c.labels, rowID, m.SenderAddress, m.Subject, at, c.profile.MaxExamples, c.domains) {
		lines = append(lines, c.rules.phrase("example_label", map[string]string{"sender": l.Sender, "subject": l.Subject,
			"question": l.Question, "value": labelText(l)}))
	}
	if len(lines) == 0 {
		return ""
	}
	return c.rules.phrase("examples", map[string]string{"lines": strings.Join(lines, "\n")})
}

func labelText(l feedback.Label) string {
	if l.Type == engine.Choice {
		return l.Value.Choice
	}
	return fmt.Sprintf("%.2f", mathx.Round(l.Value.Number, 2))
}
