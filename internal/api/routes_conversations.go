package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// handleConversations is the gated thread-first mailbox page. The service
// owns search, counts, state resolution and the keyset cursor; the handler
// validates the HTTP query and maps the service's typed errors. The cursor is
// read by the service, not here, because it binds the normalized query and
// state only that service knows (the mail cursor is a store.Cursor and is
// parsed by its own owner); both still answer the one invalidCursor shape.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedQueryInt(r, "limit", 0, conversations.MinLimit, conversations.MaxLimit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	before := query.Get("before")
	if values, present := query["before"]; present && (len(values) == 0 || values[0] == "") {
		s.writeError(w, r, invalidCursor())
		return
	}
	q := conversations.Query{
		Limit:  limit,
		Before: before,
		State:  threadintel.State(query.Get("state")),
		Q:      query.Get("q"),
	}
	page, err := s.services.Conversations.Page(r.Context(), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, conversationPageView(page))
}

// conversationPageView maps the service page to the wire shape. It owns the
// always-present arrays, the six state keys and the organization omission.
func conversationPageView(page conversations.Page) ConversationsView {
	counts := threadintel.StateCounts(page.Counts)
	rows := make([]ConversationView, 0, len(page.Conversations))
	for _, row := range page.Conversations {
		participants := make([]ThreadParticipantView, 0, len(row.Participants))
		for _, p := range row.Participants {
			participants = append(participants, ThreadParticipantViewOf(p))
		}
		var organization *ConversationOrganizationView
		if row.Organization != nil {
			organization = &ConversationOrganizationView{
				Key: row.Organization.Key, Label: row.Organization.Label, Declared: row.Organization.Declared,
			}
		}
		state := row.State
		rows = append(rows, ConversationView{
			ConversationID:   row.ConversationID,
			Subject:          row.Subject,
			SenderName:       row.SenderName,
			SenderAddress:    row.SenderAddress,
			LastMessageID:    row.LastMessageID,
			LastAt:           row.LastAt,
			FirstAt:          row.FirstAt,
			Messages:         row.Messages,
			Sent:             row.Sent,
			Received:         row.Received,
			Unread:           row.Unread,
			LastSent:         row.LastSent,
			LastIsRead:       row.LastIsRead,
			HasAttachments:   row.HasAttachments,
			Snippet:          row.Snippet,
			DueDate:          row.DueDate,
			Participants:     participants,
			ParticipantCount: row.ParticipantCount,
			Organization:     organization,
			State:            ThreadStateViewOf(state),
			StateSource:      row.StateSource,
		})
	}
	return ConversationsView{
		GeneratedAt:   page.GeneratedAt,
		Conversations: rows,
		Total:         page.Total,
		Counts:        counts,
		NextBefore:    page.NextBefore,
		HasMore:       page.HasMore,
		Preview:       page.Preview,
		Coverage: ConversationCoverageView{
			Messages:      page.Coverage.Messages,
			Conversations: page.Coverage.Conversations,
			Unthreaded:    page.Coverage.Unthreaded,
		},
	}
}
