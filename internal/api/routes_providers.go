package api

import (
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

type providersView struct {
	Providers []ProviderInfo         `json:"providers"`
	Roles     []ProviderRoleInfo     `json:"roles"`
	Protocols []ProviderProtocolInfo `json:"protocols"`
	// Fields describes the provider record: its fields, bounds, enums and
	// kinds. The editor renders it instead of keeping its own copy.
	Fields []engine.ProviderField `json:"fields"`
}

// ProviderProtocolInfo is one protocol the create form may choose. The
// static protocol table in engine is the owner; the view never invents one.
type ProviderProtocolInfo struct {
	Name string      `json:"name"`
	Kind engine.Kind `json:"kind"`
}

// providerRequest is the API write body: a complete record, with the id
// in the body for create and in the path for update.
type providerRequest = engine.Provider

func providerPathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !engine.ProviderIDValid(id) {
		return "", InvalidProviderID(true)
	}
	return id, nil
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.services.Providers.List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	roles, err := s.services.Providers.Roles(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	list = nonNilSlice(list)
	roles = nonNilSlice(roles)
	protocols := engine.Protocols()
	protocolInfos := make([]ProviderProtocolInfo, 0, len(protocols))
	for _, protocol := range protocols {
		protocolInfos = append(protocolInfos, ProviderProtocolInfo{Name: protocol.Name, Kind: protocol.Kind})
	}
	s.writeJSON(w, r, http.StatusOK, providersView{Providers: list, Roles: roles, Protocols: protocolInfos, Fields: engine.ProviderFields()})
}

func (s *Server) handleProviderCreate(w http.ResponseWriter, r *http.Request) {
	var req providerRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	info, err := s.services.Providers.Create(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, info)
}

func (s *Server) handleProviderUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := providerPathID(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req providerRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	info, err := s.services.Providers.Update(r.Context(), id, req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, info)
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	id, err := providerPathID(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.services.Providers.Delete(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	id, err := providerPathID(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.decodeOptional(r, &struct{}{}); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.services.Providers.Test(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, result)
}

// handleProviderPath dispatches the provider subtree: the literal
// discover endpoint, one record id, or one record's test action. A wrong
// method on a known shape stays a JSON 405; an unknown shape is 404.
func (s *Server) handleProviderPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/providers/")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "discover":
		if r.Method != http.MethodPost {
			s.methodNotAllowed("POST").ServeHTTP(w, r)
			return
		}
		s.handleProviderDiscover(w, r)
	case len(parts) == 1 && parts[0] != "":
		r.SetPathValue("id", parts[0])
		switch r.Method {
		case http.MethodPut:
			s.handleProviderUpdate(w, r)
		case http.MethodDelete:
			s.handleProviderDelete(w, r)
		default:
			s.methodNotAllowed("PUT, DELETE").ServeHTTP(w, r)
		}
	case len(parts) == 2 && parts[0] != "" && parts[1] == "test":
		if r.Method != http.MethodPost {
			s.methodNotAllowed("POST").ServeHTTP(w, r)
			return
		}
		r.SetPathValue("id", parts[0])
		s.handleProviderTest(w, r)
	default:
		s.apiNotFound(w, r)
	}
}

// handleProviderDiscover fetches the provider's official model list. The
// handler validates the URL boundary before the service resolves a key or
// makes any request; the engine repeats the same normalization.
func (s *Server) handleProviderDiscover(w http.ResponseWriter, r *http.Request) {
	var req ProviderDiscoverRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	req, err := engine.NormalizeDiscoverRequest(req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	models, err := s.services.Providers.Discover(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	models = nonNilSlice(models)
	s.writeJSON(w, r, http.StatusOK, ProviderDiscoveryView{Models: models})
}
