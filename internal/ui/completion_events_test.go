package ui

import (
	"strings"
	"testing"
)

// The two finished-run topics each own one listener, and both go through the
// same topic graph instead of re-implementing invalidation, staling and
// refresh.
func TestCompletionEventsShareOneReaction(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`source.addEventListener("sync.finished"`,
		`source.addEventListener("triage.finished"`,
		`if (!syncChangedMail(report)) return;`,
		`invalidateTopics(["hub"])`,
		`invalidateTopics(["mail"])`,
		`if (!triageChangedMail(report)) return;`,
		`invalidateTopics(["triage"])`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks %q", want)
		}
	}

	reaction := functionBody(t, shell, "function invalidateTopics(")
	for _, want := range []string{
		"invalidateCache([...reads])",
		"invalidateRouteCache(...names)",
		"staleRouteCache(...names)",
		"scheduleDataRefresh(...targets)",
	} {
		if !strings.Contains(reaction, want) {
			t.Errorf("invalidateTopics lacks %q", want)
		}
	}
	for _, handler := range []string{
		`source.addEventListener("sync.finished"`,
		`source.addEventListener("triage.finished"`,
	} {
		body := handlerBody(t, shell, handler)
		if !strings.Contains(body, `try { report = JSON.parse(event.data); } catch { report = null; }`) {
			t.Errorf("%s does not parse its report defensively", handler)
		}
		if strings.Contains(body, "invalidateCache(") || strings.Contains(body, "refreshActiveRoute(") {
			t.Errorf("%s still spells its own invalidation or refresh", handler)
		}
	}
}

// A finished run only refreshes the screens when it changed their data.
func TestCompletionEventsActOnlyWhenMailChanged(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	sync := functionBody(t, shell, "function syncChangedMail(")
	for _, want := range []string{
		"eventCount(totals.written) > 0",
		"eventCount(removed.deleted) + eventCount(removed.moved) > 0",
		"eventCount(scope.judged) > 0",
		"eventCount(details.completed) > 0",
	} {
		if !strings.Contains(sync, want) {
			t.Errorf("syncChangedMail lacks %q", want)
		}
	}
	triage := functionBody(t, shell, "function triageChangedMail(")
	if !strings.Contains(triage, "eventCount(report && report.decided) + eventCount(report && report.partial) > 0") {
		t.Error("triageChangedMail does not require a stored decision")
	}
}

// A sync can add, remove or rewrite mail; triage only changes verdicts. The
// read and surface lists are split accordingly, and only the sync may forget
// the All-Mail local model.
func TestCompletionInvalidationTablesAreSplitByWhatChanged(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	syncReads := jsConstList(t, shell, "SYNC_FINISHED_READS")
	for _, want := range []string{
		"/api/messages?", "/api/messages/", "/api/conversations", "/api/companies",
		"/api/priority", "/api/hub", "/api/triage", "/api/brief", "/api/agenda",
		"/api/commitments", "/api/labels", "/api/status", "/api/learning",
	} {
		if !containsString(syncReads, want) {
			t.Errorf("the sync read set lacks %q", want)
		}
	}
	triageReads := jsConstList(t, shell, "TRIAGE_FINISHED_READS")
	for _, want := range []string{
		"/api/triage", "/api/priority", "/api/conversations", "/api/agenda",
		"/api/brief", "/api/companies", "/api/hub", "/api/labels",
	} {
		if !containsString(triageReads, want) {
			t.Errorf("the triage read set lacks %q", want)
		}
	}
	for _, forbidden := range []string{"/api/messages?", "/api/messages/", "/api/status", "/api/learning"} {
		if containsString(triageReads, forbidden) {
			t.Errorf("the triage read set must not drop %q", forbidden)
		}
	}

	syncRoutes := jsStringArrayField(t, topicBody(t, shell, "mail"), "routes")
	if !containsString(syncRoutes, "mail") {
		t.Error("the sync surface set does not refresh Mail")
	}
	triageRoutes := jsStringArrayField(t, topicBody(t, shell, "triage"), "routes")
	if containsString(triageRoutes, "mail") {
		t.Error("the triage surface set refreshes Mail even though membership did not change")
	}
	for _, want := range []string{"conversations", "companies", "hub", "brief", "triage", "priority", "agenda"} {
		if !containsString(triageRoutes, want) {
			t.Errorf("the triage surface set lacks %q", want)
		}
	}
	if !strings.Contains(handlerBody(t, shell, `source.addEventListener("sync.finished"`), `invalidateTopics(["mail"])`) {
		t.Error("the sync listener does not use the mail topic")
	}
	if !strings.Contains(handlerBody(t, shell, `source.addEventListener("triage.finished"`), `invalidateTopics(["triage"])`) {
		t.Error("the triage listener does not use the triage topic")
	}
}

// A reconnect is the one repair pass that must re-base every live read. The
// agenda and triage reads were the two it omitted, and the All-Mail model
// survived the cache drop.
func TestLiveResyncCoversAgendaTriageAndAllMail(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	resync := functionBody(t, shell, "function scheduleLiveResync(")
	for _, want := range []string{
		`"mail"`, `"triage"`, `"route"`, `"runtime"`, "forgetMail: false", "forgetAllMail();",
	} {
		if !strings.Contains(resync, want) {
			t.Errorf("scheduleLiveResync lacks %q", want)
		}
	}
	for _, topic := range []string{"mail", "triage"} {
		body := topicBody(t, shell, topic)
		if !strings.Contains(body, "forgetMail:") && !strings.Contains(body, `reads: SYNC_FINISHED_READS`) &&
			!strings.Contains(body, `reads: TRIAGE_FINISHED_READS`) {
			t.Errorf("the %s topic does not declare its read owner", topic)
		}
	}
	for _, reads := range [][]string{
		jsConstList(t, shell, "SYNC_FINISHED_READS"),
		jsConstList(t, shell, "TRIAGE_FINISHED_READS"),
	} {
		if !containsString(reads, "/api/agenda") {
			t.Error("the mail completion read set does not re-base the agenda")
		}
	}
	if !containsString(jsConstList(t, shell, "TRIAGE_FINISHED_READS"), "/api/triage") {
		t.Error("the triage read set does not re-base triage")
	}
	if !strings.Contains(topicBody(t, shell, "mail"), "forgetMail: true") {
		t.Error("the mail topic does not forget the All-Mail model")
	}
}

// The broad write invalidation is the owner that also drops the local
// All-Mail model. The learning-only list must not: a label write does not
// change list membership.
func TestBroadReadInvalidationForgetsAllMail(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	broad := functionBody(t, shell, "function invalidateAllReads(")
	if !strings.Contains(broad, "forgetAllMail();") {
		t.Error("invalidateAllReads leaves the All-Mail model behind")
	}
	learning := functionBody(t, shell, "function invalidateLearningReads(")
	if strings.Contains(learning, "forgetAllMail(") {
		t.Error("invalidateLearningReads forgets list membership on a label write")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
