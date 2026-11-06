package api

import (
	"net/http"
	"strings"
)

// buildHandler registers every API path twice: the method pattern for the
// route and the methodless pattern with a JSON 405. Go's ServeMux prefers
// the method-specific pattern, so only a wrong method reaches the fallback.
func (s *Server) buildHandler() http.Handler {
	s.mux = http.NewServeMux()
	s.get("/api/status", false, s.handleStatus)
	s.get("/api/usage", false, s.handleUsage)
	s.get("/api/setup", false, s.handleSetup)
	s.get("/api/boot", false, s.handleBoot)
	s.get("/api/learning", false, s.handleLearning)
	s.get("/api/gate", false, s.handleGate)
	s.post("/api/learning/zone", false, s.handleZone)
	s.get("/api/hub", false, s.handleHub)
	s.get("/api/answers", false, s.handleAnswers)

	s.get("/api/jobs", false, s.handleJobs)
	s.get("/api/jobs/{id}", false, s.handleJob)
	s.post("/api/jobs/{id}/cancel", false, s.handleJobCancel)

	s.get("/api/held-removals", false, s.handleHeldRemovals)
	s.post("/api/held-removals/{folder}/confirm", true, s.handleHeldRemovalConfirm)

	s.get("/api/mailboxes", false, s.handleMailboxes)
	s.post("/api/mailboxes/discover", true, s.handleMailboxDiscover)
	s.post("/api/mailboxes/pick", false, s.handleMailboxPick)
	s.post("/api/mailboxes/unpick", false, s.handleMailboxUnpick)
	s.post("/api/mailboxes/declare", false, s.handleMailboxDeclare)
	s.post("/api/mailboxes/undeclare", false, s.handleMailboxUndeclare)

	s.post("/api/actions/{action}", false, s.handleAction)
	s.get("/api/sources", false, s.handleSources)
	s.post("/api/sources/{id}/{action}", true, s.handleSourceAction)

	s.get("/api/settings", false, s.handleSettings)
	// The settings subtree is dispatched by hand: a methodless literal
	// ("/api/settings/learned") and a method-specific parameter route
	// ("GET /api/settings/{key}") conflict in Go's ServeMux. One subtree
	// handler keeps the JSON 405 rules without a second router.
	s.mux.Handle("/api/settings/learned", s.onlyMethod("GET", "GET", s.handleLearned))
	s.mux.Handle("/api/settings/reload", s.onlyMethod("POST", "POST", s.handleSettingsReload))
	s.mux.Handle("/api/settings/learned/{key}/reject", s.onlyMethod("POST", "POST", s.handleLearnedReject))
	s.mux.Handle("/api/settings/learned/{key}/unreject", s.onlyMethod("POST", "POST", s.handleLearnedUnreject))
	s.mux.Handle("/api/settings/", s.route(false, s.handleSettingPath))

	s.get("/api/secrets", false, s.handleSecrets)
	s.methods("/api/secrets/{name}", "PUT, DELETE", false, map[string]http.HandlerFunc{
		"PUT":    s.handleSecretPut,
		"DELETE": s.handleSecretDelete,
	})

	s.methods("/api/providers", "GET, POST", true, map[string]http.HandlerFunc{
		"GET":  s.handleProviders,
		"POST": s.handleProviderCreate,
	})
	// The provider subtree is dispatched by hand (like the settings
	// subtree): a literal discover route and method-specific parameter
	// routes cannot coexist in Go's ServeMux without a conflict.
	s.mux.Handle("/api/providers/", s.route(true, s.handleProviderPath))

	s.get("/api/sidecar", false, s.handleSidecar)
	s.post("/api/sidecar/install", false, s.handleSidecarInstall)
	s.post("/api/sidecar/verify", true, s.handleSidecarVerify)

	s.get("/api/logs", false, s.handleLogs)
	s.get("/api/logs/{seq}", false, s.handleLogEntry)
	s.mux.Handle("GET /api/events", http.HandlerFunc(s.handleEvents))
	s.mux.Handle("/api/events", s.methodNotAllowed("GET"))

	s.get("/api/questions", false, s.handleQuestions)
	s.get("/api/labels/summary", false, s.handleLabelSummary)
	s.get("/api/labels/queue", true, s.handleLabelQueue)
	s.post("/api/labels", false, s.handleLabelBatch)
	s.methods("/api/labels/{message_id}/{question}", "DELETE", false, map[string]http.HandlerFunc{
		"DELETE": s.handleLabelDelete,
	})
	// The all-mail list is an exact literal beside the message wildcard;
	// ServeMux prefers the exact path, and both keep their JSON 405
	// fallback.
	s.get("/api/messages", false, s.gated(s.handleMessages))
	s.get("/api/messages/{$}", false, s.handleMessage)
	s.get("/api/messages/{id}", false, s.handleMessage)
	s.get("/api/messages/{id}/attachments/{position}", false, s.gated(s.handleMessageAttachment))

	s.get("/api/triage", true, s.gated(s.handleTriage))
	// The literal is method-specific only: a methodless literal would
	// conflict with the methodless /api/triage/{id} fallback. A wrong
	// method still reaches that fallback's JSON 405.
	s.mux.Handle("GET /api/triage/summary", s.route(true, s.gated(s.handleTriageSummary)))
	s.get("/api/triage/{$}", true, s.gated(s.handleTriageResult))
	s.get("/api/triage/{id}", true, s.gated(s.handleTriageResult))
	s.get("/api/search", true, s.gated(s.handleSearch))

	s.get("/api/priority", false, s.gated(s.handlePriority))
	s.get("/api/agenda", false, s.gated(s.handleAgenda))
	s.get("/api/companies", false, s.gated(s.handleCompanies))
	// The literal is method-specific only: a methodless literal would
	// conflict with the methodless /api/companies/{key} fallback. A wrong
	// method still reaches that fallback's JSON 405.
	s.mux.Handle("GET /api/companies/profiles", s.route(false, s.gated(s.handleCompanyProfiles)))
	s.get("/api/companies/{key}", false, s.gated(s.handleCompany))
	// The empty key is invalid, not an unknown route: match the trailing
	// slash exactly so a missing key answers invalid_company.
	s.get("/api/companies/{$}", false, s.gated(s.handleCompany))
	s.post("/api/ask", true, s.gated(s.handleAsk))
	s.methods("/api/ask/history", "GET, DELETE", false, map[string]http.HandlerFunc{
		"GET":    s.gated(s.handleAskHistory),
		"DELETE": s.gated(s.handleAskClear),
	})
	s.get("/api/ask/history/{$}", false, s.gated(s.handleAskHistoryItem))
	s.get("/api/ask/history/{id}", false, s.gated(s.handleAskHistoryItem))
	s.get("/api/threads/{conversation_id}", false, s.gated(s.handleThread))
	// The empty id is invalid, not an unknown route: match the trailing
	// slash exactly so a missing conversation id answers invalid_thread.
	s.get("/api/threads/{$}", false, s.gated(s.handleThread))
	s.get("/api/conversations", false, s.gated(s.handleConversations))
	s.get("/api/commitments", false, s.gated(s.handleCommitments))
	s.get("/api/brief", false, s.gated(s.handleBrief))

	// The update surface is owner-only (bearer auth) but not learning-gated:
	// a fresh install must be able to read its update status and answer the
	// What's New markers before learning completes. The GET reads state only;
	// the POSTs record a marker or run one check.
	s.get("/api/update", false, s.handleUpdate)
	s.post("/api/update/check", false, s.handleUpdateCheck)
	s.post("/api/update/skip", false, s.handleUpdateSkip)
	s.post("/api/update/seen", false, s.handleUpdateSeen)

	s.post("/api/shutdown", false, s.handleShutdown)

	// The exact API root is JSON, not ServeMux's HTML 301 to /api/.
	s.mux.Handle("/api", s.route(false, s.apiNotFound))
	s.mux.Handle("/api/", s.route(false, s.apiNotFound))

	// The static tree is methodless as well, so the one catch-all cannot
	// conflict with the methodless API subtrees; method checks live in
	// handleStatic.
	s.mux.Handle("/", s.route(false, s.handleStatic))
	return s.middleware(s.mux)
}

func (s *Server) get(path string, long bool, h http.HandlerFunc) {
	s.mux.Handle("GET "+path, s.route(long, h))
	s.mux.Handle(path, s.methodNotAllowed("GET"))
}

func (s *Server) post(path string, long bool, h http.HandlerFunc) {
	s.mux.Handle("POST "+path, s.route(long, h))
	s.mux.Handle(path, s.methodNotAllowed("POST"))
}

// methods registers several methods on one path and one combined Allow
// fallback, so a path is never registered twice in the mux.
func (s *Server) methods(path, allow string, long bool, handlers map[string]http.HandlerFunc) {
	for method, h := range handlers {
		s.mux.Handle(method+" "+path, s.route(long, h))
	}
	s.mux.Handle(path, s.methodNotAllowed(allow))
}

// handleStatic serves the embedded shell. Security headers (including the
// CSP) stay owned by the API middleware; content types and cache policy
// belong to the embedded files' handler.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	s.staticHeaders(w)
	s.static.ServeHTTP(w, r)
}

// onlyMethod serves one method and returns the JSON 405 for every other.
func (s *Server) onlyMethod(method, allow string, next http.HandlerFunc) http.Handler {
	handler := s.route(false, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			s.methodNotAllowed(allow).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// handleSettingPath dispatches the three methods of one setting key. The
// path is /api/settings/<key>; a key is one segment and is validated by
// the settings owner (Schema.Lookup).
func (s *Server) handleSettingPath(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/api/settings/")
	if key == "" || strings.Contains(key, "/") {
		s.writeError(w, r, notFound())
		return
	}
	r.SetPathValue("key", key)
	switch r.Method {
	case http.MethodGet:
		s.handleSetting(w, r)
	case http.MethodPut:
		s.handleSettingPut(w, r)
	case http.MethodDelete:
		s.handleSettingDelete(w, r)
	default:
		s.methodNotAllowed("GET, PUT, DELETE").ServeHTTP(w, r)
	}
}
