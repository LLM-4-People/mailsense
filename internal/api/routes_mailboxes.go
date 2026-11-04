package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/store"
)

type mailboxList struct {
	Mailboxes []store.MailboxView `json:"mailboxes"`
}

type discoverRequest struct {
	Source string `json:"source"`
}

type keysRequest struct {
	Keys []string `json:"keys"`
}

// maxMailboxKeys bounds one selection request. The screen picks one store per
// action; a body naming more stores than any profile lists is a malformed
// request, not a longer scan of the discovered set.
const maxMailboxKeys = 256

type declareRequest struct {
	Key     string `json:"key"`
	Address string `json:"address"`
}

func (s *Server) handleMailboxes(w http.ResponseWriter, r *http.Request) {
	// One response for every mailbox-list route: a discovery or an
	// undeclare returns exactly what a plain list returns.
	s.writeMailboxes(w, r)
}

func (s *Server) handleMailboxDiscover(w http.ResponseWriter, r *http.Request) {
	var req discoverRequest
	if err := s.decodeOptional(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	boxes, err := s.services.Mailboxes.Discover(r.Context(), req.Source)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, mailboxList{Mailboxes: nonNilSlice(boxes)})
}

func (s *Server) handleMailboxPick(w http.ResponseWriter, r *http.Request) {
	s.mailboxKeys(w, r, true)
}

func (s *Server) handleMailboxUnpick(w http.ResponseWriter, r *http.Request) {
	s.mailboxKeys(w, r, false)
}

func (s *Server) mailboxKeys(w http.ResponseWriter, r *http.Request, pick bool) {
	var req keysRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(req.Keys) == 0 {
		s.writeError(w, r, invalidRequest("keys is required"))
		return
	}
	if len(req.Keys) > maxMailboxKeys {
		s.writeError(w, r, invalidRequest("keys holds more entries than any profile lists"))
		return
	}
	for _, key := range req.Keys {
		if key == "" {
			s.writeError(w, r, invalidRequest("a mailbox key is empty"))
			return
		}
	}
	var boxes []store.MailboxView
	var err error
	if pick {
		boxes, err = s.services.Mailboxes.Pick(r.Context(), req.Keys)
	} else {
		boxes, err = s.services.Mailboxes.Unpick(r.Context(), req.Keys)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeMailboxList(w, r, boxes)
}

func (s *Server) handleMailboxDeclare(w http.ResponseWriter, r *http.Request) {
	s.mailboxIdentity(w, r, true)
}

func (s *Server) handleMailboxUndeclare(w http.ResponseWriter, r *http.Request) {
	s.mailboxIdentity(w, r, false)
}

func (s *Server) mailboxIdentity(w http.ResponseWriter, r *http.Request, declare bool) {
	var req declareRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Key == "" || req.Address == "" {
		s.writeError(w, r, invalidRequest("key and address are required"))
		return
	}
	var boxes []store.MailboxView
	var err error
	if declare {
		boxes, err = s.services.Mailboxes.Declare(r.Context(), req.Key, req.Address)
	} else {
		boxes, err = s.services.Mailboxes.Undeclare(r.Context(), req.Key, req.Address)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeMailboxList(w, r, boxes)
}

func (s *Server) writeMailboxes(w http.ResponseWriter, r *http.Request) {
	boxes, err := s.services.Mailboxes.List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeMailboxList(w, r, boxes)
}

// writeMailboxList is the one mailbox response shape. A write route hands it
// the views its service already returned instead of listing the mailboxes a
// second time.
func (s *Server) writeMailboxList(w http.ResponseWriter, r *http.Request, boxes []store.MailboxView) {
	s.writeJSON(w, r, http.StatusOK, mailboxList{Mailboxes: nonNilSlice(boxes)})
}
