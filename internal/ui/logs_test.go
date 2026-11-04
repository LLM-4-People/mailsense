package ui

import (
	"strings"
	"testing"
)

// TestLogsLiveWakeIsAReference: the event stream carries a cursor, and only
// the mounted Logs screen turns it into one coalesced since read. The
// omnibox asks for text-only suggestions, so typing never embeds.
func TestLogsLiveWakeIsAReference(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`source.addEventListener("log.wake", handleLogWake);`,
		`function handleLogWake(event)`,
		`if (state.route !== "logs") return;`,
		`scheduleLogRefresh();`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks %q", want)
		}
	}
	logs := readUIModule(t, "50-logs.js")
	for _, want := range []string{
		`function logPagePath(since)`,
		`"/api/logs?since=" + String(since) + "&attrs=off"`,
		`"/api/logs?attrs=off"`,
		`mergeLogSnapshot(snapshot, true);`,
		`function scheduleLogRefresh()`,
		`state.logLast = Math.max(state.logLast || 0, Number(snapshot.last_seq) || 0);`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("50-logs.js lacks %q", want)
		}
	}
	// One expanded row reads its own entry once; the guard also carries the
	// in-flight and rotated-out markers across a page merge. Only a 404 is
	// "gone": a transport failure keeps the retry.
	for _, want := range []string{
		`const detail = await request("/api/logs/" + String(seq));`,
		`entry.has_attrs !== true || entry.attrs !== undefined`,
		`if (held.attrsLoading) entry.attrsLoading = true;`,
		`if (held.attrsGone) entry.attrsGone = true;`,
		`if (held.attrsError) entry.attrsError = held.attrsError;`,
		`if (error instanceof ApiError && error.status === 404) held.attrsGone = true;`,
		`else held.attrsError = error;`,
		`content = retryBanner(apiFailure(entry.attrsError).message, () => retryLogAttrs(entry.seq));`,
		`if (entry.attrsGone) content = h("pre", { text: t("logs.attrs_gone") });`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("50-logs.js lacks the lazy-attribute step %q", want)
		}
	}
	search := readUIModule(t, "33-search.js")
	if !strings.Contains(search, `"&limit=" + GLOBAL_SEARCH_LIMIT + "&semantic=off"`) {
		t.Error("the omnibox does not ask for text-only suggestions")
	}
	if strings.Contains(search, `params.push("semantic=off")`) {
		t.Error("the explicit Search page was made text-only")
	}
}

// TestLogsRenderStoredAttributeValues: the expanded Logs row prints every
// stored attribute value as it was written by logx. A content-key value is
// already the redaction marker by then, so the UI shows "[REDACTED]" without
// needing a second policy (there is one owner: internal/logx).
func TestLogsRenderStoredAttributeValues(t *testing.T) {
	script := readAsset(t, "app.js")
	for _, fragment := range []string{
		`Object.keys(attrs).sort().map((key) => key + "=" + jsonValue(attrs[key])).join("\n")`,
		`if (typeof value === "string") return value;`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("app.js no longer renders stored log attributes verbatim: missing %q", fragment)
		}
	}
}
