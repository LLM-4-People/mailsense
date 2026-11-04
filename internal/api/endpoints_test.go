package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestEveryEndpointSucceeds(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	cases := []struct {
		name   string
		in     request
		status int
	}{
		{"status", request{method: "GET", path: "/api/status"}, 200},
		{"usage", request{method: "GET", path: "/api/usage"}, 200},
		{"boot", request{method: "GET", path: "/api/boot"}, 200},
		{"learning", request{method: "GET", path: "/api/learning"}, 200},
		{"gate", request{method: "GET", path: "/api/gate"}, 200},
		{"zone", request{method: "POST", path: "/api/learning/zone", body: `{"zone":"Europe/Paris"}`}, 200},
		{"jobs", request{method: "GET", path: "/api/jobs"}, 200},
		{"job", request{method: "GET", path: "/api/jobs/job-1"}, 200},
		{"job cancel", request{method: "POST", path: "/api/jobs/job-1/cancel"}, 202},
		{"held removals", request{method: "GET", path: "/api/held-removals"}, 200},
		{"held removals by source", request{method: "GET", path: "/api/held-removals?source=outlook"}, 200},
		{"held removal confirm", request{method: "POST", path: "/api/held-removals/7/confirm"}, 200},
		{"mailboxes", request{method: "GET", path: "/api/mailboxes"}, 200},
		{"discover", request{method: "POST", path: "/api/mailboxes/discover", body: `{"source":"outlook"}`}, 200},
		{"pick", request{method: "POST", path: "/api/mailboxes/pick", body: `{"keys":["store-1"]}`}, 200},
		{"unpick", request{method: "POST", path: "/api/mailboxes/unpick", body: `{"keys":["store-1"]}`}, 200},
		{"declare", request{method: "POST", path: "/api/mailboxes/declare", body: `{"key":"store-1","address":"owner@example.test"}`}, 200},
		{"undeclare", request{method: "POST", path: "/api/mailboxes/undeclare", body: `{"key":"store-1","address":"owner@example.test"}`}, 200},
		{"sync", request{method: "POST", path: "/api/actions/sync", body: `{"source":"outlook"}`}, 202},
		{"source start", request{method: "POST", path: "/api/sources/outlook/start", body: `{}`}, 200},
		{"source resume", request{method: "POST", path: "/api/sources/outlook/resume", body: `{}`}, 200},
		{"source retry details", request{method: "POST", path: "/api/sources/outlook/retry-details", body: `{}`}, 200},
		{"evidence", request{method: "POST", path: "/api/actions/run_evidence", body: `{}`}, 202},
		{"index", request{method: "POST", path: "/api/actions/index", body: `{"stores":["store-1"]}`}, 202},
		{"layer1", request{method: "POST", path: "/api/actions/run_layer1", body: `{}`}, 202},
		{"judge", request{method: "POST", path: "/api/actions/run_judge", body: `{"limit":1,"questions":["category"],"budget":{"max_calls":1,"max_usd":1,"allow_unpriced":true}}`}, 202},
		{"settings", request{method: "GET", path: "/api/settings"}, 200},
		{"setting", request{method: "GET", path: "/api/settings/time.zone"}, 200},
		{"setting put", request{method: "PUT", path: "/api/settings/time.zone", body: `{"value":"Europe/Paris"}`}, 200},
		{"setting delete", request{method: "DELETE", path: "/api/settings/time.zone"}, 200},
		{"settings reload", request{method: "POST", path: "/api/settings/reload", body: `{}`}, 200},
		{"learned", request{method: "GET", path: "/api/settings/learned"}, 200},
		{"learned reject", request{method: "POST", path: "/api/settings/learned/time.zone/reject", body: `{"value":"UTC"}`}, 200},
		{"learned unreject", request{method: "POST", path: "/api/settings/learned/time.zone/unreject", body: `{"value":"UTC"}`}, 200},
		{"secrets", request{method: "GET", path: "/api/secrets"}, 200},
		{"secret put", request{method: "PUT", path: "/api/secrets/DEMO_API_KEY", body: `{"value":"test-value"}`}, 204},
		{"secret delete", request{method: "DELETE", path: "/api/secrets/DEMO_API_KEY"}, 204},
		{"providers", request{method: "GET", path: "/api/providers"}, 200},
		{"provider create", request{method: "POST", path: "/api/providers", body: `{"id":"new","protocol":"systemone"}`}, 201},
		{"provider update", request{method: "PUT", path: "/api/providers/dec", body: `{"protocol":"systemone"}`}, 200},
		{"provider delete", request{method: "DELETE", path: "/api/providers/dec"}, 204},
		{"provider test", request{method: "POST", path: "/api/providers/dec/test", body: `{}`}, 200},
		{"provider discover", request{method: "POST", path: "/api/providers/discover", body: `{"base_url":"https://provider.example.test/v1"}`}, 200},
		{"sidecar", request{method: "GET", path: "/api/sidecar"}, 200},
		{"sidecar install", request{method: "POST", path: "/api/sidecar/install", body: `{"runtime":"llama","model":"m","variant":"cpu"}`}, 202},
		{"sidecar verify", request{method: "POST", path: "/api/sidecar/verify", body: `{"runtime":"llama","model":"m","variant":"cpu"}`}, 200},
		{"logs", request{method: "GET", path: "/api/logs?limit=1"}, 200},
		{"log entry", request{method: "GET", path: "/api/logs/1"}, 200},
		{"label queue", request{method: "GET", path: "/api/labels/queue?question=category&limit=1"}, 200},
		{"label review queue", request{method: "GET", path: "/api/labels/queue?question=category&limit=1&review=true"}, 200},
		{"labels", request{method: "POST", path: "/api/labels", body: `{"items":[{"message_id":1,"question":"category","value":"work"}]}`}, 200},
		{"label delete", request{method: "DELETE", path: "/api/labels/1/category"}, 200},
		{"messages", request{method: "GET", path: "/api/messages"}, 200},
		{"message", request{method: "GET", path: "/api/messages/1"}, 200},
		{"thread", request{method: "GET", path: "/api/threads/C1"}, 200},
		{"conversations", request{method: "GET", path: "/api/conversations"}, 200},
		{"commitments", request{method: "GET", path: "/api/commitments"}, 200},
		{"brief", request{method: "GET", path: "/api/brief"}, 200},
		{"triage", request{method: "GET", path: "/api/triage?limit=50&offset=0"}, 200},
		{"triage summary", request{method: "GET", path: "/api/triage/summary"}, 200},
		{"triage result", request{method: "GET", path: "/api/triage/1"}, 200},
		{"search", request{method: "GET", path: "/api/search?q=hello"}, 200},
		{"companies", request{method: "GET", path: "/api/companies"}, 200},
		{"company", request{method: "GET", path: "/api/companies/c%3Aatelier%20nord"}, 200},
		{"shutdown", request{method: "POST", path: "/api/shutdown", body: `{}`}, 202},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, header, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
			if tc.status == 204 {
				if len(data) != 0 || header.Get("Content-Type") != "" {
					t.Fatalf("204 with body/content type: %q %q", data, header.Get("Content-Type"))
				}
				return
			}
			if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
				t.Fatalf("content type %q", header.Get("Content-Type"))
			}
			decodeMap(t, data)
		})
	}
}

func TestEveryEndpointRejectsTheWrongMethod(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	cases := []struct {
		method string
		path   string
	}{
		{"POST", "/api/status"},
		{"POST", "/api/usage"},
		{"POST", "/api/boot"},
		{"POST", "/api/learning"},
		{"POST", "/api/gate"},
		{"GET", "/api/learning/zone"},
		{"POST", "/api/jobs"},
		{"POST", "/api/jobs/job-1"},
		{"GET", "/api/jobs/job-1/cancel"},
		{"POST", "/api/mailboxes"},
		{"GET", "/api/mailboxes/discover"},
		{"GET", "/api/mailboxes/pick"},
		{"GET", "/api/mailboxes/unpick"},
		{"GET", "/api/mailboxes/declare"},
		{"GET", "/api/mailboxes/undeclare"},
		{"GET", "/api/actions/sync"},
		{"GET", "/api/sources/outlook/start"},
		{"POST", "/api/settings"},
		{"POST", "/api/settings/time.zone"},
		{"GET", "/api/settings/reload"},
		{"POST", "/api/settings/learned"},
		{"GET", "/api/settings/learned/time.zone/reject"},
		{"GET", "/api/settings/learned/time.zone/unreject"},
		{"POST", "/api/secrets"},
		{"GET", "/api/secrets/DEMO_API_KEY"},
		{"GET", "/api/providers/dec/test"},
		{"PATCH", "/api/providers/dec"},
		{"GET", "/api/providers/discover"},
		{"POST", "/api/sidecar"},
		{"GET", "/api/sidecar/install"},
		{"GET", "/api/sidecar/verify"},
		{"POST", "/api/logs"},
		{"POST", "/api/logs/1"},
		{"POST", "/api/events"},
		{"POST", "/api/labels/queue"},
		{"GET", "/api/labels"},
		{"POST", "/api/labels/1/category"},
		{"POST", "/api/messages"},
		{"POST", "/api/messages/1"},
		{"POST", "/api/messages/1/attachments/0"},
		{"POST", "/api/threads/C1"},
		{"POST", "/api/threads/"},
		{"POST", "/api/conversations"},
		{"POST", "/api/commitments"},
		{"POST", "/api/brief"},
		{"POST", "/api/triage"},
		{"POST", "/api/triage/summary"},
		{"POST", "/api/triage/1"},
		{"POST", "/api/search"},
		{"POST", "/api/companies"},
		{"POST", "/api/companies/c%3Aatelier%20nord"},
		{"POST", "/api/companies/"},
		{"GET", "/api/shutdown"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			status, header, data := do(t, ts, srv, request{method: tc.method, path: tc.path})
			mustStatus(t, status, 405, data)
			if header.Get("Allow") == "" {
				t.Fatal("405 without Allow")
			}
			if code := errorCode(t, data); code != "method_not_allowed" {
				t.Fatalf("code %q", code)
			}
		})
	}
}

func TestProviderDiscoveryEndpoint(t *testing.T) {
	srv, ts, w := newTestServer(t)
	in := request{method: "POST", path: "/api/providers/discover",
		body: `{"base_url":" https://provider.example.test/v1/ ","api_key":"form-key"}`}
	status, header, data := do(t, ts, srv, in)
	mustStatus(t, status, 200, data)
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("content type %q", header.Get("Content-Type"))
	}
	var got ProviderDiscoveryView
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "provider.example.test/deepseek-v4.1-flash" ||
		got.Models[0].ContextTokens != 1048576 || got.Models[0].Path != "/chat/completions" {
		t.Fatalf("models %+v", got.Models)
	}
	if w.providers.discoverN != 1 || w.providers.discoverReq.BaseURL != "https://provider.example.test/v1" ||
		w.providers.discoverReq.Protocol != "openai_chat" || w.providers.discoverReq.APIKey != "form-key" {
		t.Fatalf("service request %+v", w.providers.discoverReq)
	}
	if strings.Contains(string(data), "form-key") {
		t.Fatal("the response contains the API key")
	}

	status, _, data = do(t, ts, srv, noAuth(in))
	mustStatus(t, status, 401, data)
	if code := errorCode(t, data); code != "unauthorized" {
		t.Fatalf("code %q", code)
	}
}

func TestProviderDiscoveryErrorCodes(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"auth", fmt.Errorf("%w: inference.example answered HTTP 401", engine.ErrDiscoveryAuth), 502, "provider_discovery_auth"},
		{"not found", fmt.Errorf("%w: inference.example answered HTTP 404", engine.ErrDiscoveryNotFound), 502, "provider_discovery_not_found"},
		{"failed", fmt.Errorf("%w: inference.example answered HTTP 500", engine.ErrDiscoveryFailed), 502, "provider_discovery_failed"},
		{"invalid", fmt.Errorf("%w: inference.example answered HTTP 200", engine.ErrDiscoveryInvalid), 502, "provider_discovery_invalid"},
		{"empty", fmt.Errorf("%w: inference.example answered HTTP 200", engine.ErrDiscoveryEmpty), 502, "provider_discovery_empty"},
		{"missing key", fmt.Errorf("%w: secret MISSING has no value", engine.ErrNoKey), 422, "missing_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w.providers.discoverErr = tc.err
			status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/providers/discover",
				body: `{"base_url":"https://inference.example/v1"}`})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
		})
	}
}

func TestEndpointMisuse(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	cases := []struct {
		name   string
		in     request
		status int
		code   string
	}{
		{"unknown action", request{method: "POST", path: "/api/actions/nope", body: `{}`}, 404, "not_found"},
		{"sync bad json", request{method: "POST", path: "/api/actions/sync", body: `{`}, 400, "invalid_json"},
		{"index unknown field", request{method: "POST", path: "/api/actions/index", body: `{"nope":1}`}, 400, "invalid_json"},
		{"judge missing limit", request{method: "POST", path: "/api/actions/run_judge", body: `{}`}, 400, "invalid_limit"},
		{"evidence bad json", request{method: "POST", path: "/api/actions/run_evidence", body: `{`}, 400, "invalid_json"},
		{"pick no keys", request{method: "POST", path: "/api/mailboxes/pick", body: `{"keys":[]}`}, 400, "invalid_request"},
		{"unpick empty key", request{method: "POST", path: "/api/mailboxes/unpick", body: `{"keys":[""]}`}, 400, "invalid_request"},
		{"declare no address", request{method: "POST", path: "/api/mailboxes/declare", body: `{"key":"store-1"}`}, 400, "invalid_request"},
		{"undeclare no key", request{method: "POST", path: "/api/mailboxes/undeclare", body: `{"address":"x@y"}`}, 400, "invalid_request"},
		{"job unknown", request{method: "GET", path: "/api/jobs/nope"}, 404, "not_found"},
		{"cancel unknown", request{method: "POST", path: "/api/jobs/nope/cancel"}, 404, "not_found"},
		{"settings unknown", request{method: "GET", path: "/api/settings/nope"}, 404, "unknown_setting"},
		{"settings put unknown", request{method: "PUT", path: "/api/settings/nope", body: `{"value":1}`}, 404, "unknown_setting"},
		{"settings delete unknown", request{method: "DELETE", path: "/api/settings/nope"}, 404, "unknown_setting"},
		{"learned unknown", request{method: "GET", path: "/api/settings/learned/nope/reject"}, 405, ""},
		{"learned reject unknown", request{method: "POST", path: "/api/settings/learned/nope/reject", body: `{"value":1}`}, 404, "unknown_setting"},
		{"learned missing value", request{method: "POST", path: "/api/settings/learned/time.zone/reject", body: `{}`}, 400, "invalid_request"},
		{"secret invalid name", request{method: "PUT", path: "/api/secrets/lower_case", body: `{"value":"x"}`}, 400, "invalid_name"},
		{"secret empty", request{method: "PUT", path: "/api/secrets/DEMO_API_KEY", body: `{"value":""}`}, 422, "empty_value"},
		{"secret delete invalid", request{method: "DELETE", path: "/api/secrets/lower_case"}, 400, "invalid_name"},
		{"sidecar install missing", request{method: "POST", path: "/api/sidecar/install", body: `{"runtime":"llama"}`}, 400, "invalid_request"},
		{"sidecar verify missing", request{method: "POST", path: "/api/sidecar/verify", body: `{}`}, 400, "invalid_request"},
		{"provider discover missing base_url", request{method: "POST", path: "/api/providers/discover", body: `{}`}, 400, "invalid_request"},
		{"provider discover relative base_url", request{method: "POST", path: "/api/providers/discover", body: `{"base_url":"/v1"}`}, 400, "invalid_request"},
		{"provider update bad id", request{method: "PUT", path: "/api/providers/Bad_ID", body: `{}`}, 400, "invalid_id"},
		{"provider delete bad id", request{method: "DELETE", path: "/api/providers/Bad_ID"}, 400, "invalid_id"},
		{"provider test bad id", request{method: "POST", path: "/api/providers/Bad_ID/test", body: `{}`}, 400, "invalid_id"},
		{"labels empty", request{method: "POST", path: "/api/labels", body: `{"items":[]}`}, 400, "invalid_request"},
		// A malformed item is the labeler's 422 (TestLabelBatchStatusContract
		// runs it against the real service); the handler no longer owns it.
		{"empty message id", request{method: "GET", path: "/api/messages/"}, 400, "invalid_id"},
		{"empty triage id", request{method: "GET", path: "/api/triage/"}, 400, "invalid_id"},
		{"empty history id", request{method: "GET", path: "/api/ask/history/"}, 400, "invalid_id"},
		{"label queue missing question", request{method: "GET", path: "/api/labels/queue"}, 400, "invalid_request"},
		{"label queue bad limit", request{method: "GET", path: "/api/labels/queue?question=category&limit=9999"}, 400, "invalid_limit"},
		{"attachment invalid message", request{method: "GET", path: "/api/messages/abc/attachments/0"}, 400, "invalid_id"},
		{"label queue bad review", request{method: "GET", path: "/api/labels/queue?question=category&review=maybe"}, 400, "invalid_review"},
		{"message bad id", request{method: "GET", path: "/api/messages/abc"}, 400, "invalid_id"},
		{"triage bad id", request{method: "GET", path: "/api/triage/0"}, 400, "invalid_id"},
		{"triage bad limit", request{method: "GET", path: "/api/triage?limit=0"}, 400, "invalid_limit"},
		{"triage huge limit", request{method: "GET", path: "/api/triage?limit=201"}, 400, "invalid_limit"},
		{"triage bad offset", request{method: "GET", path: "/api/triage?offset=-1"}, 400, "invalid_offset"},
		{"search empty", request{method: "GET", path: "/api/search?q="}, 400, "empty_query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
			if tc.code != "" {
				if code := errorCode(t, data); code != tc.code {
					t.Fatalf("code %q, want %q: %s", code, tc.code, data)
				}
			}
		})
	}
}

// The client composes localized messages from codes and params, so a
// limit/validation failure must carry the values the message needs.
func TestErrorParamsAndStatusLocale(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.status.view = StatusView{SystemLanguage: "fr", SystemLocale: "fr-CA", TimeZone: "Europe/Paris"}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/status"})
	mustStatus(t, status, 200, data)
	var view map[string]json.RawMessage
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"system_language": `"fr"`, "system_locale": `"fr-CA"`, "time_zone": `"Europe/Paris"`} {
		if got := string(view[key]); got != want {
			t.Errorf("status %s = %s, want %s", key, got, want)
		}
	}

	var failure struct {
		Error struct {
			Code   string            `json:"code"`
			Params map[string]string `json:"params"`
		} `json:"error"`
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/triage?limit=201"})
	mustStatus(t, status, 400, data)
	if err := json.Unmarshal(data, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "invalid_limit" {
		t.Fatalf("code %q", failure.Error.Code)
	}
	if failure.Error.Params["name"] != "limit" || failure.Error.Params["min"] == "" || failure.Error.Params["max"] == "" {
		t.Fatalf("invalid_limit params = %#v", failure.Error.Params)
	}

	w.settings.setErr = &config.ValidationError{Problems: config.Problems{
		{Key: "ui.language", Source: "value", Message: "must be one of auto, en, fr"},
	}}
	status, _, data = do(t, ts, srv, request{method: "PUT", path: "/api/settings/time.zone", body: `{"value":"Mars/Olympus"}`})
	mustStatus(t, status, 422, data)
	var validation struct {
		Error struct {
			Code     string `json:"code"`
			Problems []struct {
				Subject string            `json:"subject"`
				Params  map[string]string `json:"params"`
			} `json:"problems"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &validation); err != nil {
		t.Fatal(err)
	}
	if validation.Error.Code != "invalid_value" || len(validation.Error.Problems) != 1 {
		t.Fatalf("validation error = %s", data)
	}
	problem := validation.Error.Problems[0]
	if problem.Subject != "ui.language" || problem.Params["key"] != "ui.language" || problem.Params["source"] != "value" {
		t.Fatalf("validation problem = %#v", problem)
	}
}

func TestActionBusy(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.runs.evidenceErr = &APIError{Status: 409, Code: "busy", Message: "a run is in progress"}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/actions/run_evidence", body: `{}`})
	mustStatus(t, status, 409, data)
	if code := errorCode(t, data); code != "busy" {
		t.Fatalf("code %q", code)
	}
}

// A condition has one constructor, so a status or code cannot differ by the
// path that returns it.
func TestErrorConditionsHaveOneShape(t *testing.T) {
	for _, tc := range []struct {
		err    *APIError
		status int
		code   string
	}{
		{ErrUnknownSetting(), 404, "unknown_setting"},
		{ErrUseMailboxEndpoint(), 409, "use_mailbox_endpoint"},
		{ErrUnknownCompany(), 404, "unknown_company"},
	} {
		if tc.err.Status != tc.status || tc.err.Code != tc.code || tc.err.Message == "" {
			t.Errorf("%s = %d/%q/%q, want %d/%q with a message", tc.code, tc.err.Status, tc.err.Code, tc.err.Message, tc.status, tc.code)
		}
	}
}

// The UI no longer declares protocols or role kinds: the providers view is
// the one catalog it renders.
func TestProvidersViewCarriesTheCatalog(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.providers.roles = []ProviderRoleInfo{{Setting: "qa.provider", Label: "Q&A generator", Kind: engine.KindGenerator, Configured: true, Available: true}}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/providers"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	protocols, _ := body["protocols"].([]any)
	if len(protocols) < 4 {
		t.Fatalf("protocols %#v", protocols)
	}
	kinds := map[string]string{}
	for _, raw := range protocols {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		kind, _ := entry["kind"].(string)
		kinds[name] = kind
	}
	if kinds["openai_chat"] != "generator" || kinds["systemone"] != "decider" ||
		kinds["openai_embeddings"] != "embedder" {
		t.Fatalf("protocol kinds %#v", kinds)
	}
	roles, _ := body["roles"].([]any)
	if len(roles) != 1 {
		t.Fatalf("roles %#v", roles)
	}
	role, _ := roles[0].(map[string]any)
	if role["setting"] != "qa.provider" || role["kind"] != "generator" ||
		role["configured"] != true || role["available"] != true {
		t.Fatalf("role %#v", role)
	}
}

// TestMailboxSelectionBatchIsBounded: one selection request may name at most
// the number of stores a profile can list. Without the bound a 1 MiB body
// turns one click route into tens of thousands of per-key scans; the screen
// picks one store per action, so no legitimate client is near it.
func TestMailboxSelectionBatchIsBounded(t *testing.T) {
	opts := testOptions(t)
	opts.MaxBytes = 1 << 16 // the batch bound must answer before the body bound
	srv, ts, w := newTestServerWith(t, opts, newWorld())
	keys := make([]string, maxMailboxKeys+1)
	for i := range keys {
		keys[i] = "store-" + strconv.Itoa(i)
	}
	body, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pick", "unpick"} {
		t.Run(action, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "POST",
				path: "/api/mailboxes/" + action, body: string(body)})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != "invalid_request" {
				t.Fatalf("code %q, want invalid_request: %s", code, data)
			}
		})
	}
	if w.mailboxes.pick != 0 {
		t.Fatalf("an oversized selection reached the service %d times", w.mailboxes.pick)
	}

	// The bound itself is accepted.
	body, err = json.Marshal(map[string]any{"keys": keys[:maxMailboxKeys]})
	if err != nil {
		t.Fatal(err)
	}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/mailboxes/pick", body: string(body)})
	mustStatus(t, status, 200, data)
	if w.mailboxes.pick != 1 {
		t.Fatalf("the bounded selection reached the service %d times, want 1", w.mailboxes.pick)
	}
}

// TestMailboxWriteReturnsTheServiceSnapshot: a selection or identity write
// answers with the mailbox views its service already read and published. The
// route used to list the mailboxes again for the response, which cost a third
// MailboxViews scan per click and could return a different snapshot than the
// one the bus event carried.
func TestMailboxWriteReturnsTheServiceSnapshot(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.mailboxes.boxes = []store.MailboxView{sourceMailbox("store-2")}
	cases := []struct {
		name string
		path string
		body string
	}{
		{"pick", "/api/mailboxes/pick", `{"keys":["store-1"]}`},
		{"unpick", "/api/mailboxes/unpick", `{"keys":["store-1"]}`},
		{"declare", "/api/mailboxes/declare", `{"key":"store-1","address":"owner@example.org"}`},
		{"undeclare", "/api/mailboxes/undeclare", `{"key":"store-1","address":"owner@example.org"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeList, beforeWrite := w.mailboxes.calls, w.mailboxes.pick
			status, _, data := do(t, ts, srv, request{method: "POST", path: tc.path, body: tc.body})
			mustStatus(t, status, 200, data)
			if w.mailboxes.pick != beforeWrite+1 {
				t.Fatalf("the write reached the service %d times", w.mailboxes.pick-beforeWrite)
			}
			if w.mailboxes.calls != beforeList {
				t.Fatalf("the write route listed the mailboxes %d more times", w.mailboxes.calls-beforeList)
			}
			var body mailboxList
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Mailboxes) != 1 || body.Mailboxes[0].Key != "store-2" {
				t.Fatalf("the response is not the service snapshot: %s", data)
			}
		})
	}
}

// TestActionAliasesAreGone pins that each action has one spelling. These six
// names were never sent by the UI (it navigates for label_messages and posts
// the run_* ids) and duplicated a branch the canonical name already owns, so
// they answer the dispatcher's 404 instead of being a second contract to
// maintain.
func TestActionAliasesAreGone(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	for _, name := range []string{"evidence", "layer1", "judge", "label", "label_messages", "backfill_bodies"} {
		t.Run(name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/actions/" + name, body: `{}`})
			mustStatus(t, status, 404, data)
			if code := errorCode(t, data); code != "not_found" {
				t.Fatalf("code %q, want not_found: %s", code, data)
			}
		})
	}
}
