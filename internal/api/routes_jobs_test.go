package api

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// The job detail carries the fields a detail view renders: stage, step
// position, ETA and the stable cause. A running job has the live progress; a
// finished job keeps its last stage and its cause.
func TestJobDetailCarriesStageStepETAAndCause(t *testing.T) {
	eta := 90 * time.Second
	started := time.Unix(1700000000, 0).UTC()
	cases := []struct {
		name string
		info jobs.Info
	}{
		{"running", jobs.Info{
			ID: "job-running", Kind: "sync", Name: "Outlook", Status: runstate.Running, StartedAt: started,
			Progress: jobs.Progress{Done: 2, Total: 5, Unit: "folders", Stage: "details", Detail: "item",
				StepDone: 3, StepTotal: 7, ETA: &eta},
		}},
		{"finished", jobs.Info{
			ID: "job-finished", Kind: "sync", Name: "Outlook", Status: runstate.Failed, StartedAt: started,
			FinishedAt: started.Add(time.Minute), Cause: "outlook_not_running",
			Progress: jobs.Progress{Done: 5, Total: 5, Unit: "folders", Stage: "details", StepDone: 5, StepTotal: 5}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts, w := newTestServer(t)
			w.jobs.list = []jobs.Info{tc.info}
			status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/jobs/" + tc.info.ID})
			mustStatus(t, status, 200, data)
			body := decodeMap(t, data)
			progress, _ := body["progress"].(map[string]any)
			if progress["stage"] != tc.info.Progress.Stage {
				t.Fatalf("progress %#v", progress)
			}
			if tc.info.Progress.StepTotal != 0 &&
				(progress["step_done"] != float64(tc.info.Progress.StepDone) ||
					progress["step_total"] != float64(tc.info.Progress.StepTotal)) {
				t.Fatalf("step %#v", progress)
			}
			if tc.info.Progress.ETA != nil {
				if got, ok := progress["eta"]; !ok || got != float64(tc.info.Progress.ETA.Nanoseconds()) {
					t.Fatalf("eta %#v, want %d", got, tc.info.Progress.ETA.Nanoseconds())
				}
			}
			if tc.info.Cause != "" && body["cause"] != tc.info.Cause {
				t.Fatalf("cause %#v", body["cause"])
			}
		})
	}
}
