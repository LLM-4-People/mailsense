package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

type settingsProjectionView struct {
	Settings []struct {
		Key string `json:"key"`
	} `json:"settings"`
	Total          int      `json:"total"`
	RestartPending []string `json:"restart_pending"`
}

func (v settingsProjectionView) keys() []string {
	out := make([]string, 0, len(v.Settings))
	for _, setting := range v.Settings {
		out = append(out, setting.Key)
	}
	return out
}

// The optional keys projection is the one server-side owner of the default
// Settings payload: a subset in schema order, never the request order, with
// the full schema count kept for the "more settings" note.
func TestSettingsKeysProjection(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.settings.views["ui.language"] = SettingView{
		Setting: &config.Setting{Key: "ui.language", Type: config.TypeString,
			Default: json.RawMessage(`"auto"`), Description: "interface language", Group: "Interface"},
		Value: "en", Layer: config.LayerFile,
	}
	// The fake's order is the schema order the production service exposes.
	w.settings.order = []string{"http.proxy_url", "mailboxes.selected", "time.zone", "ui.language"}
	wantTotal := len(w.settings.order)

	read := func(t *testing.T, path string) (int, []byte) {
		t.Helper()
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		return status, data
	}
	decode := func(t *testing.T, status int, data []byte) settingsProjectionView {
		t.Helper()
		mustStatus(t, status, http.StatusOK, data)
		var view settingsProjectionView
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatalf("not a settings view: %v: %s", err, data)
		}
		return view
	}
	equalKeys := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("keys %q, want %q", got, want)
			}
		}
	}

	t.Run("subset in schema order", func(t *testing.T) {
		status, data := read(t, "/api/settings?keys=ui.language,time.zone")
		view := decode(t, status, data)
		equalKeys(t, view.keys(), []string{"time.zone", "ui.language"})
		if view.Total != wantTotal {
			t.Fatalf("total %d, want %d", view.Total, wantTotal)
		}
		if len(view.RestartPending) != 1 || view.RestartPending[0] != "log.level" {
			t.Fatalf("restart_pending %v, want the full pending list", view.RestartPending)
		}
	})

	t.Run("duplicates collapse", func(t *testing.T) {
		status, data := read(t, "/api/settings?keys=time.zone,ui.language,time.zone")
		view := decode(t, status, data)
		equalKeys(t, view.keys(), []string{"time.zone", "ui.language"})
		if view.Total != wantTotal {
			t.Fatalf("total %d, want %d", view.Total, wantTotal)
		}
	})

	t.Run("trimmed and empty segments are ignored", func(t *testing.T) {
		path := "/api/settings?keys=" + url.QueryEscape(" time.zone , ,ui.language ")
		status, data := read(t, path)
		view := decode(t, status, data)
		equalKeys(t, view.keys(), []string{"time.zone", "ui.language"})
	})

	t.Run("empty keys is absent", func(t *testing.T) {
		status, fullData := read(t, "/api/settings")
		full := decode(t, status, fullData)
		status, emptyData := read(t, "/api/settings?keys=")
		empty := decode(t, status, emptyData)
		equalKeys(t, empty.keys(), full.keys())
		if empty.Total != wantTotal || full.Total != wantTotal {
			t.Fatalf("total full %d / empty %d, want %d", full.Total, empty.Total, wantTotal)
		}
	})

	t.Run("full response is unchanged", func(t *testing.T) {
		status, data := read(t, "/api/settings")
		view := decode(t, status, data)
		equalKeys(t, view.keys(), w.settings.order)
		if view.Total != wantTotal {
			t.Fatalf("total %d, want %d", view.Total, wantTotal)
		}
	})

	t.Run("unknown key is a bad parameter", func(t *testing.T) {
		status, data := read(t, "/api/settings?keys=time.zone,nope")
		mustStatus(t, status, http.StatusBadRequest, data)
		if code := errorCode(t, data); code != "invalid_keys" {
			t.Fatalf("code %q, want invalid_keys: %s", code, data)
		}
		body := decodeMap(t, data)
		params, _ := body["error"].(map[string]any)["params"].(map[string]any)
		if got, _ := params["key"].(string); got != "nope" {
			t.Fatalf("params.key %q, want nope: %s", got, data)
		}
	})

	t.Run("first unknown key wins", func(t *testing.T) {
		status, data := read(t, "/api/settings?keys=nope1,time.zone,nope2")
		mustStatus(t, status, http.StatusBadRequest, data)
		body := decodeMap(t, data)
		params, _ := body["error"].(map[string]any)["params"].(map[string]any)
		if got, _ := params["key"].(string); got != "nope1" {
			t.Fatalf("params.key %q, want nope1: %s", got, data)
		}
	})
}
