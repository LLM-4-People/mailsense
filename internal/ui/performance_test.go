package ui

import (
	"io/fs"
	"strings"
	"testing"
)

func readApp(t *testing.T) string {
	t.Helper()
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(script)
}

// Typing in the Logs component filter must not rebuild the thousand-row
// table on every character.
func TestLogsFilterIsDebounced(t *testing.T) {
	js := readApp(t)
	start := strings.Index(js, "function renderLogs(route, seq)")
	if start < 0 {
		t.Fatal("renderLogs not found")
	}
	end := strings.Index(js[start:], "\n  }")
	if end <= 0 {
		t.Fatal("renderLogs has no end")
	}
	body := js[start : start+end]
	if !strings.Contains(body, "scheduleFilteredRender") || !strings.Contains(body, "150") {
		t.Fatal("the Logs component filter is not debounced")
	}
	if !strings.Contains(body, "tableHost.isConnected") {
		t.Fatal("a debounced filter can rebuild a detached table")
	}
	trim := functionBody(t, js, "function trimLogEntries(")
	if !strings.Contains(trim, "MAX_LOG_ENTRIES + 256") {
		t.Fatal("the log ring still sorts on every insert")
	}
}

// A label or settings write must keep the mounted screens and revalidate;
// only the markup that is genuinely wrong may be dropped.
func TestWritesKeepMountedScreens(t *testing.T) {
	js := readApp(t)
	labels := functionBody(t, js, "function afterLabelWrite(")
	if strings.Contains(labels, "invalidateLearningDerived(") {
		t.Fatal("a label write still drops every cached screen")
	}
	if !strings.Contains(labels, `invalidateTopics(["labels"]`) {
		t.Fatal("a label write does not go through the labels topic")
	}
	if !strings.Contains(labels, "{ hard: true") {
		t.Fatal("a label write leaves the completed workbench cached")
	}
	for _, forbidden := range []string{"invalidateLearningReads(", "staleRouteCache(", "invalidateRouteCache("} {
		if strings.Contains(labels, forbidden) {
			t.Errorf("a label write still spells its own invalidation via %q", forbidden)
		}
	}
	start := strings.Index(js, "const commitSetting = async (event")
	if start < 0 {
		t.Fatal("commitSetting not found")
	}
	end := strings.Index(js[start:], "\n    };")
	if end <= 0 {
		t.Fatal("commitSetting has no end")
	}
	commit := js[start : start+end]
	if strings.Contains(commit, "invalidateAllData()") {
		t.Fatal("a settings write still drops every cached screen")
	}
	if !strings.Contains(commit, "invalidateAllReads()") || !strings.Contains(commit, "staleRouteCache()") {
		t.Fatal("a settings write does not invalidate reads and stale the mounted screens")
	}
}

// Independent screen reads start together, a repeat queue read is cached, and
// the question catalogue is invalidated with the learning reads.
func TestScreenReadsAreBatchedAndCached(t *testing.T) {
	js := readApp(t)
	sources := functionBody(t, js, "async function renderSources(")
	if !strings.Contains(sources, "Promise.all([") {
		t.Fatal("Sources still reads its four independent views in series")
	}
	// The job snapshot and the gate come from their owners; Sources must not
	// issue a second /api/jobs read or await the gate unguarded.
	if !strings.Contains(sources, "settleRead(loadJobs())") {
		t.Fatal("Sources does not read the shared job snapshot through loadJobs")
	}
	if !strings.Contains(sources, "readGate()") {
		t.Fatal("Sources awaits the gate outside the settled permission read")
	}
	if strings.Contains(sources, `requestCached("/api/jobs"`) {
		t.Fatal("Sources still owns a second /api/jobs read")
	}
	learning := functionBody(t, js, "async function renderLearning(")
	if !strings.Contains(learning, "jobSnapshot()") {
		t.Fatal("Learning does not read the shared job model")
	}
	if strings.Contains(learning, `requestCached("/api/jobs"`) {
		t.Fatal("Learning still owns a second /api/jobs read")
	}
	labels := functionBody(t, js, "async function loadQueue(")
	if !strings.Contains(labels, "requestCached(") || !strings.Contains(labels, "CACHE_TTL.labelsQueue") {
		t.Fatal("the Labels queue is still an uncached read")
	}
	list := functionBody(t, js, "function invalidateLearningReads(")
	if !strings.Contains(list, `invalidateTopics(["learning"]`) {
		t.Fatal("the shared learning read invalidation bypasses the topic graph")
	}
	if !strings.Contains(topicBody(t, js, "learning"), `"/api/questions"`) {
		t.Fatal("a profile or configuration write leaves the question catalogue cached")
	}
}

// A Logs remount is a delta read, not another 500-entry page.
func TestLogsRemountReadsSinceTheCursor(t *testing.T) {
	js := readApp(t)
	body := functionBody(t, js, "async function renderLogs(")
	if !strings.Contains(body, "request(logPagePath(since))") {
		t.Fatal("a Logs remount still re-reads the whole recent page")
	}
	if !strings.Contains(body, "request(logPagePath(0))") {
		t.Fatal("the delta read does not resync after the ring rotated past the cursor")
	}
	if !strings.Contains(body, "snapshot.oldest_seq) > since + 1") {
		t.Fatal("the delta read does not check the ring bounds after the cursor")
	}
	if strings.Contains(body, "limit=") {
		t.Fatal("the Logs client still owns the page size instead of the server")
	}
}

// A sync changes list membership, so the next All-Mail mount refetches the
// list while an already stored body stays cached. A display-HTML pass changes
// only the detail body. The gate is read fresh per navigation, with concurrent
// renders sharing one in-flight request.
func TestMailDataChangesForgetTheLocalListModel(t *testing.T) {
	js := readApp(t)
	for _, want := range []string{
		`invalidateTopics(["mailboxes"])`,
		`forgetAllMail();`,
		`if (terminal && info.kind === "messages.html")`,
		`request("/api/gate")`,
		`"/api/questions", "/api/gate"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the mail data-change path lacks %q", want)
		}
	}
	mailboxes := topicBody(t, js, "mailboxes")
	for _, want := range []string{
		`"/api/mailboxes"`, `"/api/status"`, `"/api/learning"`, `"/api/hub"`, `"/api/messages?"`,
	} {
		if !strings.Contains(mailboxes, want) {
			t.Errorf("the mailbox topic lacks %q", want)
		}
	}
	if strings.Contains(mailboxes, `"/api/messages/"`) {
		t.Error("a mailbox sync drops the stored message bodies too")
	}
}
