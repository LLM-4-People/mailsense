package triage

import (
	"cmp"
	"context"
	"encoding/json"

	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// yearChars is the year prefix of a stored timestamp (mail.TimeLayout).
const yearChars = 4

// ExportGold writes the gold-judge sample of the whole chosen mailbox
// (package goldexport): the received owner-scope mail of every triaged
// folder in the model view (no history, no label examples), without any
// provider call.
func (t *Triager) ExportGold(ctx context.Context, st *store.Store, o goldexport.Options) (*goldexport.Report, error) {
	if err := o.Check(); err != nil {
		return nil, err
	}
	c, err := t.core(ctx)
	if err != nil {
		return nil, err
	}
	snap := t.cfg.Snapshot()
	legacy, err := goldexport.LegacyCategories(o.Legacy, snap.Int(SettingArchiveMaxLineBytes))
	if err != nil {
		return nil, err
	}
	orgs, err := ownerOrgs(ctx, st, c.set.Domains)
	if err != nil {
		return nil, err
	}
	strata, msgs := goldexport.NewStrata(), map[int64]mail.Message{}
	err = st.EachMessage(ctx, triagedFilter(c.roles), func(r *store.MessageRow) error {
		if r.Reason == scope.ReasonSentByOwner {
			return nil
		}
		m := r.Message
		strata.Add(goldexport.Key{Script: cmp.Or(mail.Scripts(mail.CleanBody(m.Subject+"\n"+m.Body)).Dominant(), "none"),
			Category: cmp.Or(legacy.Categories[m.ID], "unknown"), SenderKind: goldexport.SenderKind(&m, orgs, c.set.Domains),
			Year: cmp.Or(mail.Head(m.ReceivedAt, yearChars), "undated")}, r.RowID)
		msgs[r.RowID] = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	rep, err := strata.Report(o)
	if err != nil {
		return nil, err
	}
	rep.LegacyBadLines = legacy.BadLines
	if legacy.BadLines > 0 {
		t.log.Warn("legacy classification lines not read", "lines", legacy.BadLines)
	}
	files, err := goldFiles(c, goldexport.Draw(rep, o), msgs, rep)
	if err != nil {
		return nil, err
	}
	return rep, goldexport.Write(o.Dir, files, snap.Replace(nil))
}

// goldFiles renders the export's files.
func goldFiles(c *core, rows []int64, msgs map[int64]mail.Message, rep *goldexport.Report) (map[string][]byte, error) {
	tax, err := json.MarshalIndent(goldexport.Taxonomy{Fingerprint: c.profile.Fingerprint, Questions: c.profile.Questions}, "", "  ")
	if err != nil {
		return nil, err
	}
	view := modelView(c.composer)
	var lines []byte
	for _, row := range rows {
		m := msgs[row]
		b, err := json.Marshal(goldexport.Input{ID: goldexport.ID(&m), Text: view.State(row, &m)})
		if err != nil {
			return nil, err
		}
		lines = append(append(lines, b...), '\n')
	}
	strata, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string][]byte{goldexport.TaxonomyFile: append(tax, '\n'), goldexport.InputFile: lines,
		goldexport.StrataFile: append(strata, '\n')}, nil
}

// modelView is the composer of the providers' states without the history
// and label-example blocks: the header, the observed facts and the body.
func modelView(c *Composer) *Composer {
	p := *c.profile
	p.IncludeHistory, p.IncludeExamples = false, false
	v := *c
	v.profile = &p
	return &v
}

// ownerOrgs are the organisations of the picked mailboxes' owners.
func ownerOrgs(ctx context.Context, st *store.Store, domains mail.Domains) (map[string]bool, error) {
	owners, err := st.Owners(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, o := range owners {
		for _, a := range o.Identity.Addresses {
			if d := domains.Of(a); d != "" {
				out[d] = true
			}
		}
	}
	return out, nil
}
