package qa

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// topicRankingPlan is the one computed lane that uses retrieval: a ranking of
// people filtered by a subject. The plan's Text field carries the topic; Kind
// and Limit keep the typed contract. A company ranking stays on the lexical
// executor, which owns domain aggregation and the company book; this lane
// groups by sender address and would otherwise label a person as a company.
func topicRankingPlan(plan mailquery.Plan) bool {
	if plan.Kind != mailquery.KindContacts {
		return false
	}
	return plan.Action == mailquery.ActionRank && strings.TrimSpace(plan.Text) != ""
}

// computedPlanAllowed keeps a topic ranking out of the content-question guard
// only when the question is a listing of several people or companies. "what
// did the clients say" remains a content question and reaches the planner's
// written answer, not a contact table.
func computedPlanAllowed(question string, plan mailquery.Plan) bool {
	// A contacts or companies ranking filtered by a topic is an aggregate
	// listing even when it starts with "who" or "which". Contacts take the
	// semantic lane; companies keep the lexical executor and its company
	// aggregation.
	if (plan.Kind == mailquery.KindContacts || plan.Kind == mailquery.KindCompanies) &&
		plan.Action == mailquery.ActionRank && strings.TrimSpace(plan.Text) != "" &&
		listingEntityQuestion(question) {
		return true
	}
	return !asksForMessageContent(question) && !summaryRequest(question)
}

// executeTopicRanking answers a people/company topic list locally: retrieval
// finds the matching owner mail, the hits are grouped by the person who wrote
// them, and the rows are ranked by how many matching messages each one sent.
// The model never writes the list and nothing is invented; one representative
// message per contact is cited. A sent-only request stays on the lexical
// executor because a sent message's counterpart is its recipient, which the
// search hit does not carry.
func (s *Service) executeTopicRanking(ctx context.Context, view AskView, key, language string, plan mailquery.Plan) (AskView, error) {
	resolved, _, err := plan.Resolve(s.clock.Now(), s.zone())
	if err != nil {
		return AskView{}, err
	}
	searchLimit := s.cfg.Int("search.max_hits")
	if searchLimit < 1 {
		searchLimit = 200
	}
	retrievalCtx, cancel := s.retrievalContext(ctx)
	defer cancel()
	res, err := s.search.Search(retrievalCtx, search.Query{
		Text:   strings.TrimSpace(plan.Text),
		Filter: store.SearchFilter{OwnerOnly: true, Roles: []mail.FolderRole{mail.RoleInbox, mail.RoleArchive, mail.RoleOther}},
		Limit:  searchLimit,
		Wait:   true,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(retrievalCtx.Err(), context.DeadlineExceeded) {
			err = errSearchTimeout
		} else {
			return AskView{}, err
		}
	}
	if err == nil {
		err = retrievalProblemOf(res.Semantic)
	}
	if err != nil {
		// The one notice mapping decides whether this is a timeout or a
		// warming index; the topic lane used to report every one of them as
		// a timeout and to miss a partial vector set entirely.
		notice, reason, ok := retrievalNotice(err)
		if !ok {
			return AskView{}, err
		}
		s.log.Warn("qa topic retrieval degraded", "notice", notice, "reason", reason)
		return retrieval(view, notice, nil, "", ""), nil
	}
	rows, citations, total := topicRows(res.Hits, plan.Limit)
	result := mailquery.Result{Kind: plan.Kind, Action: mailquery.ActionRank, Metric: mailquery.MetricMessages,
		Rows: rows, Total: total, Since: resolved.Since, Until: resolved.Until,
		Capped: res.Capped || len(res.Hits) >= searchLimit}
	view.Mode, view.Intent, view.Answer, view.Notice = ModeComputed, string(plan.Kind), "", ""
	view.Plan, view.Result, view.Citations = &resolved, &result, citations
	view.Counts = map[string]int{}
	if plan.Kind == mailquery.KindCompanies {
		view.Counts["companies"] = len(rows)
	} else {
		view.Counts["contacts"] = len(rows)
	}
	view.Counts["messages"] = total
	return view, nil
}

// topicGroup is one counterpart aggregated from the retrieved hits. rep is the
// newest matching message, used as that contact's citation.
type topicGroup struct {
	label    string
	address  string
	messages int
	last     string
	rep      search.Hit
}

// topicRows groups received hits by exact sender address, ranks them by
// matching message count, then by recency, name and address so the table is
// stable between identical requests. Sent hits are skipped: the owner is not a
// contact of the owner.
func topicRows(hits []search.Hit, limit int) ([]mailquery.Row, []Citation, int) {
	if limit < 1 {
		limit = 15
	}
	byAddress := make(map[string]*topicGroup, len(hits))
	for _, hit := range hits {
		if hit.Role == mail.RoleSent {
			continue
		}
		address := mail.NormalizeAddress(hit.SenderAddress)
		if address == "" {
			continue
		}
		group := byAddress[address]
		if group == nil {
			group = &topicGroup{label: strings.TrimSpace(hit.SenderName), address: address, last: hit.Instant, rep: hit}
			byAddress[address] = group
		}
		group.messages++
		if hit.Instant > group.last {
			group.last, group.rep = hit.Instant, hit
		}
		if group.label == "" {
			group.label = strings.TrimSpace(hit.SenderName)
		}
	}
	groups := make([]*topicGroup, 0, len(byAddress))
	total := 0
	for _, group := range byAddress {
		groups = append(groups, group)
		total += group.messages
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.messages != b.messages {
			return a.messages > b.messages
		}
		if a.last != b.last {
			return a.last > b.last
		}
		if la, lb := strings.ToLower(a.label), strings.ToLower(b.label); la != lb {
			return la < lb
		}
		return a.address < b.address
	})
	if len(groups) > limit {
		groups = groups[:limit]
	}
	rows := make([]mailquery.Row, 0, len(groups))
	citations := make([]Citation, 0, len(groups))
	for _, group := range groups {
		label := group.label
		if label == "" {
			label = group.address
		}
		rows = append(rows, mailquery.Row{Key: group.address, Label: label, Address: group.address,
			Messages: group.messages, Received: group.messages, LastAt: group.last,
			MessageID: group.rep.RowID, Subject: group.rep.Subject, SenderName: label, Instant: group.rep.Instant})
		citations = append(citations, Citation{MessageID: group.rep.RowID, Subject: group.rep.Subject,
			SenderName: label, ReceivedAt: group.rep.Instant, Reason: "topic"})
	}
	return rows, citations, total
}
