package api

import (
	"errors"
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

type secretValueRequest struct {
	Value string `json:"value"`
}

// secretName is the one secret-name check of the write and delete routes.
func (s *Server) secretName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if err := secrets.ValidName(name); err != nil {
		s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_name", Message: "invalid secret name"})
		return "", false
	}
	return name, true
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	view, err := s.services.Secrets.List()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view.Names = nonNilSlice(view.Names)
	view.Expected = nonNilSlice(view.Expected)
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleSecretPut(w http.ResponseWriter, r *http.Request) {
	name, ok := s.secretName(w, r)
	if !ok {
		return
	}
	var req secretValueRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Value == "" {
		s.writeError(w, r, &APIError{Status: http.StatusUnprocessableEntity, Code: "empty_value", Message: "the secret value is empty"})
		return
	}
	if err := s.services.Secrets.Set(name, req.Value); err != nil {
		s.writeError(w, r, s.secretError(name, err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	name, ok := s.secretName(w, r)
	if !ok {
		return
	}
	if err := s.services.Secrets.Delete(name); err != nil {
		s.writeError(w, r, s.secretError(name, err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// secretError adds the environment remedy for a read-only store. It
// never includes the value.
func (s *Server) secretError(name string, err error) error {
	if !errors.Is(err, secrets.ErrReadOnly) {
		return err
	}
	return &APIError{Status: http.StatusForbidden, Code: "read_only",
		Message: "the secret store is read-only; set " + config.EnvSecretPrefix + name + " in the environment"}
}
