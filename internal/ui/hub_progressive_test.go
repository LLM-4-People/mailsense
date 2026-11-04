package ui

import (
	"strings"
	"testing"
)

// Home's shell no longer waits for the discarded learning read, and the
// priority and agenda columns start together instead of queueing behind one
// Promise.allSettled.
func TestHomeColumnsFillInPlace(t *testing.T) {
	hub := readUIModule(t, "20-hub.js")
	render := functionBody(t, hub, "async function renderHub(")
	if !strings.Contains(render, "settleRead(loadLearning())") {
		t.Fatal("renderHub does not start the learning read")
	}
	if strings.Contains(render, "await loadLearning") ||
		strings.Contains(render, "const [view, gateRead] = await Promise.all([\n      loadLearning") {
		t.Fatal("Home still waits for the learning read")
	}
	if strings.Contains(render, "Promise.allSettled([") {
		t.Fatal("Home still blocks first paint on priority and agenda")
	}

	columns := functionBody(t, hub, "function hubColumns(")
	for _, want := range []string{
		"settleRead(loadPriority(\"all\", PRIORITY_HOME_SIZE))",
		"settleRead(loadAgenda(state.agendaHours))",
		"priorityRead.then(",
		"agendaRead.then(",
		"drawImportant();",
		"drawAgenda(agendaView, agendaError);",
		"drawBrief();",
	} {
		if !strings.Contains(columns, want) {
			t.Errorf("hubColumns lacks %q", want)
		}
	}
	if strings.Index(columns, "priorityRead.then(") > strings.Index(columns, "return { countsHost, grid }") {
		t.Fatal("the priority fill is registered after the grid is returned")
	}
	if strings.Contains(columns, "seq !== state.renderSeq") {
		t.Fatal("a read that started with a cached Home is dropped when the owner navigates away")
	}

	for _, tc := range []struct{ signature, want string }{
		{"function homeBriefSection(", "body: loading ? homeSectionLoading() :"},
		{"function homeImportantSection(", "body = loading"},
	} {
		if body := functionBody(t, hub, tc.signature); !strings.Contains(body, tc.want) {
			t.Errorf("%s has no loading body", tc.signature)
		}
	}
	loading := functionBody(t, hub, "function homeSectionLoading(")
	if !strings.Contains(loading, `"aria-busy": "true"`) {
		t.Fatal("a Home loading body has no busy state")
	}
	if strings.Contains(loading, `role: "status"`) {
		t.Fatal("three Home columns announce a loading status at once")
	}
}

// The home section still names the four content builders and keeps the More
// rail. This is the guard that the progressive split did not drop a column.
func TestHomeStillOwnsEveryColumn(t *testing.T) {
	hub := readUIModule(t, "20-hub.js")
	for _, want := range []string{
		`h("div", { class: "home-brief-grid" }`,
		"homeRecentSection(",
		"homeLearningSection(view)",
		"homeMailboxSection(view)",
		`class: "surface home-more"`,
		`showPreview ? previewBanner(t("preview.home_body")`,
	} {
		if !strings.Contains(hub, want) {
			t.Errorf("20-hub.js lacks %q", want)
		}
	}
}
