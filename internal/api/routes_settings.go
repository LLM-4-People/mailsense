package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
)

type settingsView struct {
	Settings       []SettingView `json:"settings"`
	Total          int           `json:"total"`
	RestartPending []string      `json:"restart_pending"`
}

type valueRequest struct {
	Value json.RawMessage `json:"value"`
}

// decodeSettingValue reads the one write body of the settings routes. The
// value key must be present and not null: both the explicit write and the
// learned-decision routes require it, and a second copy of that rule is how
// one route starts accepting an empty value.
func (s *Server) decodeSettingValue(r *http.Request, key string) (json.RawMessage, error) {
	if _, err := s.requireSetting(key); err != nil {
		return nil, err
	}
	var req valueRequest
	if err := s.decodeJSON(r, &req); err != nil {
		return nil, err
	}
	if len(req.Value) == 0 || bytes.Equal(bytes.TrimSpace(req.Value), []byte("null")) {
		return nil, invalidValueRequired()
	}
	return req.Value, nil
}

// requireSetting is the one settings-existence rule of the detail, write and
// reset routes: an unknown key is the same 404 problem everywhere. It returns
// the view so the detail route does not read the setting a second time.
func (s *Server) requireSetting(key string) (SettingView, error) {
	view, ok := s.services.Settings.Get(key)
	if !ok {
		return SettingView{}, ErrUnknownSetting()
	}
	return view, nil
}

// invalidValueRequired is the missing/null value shape: the body parsed as
// JSON, so this is a request-field error, not invalid JSON.
func invalidValueRequired() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_request",
		Message: "value is required", Params: map[string]string{"name": "value"}}
}

type restartPendingView struct {
	RestartPending []string `json:"restart_pending"`
}

type learnedView struct {
	Learned []config.Learned `json:"learned"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	settings, pending := s.services.Settings.List()
	settings = nonNilSlice(settings)
	// total is the whole schema, never the projection, so a client can say
	// how many settings it has not read without a second request.
	total := len(settings)
	if keys := settingsKeys(r.URL.Query().Get("keys")); len(keys) > 0 {
		known := make(map[string]bool, len(settings))
		for _, view := range settings {
			known[view.Key] = true
		}
		wanted := make(map[string]bool, len(keys))
		for _, key := range keys {
			if !known[key] {
				s.writeError(w, r, ErrInvalidSettingKeys(key))
				return
			}
			wanted[key] = true
		}
		projected := make([]SettingView, 0, len(keys))
		// The service returns the schema order; filtering preserves it instead
		// of following the request's order.
		for _, view := range settings {
			if wanted[view.Key] {
				projected = append(projected, view)
			}
		}
		settings = projected
	}
	pending = nonNilSlice(pending)
	s.writeJSON(w, r, http.StatusOK, settingsView{Settings: settings, Total: total, RestartPending: pending})
}

// settingsKeys parses the optional keys projection: comma-separated dotted
// keys, trimmed and de-duplicated in request order. It returns nil when the
// parameter is absent or names no key, which is the full-list request.
func settingsKeys(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		key := strings.TrimSpace(part)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) handleSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	view, err := s.requireSetting(key)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleSettingPut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	value, err := s.decodeSettingValue(r, key)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.services.Settings.Set(key, value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleSettingDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, err := s.requireSetting(key); err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.services.Settings.Reset(key)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleSettingsReload(w http.ResponseWriter, r *http.Request) {
	if err := s.decodeOptional(r, &struct{}{}); err != nil {
		s.writeError(w, r, err)
		return
	}
	pending, err := s.services.Settings.Reload()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, restartPendingView{RestartPending: nonNilSlice(pending)})
}

func (s *Server) handleLearned(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, learnedView{Learned: nonNilSlice(s.services.Settings.Learned())})
}

func (s *Server) handleLearnedReject(w http.ResponseWriter, r *http.Request) {
	s.learnedDecision(w, r, true)
}

func (s *Server) handleLearnedUnreject(w http.ResponseWriter, r *http.Request) {
	s.learnedDecision(w, r, false)
}

func (s *Server) learnedDecision(w http.ResponseWriter, r *http.Request, reject bool) {
	key := r.PathValue("key")
	value, err := s.decodeSettingValue(r, key)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var learned []config.Learned
	if reject {
		learned, err = s.services.Settings.Reject(key, value)
	} else {
		learned, err = s.services.Settings.Unreject(key, value)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, learnedView{Learned: nonNilSlice(learned)})
}
