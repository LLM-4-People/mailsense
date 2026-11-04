package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/httpgzip"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/triage"
)

// APIError is the error value under the "error" key. Status is never
// encoded. Params carries the values a client needs to compose its own
// localized message; Message stays an English diagnostic/fallback.
type APIError struct {
	Status   int               `json:"-"`
	Code     string            `json:"code"`
	Message  string            `json:"message"`
	Params   map[string]string `json:"params,omitempty"`
	Problems []Problem         `json:"problems,omitempty"`
}

// Error implements error.
func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Problem is one actionable validation or gate failure.
type Problem struct {
	Code    string            `json:"code"`
	Stage   string            `json:"stage,omitempty"`
	Subject string            `json:"subject,omitempty"`
	Message string            `json:"message,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Actions []learning.Action `json:"actions,omitempty"`
	Measure *learning.Measure `json:"measure,omitempty"`
}

type errorBody struct {
	Error *APIError `json:"error"`
}

// marshal is the only JSON encoding path of the API (responses and SSE).
func marshal(v any) ([]byte, error) { return json.Marshal(v) }

// writeJSON writes one JSON response. It is the only success encoder.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	body, err := marshal(v)
	if err != nil {
		s.log.Error("api response encoding failed", "err", err)
		body = []byte(`{"error":{"code":"internal","message":"internal error"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Vary", "Accept-Encoding")
	if httpgzip.Accepts(r) && len(body) >= httpgzip.MinBytes {
		compressed, err := httpgzip.Compress(body)
		if err != nil {
			s.log.Warn("api response compression failed; sending plain JSON", "err", err)
		} else {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
			w.WriteHeader(status)
			_, _ = w.Write(compressed)
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeAPIError writes an already classified error.
func (s *Server) writeAPIError(w http.ResponseWriter, r *http.Request, status int, e *APIError) {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if e.Status == 0 {
		e.Status = status
	}
	s.writeJSON(w, r, status, errorBody{Error: e})
}

// writeError maps and writes one service error. A canceled client is not
// answered (it is gone); everything else gets the contract's JSON shape.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}
	e := s.mapError(err)
	switch {
	case e.Status >= 500:
		s.log.Error("api request failed", "method", r.Method, "route", r.Pattern, "status", e.Status, "err", err)
	case e.Status == http.StatusServiceUnavailable:
		s.log.Warn("api request unavailable", "method", r.Method, "route", r.Pattern, "status", e.Status, "err", err)
	}
	s.writeAPIError(w, r, e.Status, e)
}

func (s *Server) mapError(err error) *APIError {
	// A label batch is well-formed JSON whose items can still be rejected by
	// the labeler: one problem per item, never an internal error.
	var labelValidation *feedback.ValidationError
	if errors.As(err, &labelValidation) {
		out := &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_value",
			Message: "some labels were not stored"}
		for _, problem := range labelValidation.Problems {
			out.Problems = append(out.Problems, Problem{Code: "invalid_value", Message: problem})
		}
		return out
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 0 {
			apiErr.Status = http.StatusInternalServerError
		}
		if apiErr.Code == "" {
			apiErr.Code = "internal"
		}
		if apiErr.Message == "" {
			apiErr.Message = genericMessage(apiErr.Status)
		}
		return apiErr
	}
	var tooLong *search.QueryTooLongError
	if errors.As(err, &tooLong) {
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "query_too_long",
			Message: tooLong.Error(), Params: map[string]string{"max": strconv.Itoa(tooLong.Max)}}
	}
	var locked *learning.LockedError
	if errors.As(err, &locked) {
		return lockedError(locked)
	}
	var validation *config.ValidationError
	if errors.As(err, &validation) {
		return s.validationError(validation)
	}
	var providerValidation *engine.ValidationError
	if errors.As(err, &providerValidation) {
		return providerValidationError(providerValidation)
	}
	var limited *resource.LimitError
	if errors.As(err, &limited) {
		return &APIError{Status: http.StatusServiceUnavailable, Code: "resource_limit",
			Message: limited.Error(),
			Params: map[string]string{
				"resource": string(limited.Kind),
				"percent":  strconv.Itoa(int(math.Round(limited.Percent))),
				"limit":    strconv.Itoa(limited.Limit),
			}}
	}
	if errors.Is(err, resource.ErrLimited) {
		return &APIError{Status: http.StatusServiceUnavailable, Code: "resource_limit",
			Message: "the host is above a resource limit"}
	}
	switch {
	case errors.Is(err, triage.ErrTriageBusy), errors.Is(err, index.ErrBusy), errors.Is(err, evidence.ErrBusy),
		errors.Is(err, layer1.ErrBusy), errors.Is(err, layer1.ErrJudgeBusy), errors.Is(err, syncer.ErrSourceBusy),
		errors.Is(err, syncer.ErrBackfillBusy):
		return &APIError{Status: http.StatusConflict, Code: "busy", Message: "a run of this kind is already in progress"}
	case errors.Is(err, source.ErrUnknownMailbox):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "unknown_mailbox", Message: "the mailbox is not in the last discovery"}
	case errors.Is(err, source.ErrInvalidAddress):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_address", Message: "the address is not a mail address"}
	case errors.Is(err, source.ErrUnavailable):
		// A connector names the fix ("start Outlook", "install classic
		// Outlook"). Preserve it as a parameter so the screen can offer the
		// next step instead of a generic refusal; a source without a named
		// cause keeps the generic message.
		out := &APIError{Status: http.StatusServiceUnavailable, Code: "source_unavailable", Message: "the mail source is unavailable"}
		if code, reason := source.Cause(err); reason != "" {
			out.Params = map[string]string{"reason": reason}
			if code != "" {
				out.Params["cause"] = code
			}
		}
		return out
	case errors.Is(err, secrets.ErrNotFound):
		return &APIError{Status: http.StatusNotFound, Code: "secret_not_found", Message: "the secret does not exist"}
	case errors.Is(err, secrets.ErrReadOnly):
		return &APIError{Status: http.StatusForbidden, Code: "read_only", Message: "the secret store is read-only"}
	case errors.Is(err, store.ErrNotFound):
		return notFound()
	case errors.Is(err, store.ErrInvalidRole):
		return invalidRole()
	case errors.Is(err, store.ErrInvalidLimit):
		return invalidLimit(http.StatusBadRequest, "limit", 1, store.MessageListMaxLimit)
	case errors.Is(err, store.ErrInvalidStore):
		return invalidStore()
	case errors.Is(err, store.ErrInvalidSender):
		return invalidSenderError()
	case errors.Is(err, store.ErrInvalidCursor):
		return invalidCursor()
	case errors.Is(err, store.ErrThreadNotFound):
		return notFoundWith("unknown_thread", "unknown thread")
	case errors.Is(err, store.ErrInvalidConversationID):
		return invalidThread()
	case errors.Is(err, store.ErrInvalidThreadCursor):
		return invalidBefore()
	case errors.Is(err, conversations.ErrInvalidLimit):
		return invalidLimit(http.StatusBadRequest, "limit", conversations.MinLimit, conversations.MaxLimit)
	case errors.Is(err, conversations.ErrInvalidCursor):
		return invalidCursor()
	case errors.Is(err, conversations.ErrInvalidState):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_state",
			Message: "unknown conversation state", Params: map[string]string{"name": "state"}}
	case errors.Is(err, conversations.ErrInvalidQuery):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_query",
			Message: "the query is too long or contains a control character",
			Params:  map[string]string{"name": "q", "max": strconv.Itoa(conversations.MaxQueryRunes)}}
	case errors.Is(err, commitments.ErrInvalidKind):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_kind",
			Message: "unknown commitment kind"}
	case errors.Is(err, commitments.ErrInvalidDirection):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_direction",
			Message: "unknown commitment direction"}
	case errors.Is(err, commitments.ErrInvalidStatus):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_status",
			Message: "unknown commitment status"}
	case errors.Is(err, commitments.ErrInvalidLimit):
		return invalidLimit(http.StatusBadRequest, "limit", 1, commitments.MaxLimit)
	case errors.Is(err, sidecar.ErrUnsupported):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "build_unsupported", Message: "no build supports this machine"}
	case errors.Is(err, sidecar.ErrNoVulkan):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "vulkan_unavailable",
			Message: "no usable Vulkan device was found; install a GPU driver with Vulkan support, or use another machine"}
	case errors.Is(err, sidecar.ErrVulkanProbe):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "vulkan_probe_failed",
			Message: "the Vulkan device check failed; it may be temporary, so retry"}
	case errors.Is(err, sidecar.ErrNotInstalled), errors.Is(err, sidecar.ErrStarting), errors.Is(err, sidecar.ErrFailed):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "runtime_unavailable", Message: "the local runtime is unavailable"}
	case errors.Is(err, sidecar.ErrHash):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "hash_mismatch", Message: "the runtime build does not match its pin"}
	case errors.Is(err, sidecar.ErrStopped):
		return shuttingDown()
	case errors.Is(err, engine.ErrDiscoveryInvalidRequest):
		return invalidRequest("base_url must be an absolute http(s) URL")
	case errors.Is(err, engine.ErrDiscoveryAuth):
		return &APIError{Status: http.StatusBadGateway, Code: "provider_discovery_auth", Message: "the model list rejected the credentials"}
	case errors.Is(err, engine.ErrDiscoveryNotFound):
		return &APIError{Status: http.StatusBadGateway, Code: "provider_discovery_not_found", Message: "the model list endpoint does not exist"}
	case errors.Is(err, engine.ErrDiscoveryFailed):
		return &APIError{Status: http.StatusBadGateway, Code: "provider_discovery_failed", Message: "the model list request failed"}
	case errors.Is(err, engine.ErrDiscoveryInvalid):
		return &APIError{Status: http.StatusBadGateway, Code: "provider_discovery_invalid", Message: "the model list response is invalid"}
	case errors.Is(err, engine.ErrDiscoveryEmpty):
		return &APIError{Status: http.StatusBadGateway, Code: "provider_discovery_empty", Message: "the model list is empty"}
	case errors.Is(err, engine.ErrTestFailed):
		return ProviderTestFailed()
	case errors.Is(err, engine.ErrNoKey):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "missing_key", Message: "the provider key is not configured"}
	case errors.Is(err, engine.ErrUnknownProvider), errors.Is(err, triage.ErrUnknownProvider):
		return UnknownProvider()
	case errors.Is(err, engine.ErrNotConfigured):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "provider_not_configured", Message: "the provider role is not configured"}
	case errors.Is(err, engine.ErrUnavailable):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "provider_unavailable", Message: "the provider is unavailable"}
	case errors.Is(err, layer1.ErrUnknownQuestion):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "unknown_question", Message: "the question is not in the profile"}
	case errors.Is(err, triage.ErrNotTriaged):
		return &APIError{Status: http.StatusNotFound, Code: "not_triaged", Message: "the message is not in the triaged scope"}
	case errors.Is(err, search.ErrEmptyQuery):
		return &APIError{Status: http.StatusBadRequest, Code: "empty_query", Message: "the query is empty"}
	case errors.Is(err, search.ErrInvalidLimit):
		return invalidLimit(http.StatusBadRequest, "limit", 1, MaxSearchAPILimit)
	case errors.Is(err, search.ErrInvalidOffset):
		return invalidOffset("offset", 0, search.MaxOffset)
	case errors.Is(err, search.ErrNoTriage):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "triage_unavailable", Message: "triage filters need triage results"}
	case errors.Is(err, qa.ErrInvalidQuestion):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_question",
			Message: "the question must be between " + strconv.Itoa(qa.MinQuestionRunes) + " and " +
				strconv.Itoa(qa.MaxQuestionRunes) + " characters"}
	case errors.Is(err, qa.ErrInvalidLanguage):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_language",
			Message: "language must be " + qa.LanguageEN + " or " + qa.LanguageFR}
	case errors.Is(err, qa.ErrInvalidConversation):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_conversation_id",
			Message: "the conversation id is invalid"}
	case errors.Is(err, qa.ErrInvalidRoute):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_route",
			Message: "route must be " + string(qa.RouteAuto) + ", " + string(qa.RouteLocal) + " or " + string(qa.RouteRemote)}
	case errors.Is(err, qa.ErrRouteUnavailable):
		out := &APIError{Status: http.StatusUnprocessableEntity, Code: "qa_route_unavailable",
			Message: "the selected answer route is unavailable",
			Params:  map[string]string{"route": string(qa.RouteRemote), "reason": "unavailable"}}
		var routeErr *qa.RouteUnavailableError
		if errors.As(err, &routeErr) {
			out.Params["route"] = string(routeErr.Route)
			if routeErr.Reason != "" {
				out.Params["reason"] = routeErr.Reason
			}
		}
		return out
	case errors.Is(err, qa.ErrInvalidHistoryID):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_id", Message: "the history id is invalid"}
	case errors.Is(err, qa.ErrDisabled):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "qa_disabled", Message: "Q&A is disabled"}
	case errors.Is(err, layer1.ErrJudgeNotBuilt):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "judge_unavailable", Message: "no judge is configured"}
	case errors.Is(err, context.DeadlineExceeded):
		return &APIError{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "the request timed out"}
	case errors.Is(err, context.Canceled):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "request_cancelled", Message: "the request was cancelled"}
	case errors.Is(err, ErrShutdown), errors.Is(err, jobs.ErrShutdown):
		return shuttingDown()
	}
	return &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
}

func lockedError(err *learning.LockedError) *APIError {
	out := &APIError{Status: http.StatusLocked, Code: "learning_locked", Message: "learning is not complete"}
	for _, stage := range err.Status.Stages {
		for _, b := range stage.Blockers {
			out.Problems = append(out.Problems, Problem{Code: b.Code, Stage: stage.ID, Subject: b.Subject,
				Message: b.Detail, Actions: b.Actions, Measure: b.Measure})
		}
	}
	if len(out.Problems) == 0 {
		out.Message = fmt.Sprintf("learning is not complete (state: %s)", err.Status.State)
	}
	return out
}

func (s *Server) validationError(err *config.ValidationError) *APIError {
	out := &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_value", Message: "one or more values are invalid"}
	for _, p := range err.Problems {
		message := p.Source + ": " + p.Message
		if s.services.Settings.Secret(p.Key) {
			message = "invalid value (hidden)"
		}
		out.Problems = append(out.Problems, Problem{Code: "invalid_value", Subject: p.Key, Message: message,
			Params: map[string]string{"key": p.Key, "source": p.Source}})
	}
	return out
}

// InvalidProvider is the one constructor of an invalid provider request.
func InvalidProvider(message string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_provider", Message: message}
}

// InvalidProviderProblems is the validation form: one problem per message.
func InvalidProviderProblems(problems []string) *APIError {
	out := InvalidProvider("the provider record set is invalid")
	for _, msg := range problems {
		out.Problems = append(out.Problems, Problem{Code: "invalid_provider", Message: msg})
	}
	return out
}

// ProviderTestFailed is the one failed-provider-test answer: mapError uses it
// for engine.ErrTestFailed, and the wiring uses it for an unexpected failure
// that must not leak as an internal error.
func ProviderTestFailed() *APIError {
	return &APIError{Status: http.StatusBadGateway, Code: "provider_test_failed", Message: "the provider test failed"}
}

// ErrNoMailboxPicked is the one "pick a mailbox first" answer of the sync and
// backfill starters.
func ErrNoMailboxPicked() *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "no_mailbox_picked", Message: "no mailbox is picked"}
}

// ErrInvalidProfile is the one "the triage profile could not be loaded" answer
// of the question catalogue and the label batch.
func ErrInvalidProfile() *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_profile", Message: "the triage profile is invalid"}
}

// UnknownProvider is the one constructor of a missing provider record.
func UnknownProvider() *APIError {
	return &APIError{Status: http.StatusNotFound, Code: "unknown_provider", Message: "the provider record does not exist"}
}

func providerValidationError(err *engine.ValidationError) *APIError {
	return InvalidProviderProblems(err.Problems)
}

func genericMessage(status int) string {
	if status >= 500 {
		return "internal error"
	}
	return http.StatusText(status)
}

// shuttingDown is the one "the app is closing" answer: mapped from the
// shutdown sentinels and written directly by the middleware (a refused
// handler) and the SSE stream (a refused subscriber).
func shuttingDown() *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: "shutting_down", Message: "the app is shutting down"}
}

// ErrUnknownSetting is the one unknown-setting error: the settings handler
// and the service wiring both return it.
func ErrUnknownSetting() *APIError {
	return &APIError{Status: http.StatusNotFound, Code: "unknown_setting", Message: "unknown setting"}
}

// ErrInvalidSettingKeys is the one invalid settings-projection error: the
// collection exists, but the keys parameter names a key the schema does not
// expose. A 404 would blame the collection, not the query.
func ErrInvalidSettingKeys(key string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_keys",
		Message: "the keys parameter names an unknown setting", Params: map[string]string{"key": key}}
}

// ErrUseMailboxEndpoint is the one "this setting belongs to the mailbox
// endpoints" error.
func ErrUseMailboxEndpoint() *APIError {
	return &APIError{Status: http.StatusConflict, Code: "use_mailbox_endpoint", Message: "use the mailbox endpoints"}
}

// ErrUnknownCompany is the one unknown-company error.
func ErrUnknownCompany() *APIError {
	return &APIError{Status: http.StatusNotFound, Code: "unknown_company", Message: "unknown company"}
}
