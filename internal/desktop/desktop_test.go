package desktop

import "testing"

func TestLocalURL(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:54321/#token=abcDEF123",
		"http://127.0.0.1:1/#token=x",
	}
	for _, raw := range valid {
		if got, err := localURL(raw); err != nil || got != raw {
			t.Errorf("localURL(%q) = %q, %v", raw, got, err)
		}
	}
	invalid := map[string]string{
		"empty":          "",
		"https":          "https://127.0.0.1:54321/#token=x",
		"remote host":    "http://example.com:54321/#token=x",
		"localhost":      "http://localhost:54321/#token=x",
		"no port":        "http://127.0.0.1/#token=x",
		"bad port":       "http://127.0.0.1:70000/#token=x",
		"credentials":    "http://user:pass@127.0.0.1:54321/#token=x",
		"query":          "http://127.0.0.1:54321/?x=1#token=x",
		"path":           "http://127.0.0.1:54321/admin#token=x",
		"no token":       "http://127.0.0.1:54321/",
		"empty token":    "http://127.0.0.1:54321/#token=",
		"other fragment": "http://127.0.0.1:54321/#/mail",
		"file scheme":    "file:///tmp/index.html",
	}
	for name, raw := range invalid {
		if _, err := localURL(raw); err == nil {
			t.Errorf("%s: localURL(%q) accepted", name, raw)
		}
	}
}
