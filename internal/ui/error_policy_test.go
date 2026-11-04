package ui

import (
	"io/fs"
	"path"
	"strings"
	"testing"
)

// This file pins the one background-error policy at the HTTP transport. A
// module may hand an error to reportBackgroundError or ignorableBackground;
// it must not re-implement the ignorable-code list, and an inline surface
// must not add a second toast for the same failure.

func TestBackgroundErrorPolicyHasOneOwner(t *testing.T) {
	names, err := moduleNames("js", ".js")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		body, err := fs.ReadFile(Files, path.Join("js", name))
		if err != nil {
			t.Fatalf("js/%s: %v", name, err)
		}
		source := string(body)
		if name == "11-http.js" {
			for _, want := range []string{
				"function ignorableBackground(error)",
				`error.code === "unauthorized"`,
				`error.code === "shutting_down"`,
				`error.code === "network_error"`,
				"function reportBackgroundError(error)",
				"if (ignorableBackground(error)) return;",
			} {
				if !strings.Contains(source, want) {
					t.Errorf("js/11-http.js no longer owns %q", want)
				}
			}
			continue
		}
		for _, forbidden := range []string{
			`code === "unauthorized"`,
			`code !== "unauthorized"`,
			`code === "shutting_down"`,
			`code !== "shutting_down"`,
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("js/%s compares %s; use reportBackgroundError or ignorableBackground", name, forbidden)
			}
		}
	}
}

func TestBackgroundReportCallSites(t *testing.T) {
	for _, tc := range []struct {
		file string
		want int
	}{
		{"13-shell.js", 3},
		{"50-logs.js", 1},
		{"52-init.js", 1},
	} {
		body, err := fs.ReadFile(Files, path.Join("js", tc.file))
		if err != nil {
			t.Fatalf("js/%s: %v", tc.file, err)
		}
		if got := strings.Count(string(body), "reportBackgroundError("); got != tc.want {
			t.Errorf("js/%s has %d reportBackgroundError call(s), want %d", tc.file, got, tc.want)
		}
	}
}

// askErrorCard is the one visible surface of a failed Ask send. The retry
// action lives in the card, so a toast would be a duplicate report.
func TestAskFailurePaintsOnlyInline(t *testing.T) {
	body, err := fs.ReadFile(Files, "js/22-ask.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if got := strings.Count(source, "placement.answerSlot.append(askErrorCard(error))"); got != 1 {
		t.Fatalf("js/22-ask.js appends askErrorCard(error) %d time(s), want 1", got)
	}
	// The generic card body is apiFailure's message, so an unknown server
	// code, a network failure and a timeout all still say something.
	for _, want := range []string{
		"const failure = apiFailure(error);",
		`h("p", { text: failure.message })`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("js/22-ask.js askErrorCard lacks %q", want)
		}
	}
	for _, forbidden := range []string{"reportError(", "reportBackgroundError(", "toast("} {
		if strings.Contains(source, forbidden) {
			t.Errorf("js/22-ask.js contains %q; the inline error card is its only surface", forbidden)
		}
	}
}

func TestAnswersSkipsIgnorableBackgroundErrorsOnce(t *testing.T) {
	body, err := fs.ReadFile(Files, "js/27-answers.js")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "ignorableBackground(error)"); got != 2 {
		t.Errorf("js/27-answers.js guards %d inline error surface(s), want 2", got)
	}
}

// A supplementary read that fails must stay distinguishable from one that
// returned nothing. settleRead is the one helper that keeps the error, and
// each panel hands it to its own inline surface instead of catching to a
// success-shaped null or []. The permission snapshot and a screen's own
// reads share this rule: a failed gate refresh cannot hide the screen.
func TestSupplementaryReadsSettleExplicitly(t *testing.T) {
	http, err := fs.ReadFile(Files, "js/11-http.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"async function settleRead(promise)",
		"return { value: await promise, error: null };",
		"return { value: null, error };",
	} {
		if !strings.Contains(string(http), want) {
			t.Errorf("js/11-http.js settleRead lacks %q", want)
		}
	}
	for _, tc := range []struct{ file, want string }{
		{"13-shell.js", "function readGate()"},
		{"13-shell.js", "return settleRead(loadGate());"},
		{"13-shell.js", "function readLabelSummary()"},
		{"13-shell.js", `return settleRead(requestCached("/api/labels/summary"`},
		{"20-hub.js", "settleRead(loadAskHistory())"},
		{"20-hub.js", "readGate()"},
		{"22-ask.js", "readGate()"},
		{"23-learning.js", "readLabelSummary()"},
		{"25-sources.js", "settleRead(loadJobs())"},
		{"25-sources.js", "readGate()"},
		{"26-labels.js", "readLabelSummary()"},
		{"26-labels.js", "readGate()"},
		{"33-search.js", "settleRead(loadMailboxes())"},
	} {
		if !strings.Contains(readUIModule(t, tc.file), tc.want) {
			t.Errorf("js/%s does not settle the supplementary read with %q", tc.file, tc.want)
		}
	}
	// No screen owns a second copy of the label summary or the job snapshot:
	// both come from the boundary owner in 13-shell.js.
	for _, tc := range []struct{ file, forbidden string }{
		{"23-learning.js", `requestCached("/api/jobs"`},
		{"23-learning.js", `requestCached("/api/labels/summary"`},
		{"25-sources.js", `requestCached("/api/jobs"`},
		{"26-labels.js", `requestCached("/api/labels/summary"`},
	} {
		if strings.Contains(readUIModule(t, tc.file), tc.forbidden) {
			t.Errorf("js/%s keeps a second read owner (%q)", tc.file, tc.forbidden)
		}
	}
	// The historical success-shaped defaults these reads fed are gone.
	for _, tc := range []struct{ file, forbidden string }{
		{"20-hub.js", `.then(() => true, () => false)`},
		{"26-labels.js", `.catch(() => null)`},
		{"33-search.js", `.catch(() => [])`},
	} {
		if strings.Contains(readUIModule(t, tc.file), tc.forbidden) {
			t.Errorf("js/%s still converts a failed read to a success-shaped default (%q)", tc.file, tc.forbidden)
		}
	}
	// A failed read keeps its own slot while the panel's usable content stays.
	for _, tc := range []struct{ file, want string }{
		{"23-learning.js", "summaryRead.error"},
		{"20-hub.js", `t("hub.recent.error")`},
		{"26-labels.js", "labelSummaryUnavailable(summaryError)"},
		{"13-shell.js", `t("read.labels_unavailable")`},
		{"33-search.js", "mailboxesError"},
		{"25-sources.js", `t("read.jobs_unavailable")`},
		{"41-providers.js", "if (loadError) return;"},
	} {
		if !strings.Contains(readUIModule(t, tc.file), tc.want) {
			t.Errorf("js/%s has no inline failed-read surface (%q)", tc.file, tc.want)
		}
	}
}
