package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

type messageBackfillRequest struct {
	Source string `json:"source"`
}

// handleMessages is the gated owner all-mail page. The store owns the
// visibility boundary, ordering, cursor and counts; the handler only reads
// the HTTP query and maps the typed validation errors.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	q, err := messageListQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.services.Messages.List(r.Context(), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, normalizeMessageListView(view))
}

// messageListQuery validates the request against the store's one page
// contract. A cursor that parses but belongs to another role, store or sender
// is rejected by the store, not here, so the check has one owner.
func messageListQuery(r *http.Request) (store.MessageListQuery, error) {
	var out store.MessageListQuery
	query := r.URL.Query()

	role, _, err := queryRole(r, RoleGrammar{AllowAll: true, AllowEmpty: true})
	if err != nil {
		return out, err
	}
	out.Role = role

	if key, present, err := queryStore(r); err != nil {
		return out, err
	} else if present {
		out.Store = key
	}

	sender, _, err := querySender(r)
	if err != nil {
		return out, err
	}
	out.Sender = sender

	limit, err := boundedQueryInt(r, "limit", 0, 1, store.MessageListMaxLimit)
	if err != nil {
		return out, err
	}
	out.Limit = limit

	// The mail cursor is a store.Cursor: it carries the keyset position and
	// the role/store/sender/read state it belongs to, and the store owns its
	// encoding and validation, so the handler parses it here. The
	// conversations cursor is a different object (it binds the normalized
	// query and state, which only that service knows), so that service parses
	// its own. Both answer the one invalidCursor shape below, and the split is
	// deliberate: one owner per cursor type.
	if values, present := query["before"]; present {
		if len(values) == 0 || values[0] == "" {
			return out, invalidCursor()
		}
		cursor, err := store.ParseCursor(values[0])
		if err != nil {
			return out, invalidCursor()
		}
		out.Before = cursor
	}

	if value, present, ok := queryBoolValue(r, "counts"); present {
		if !ok {
			return out, invalidBool("counts")
		}
		out.Counts = value
	}

	// unread is one boolean filter: true/1 keeps only unread mail, false/0 is
	// the same as absent (every read state, never a read-only page).
	if value, present, ok := queryBoolValue(r, "unread"); present {
		if !ok {
			return out, invalidBool("unread")
		}
		out.Unread = value
	}
	return out, nil
}

// normalizeMessageListView owns the always-present messages array of the
// wire shape: an empty mailbox is [], never null.
func normalizeMessageListView(view MessageListView) MessageListView {
	view.Messages = nonNilSlice(view.Messages)
	return view
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// A message is readable before the gate only to label it, and only when
	// the label queue currently offers it: the screen must show the mail it
	// asks the user to label, and a bare ?for=label must not unlock any
	// message id. The queue query is repeated from the screen (question,
	// effective limit and review mode), so membership is judged against the
	// exact page it is showing. Every other read is answer-side and stays
	// behind the one learning gate; a failed binding falls back to that same
	// gate error, so the caller learns nothing extra.
	if err := s.services.Learning.Allow(r.Context()); err != nil {
		// Only the learning lock opens a pre-gate surface. Any other Allow
		// failure is an ordinary server error and must not be mistaken for a
		// lock that a query flag can bypass.
		var locked *learning.LockedError
		if !errors.As(err, &locked) {
			s.writeError(w, r, err)
			return
		}
		switch r.URL.Query().Get("for") {
		case "label":
			// The binding is judged against the exact page the queue route
			// would serve, including its limit bound: an unbounded or
			// malformed limit must not reach the queue owner. Any binding
			// failure still answers the gate error, so the caller learns
			// nothing beyond "not readable yet".
			query, qerr := queryLabel(r)
			if qerr != nil || query.Question == "" {
				s.writeError(w, r, err)
				return
			}
			ok, qerr := s.services.Labels.LabelQueueContains(r.Context(), query, id)
			// A failed proof of membership is the lock error, not the
			// binding's own error: the caller learns only that the message
			// is not readable yet.
			if qerr != nil || !ok {
				s.writeError(w, r, err)
				return
			}
		case "answer":
			// The Answers screen may read the body of a message that has a
			// stored answer while the gate is closed. The answer itself is
			// the binding: a bare flag opens nothing.
			page, aerr := s.services.Hub.Answers(r.Context(), hub.AnswerQuery{MessageID: id, Limit: 1})
			if aerr != nil || len(page.Items) == 0 || page.Items[0].MessageID != id {
				s.writeError(w, r, err)
				return
			}
		default:
			s.writeError(w, r, err)
			return
		}
	}
	view, err := s.services.Messages.Get(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view.ProjectBody()
	s.writeJSON(w, r, http.StatusOK, view)
}

// handleMessageAttachment serves one stored inline image. The URL is under
// the ordinary bearer API: the shell fetches the bytes with its token and
// turns them into an ephemeral blob URL inside the message iframe, so the
// global token never appears in an <img> URL and no asset is guessable.
// maxAttachmentPosition bounds the attachment index a path may name. A
// message with more parts than this is not a real one; the bound keeps a
// hostile path from making the store scan an arbitrary index.
const maxAttachmentPosition = 10000

func (s *Server) handleMessageAttachment(w http.ResponseWriter, r *http.Request) {
	rowID, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	position, err := strconv.Atoi(r.PathValue("position"))
	if err != nil || position < 0 || position > maxAttachmentPosition {
		s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_attachment", Message: "invalid attachment position"})
		return
	}
	asset, err := s.services.Messages.Attachment(r.Context(), rowID, position)
	if errors.Is(err, store.ErrAssetUnavailable) {
		s.writeError(w, r, &APIError{Status: http.StatusNotFound, Code: "asset_unavailable", Message: "attachment content is not stored"})
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	imageType := mail.InlineImageType(asset.ContentType, asset.Name)
	if imageType == "" {
		s.writeError(w, r, &APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_asset", Message: "attachment is not an inline image"})
		return
	}
	if v := r.URL.Query().Get("v"); v != "" && v != asset.SHA256 {
		s.writeError(w, r, notFound())
		return
	}
	w.Header().Set("Content-Type", imageType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Length", strconv.Itoa(len(asset.Bytes)))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if asset.SHA256 != "" {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(asset.Bytes)
}

// handleMessageBackfill starts the owner-triggered one-time pass that
// re-reads display HTML for messages stored before it was captured. It is
// a data-capture job like a sync (no message content is returned), so it is
// not behind the learning gate; the job is visible and cancelable through
// the ordinary /api/jobs endpoints.
func (s *Server) handleMessageBackfill(w http.ResponseWriter, r *http.Request) {
	var req messageBackfillRequest
	if err := s.decodeOptional(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	info, err := s.services.Messages.StartBackfill(r.Context(), req.Source)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
}
