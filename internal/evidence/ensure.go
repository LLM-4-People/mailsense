package evidence

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Ensure starts one run when the newest run does not read the current
// inputs. The automatic trigger is event-driven (a sync, an index run that
// committed, a settings or data-file change); a restart fires none of them,
// so a run needed by a change made while the app was down would never start
// and the stage would report "running" forever. A run that already reads the
// current inputs and succeeded or failed is left alone, so a failure the
// owner must review is not retried in a loop; a cancelled or interrupted run
// is started again.
func (e *Engine) Ensure(ctx context.Context, m *jobs.Manager) (*jobs.Job, error) {
	inputs, _, err := e.stage.current(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := e.st.ListEvidenceRuns(ctx, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) > 0 {
		switch {
		case runs[0].Inputs != inputs:
			// The newest run read other inputs: this one is owed.
		case runs[0].State == runstate.Succeeded:
			return nil, nil
		case runs[0].State == runstate.Failed:
			// A failure over the current inputs is the owner's to review;
			// retrying it in a loop would hide it.
			return nil, nil
		case runs[0].State == runstate.Running:
			// A run is live (the startup cleanup closes the ones a previous
			// process left open), so it will finish this work itself.
			return nil, nil
		}
	}
	return e.Start(m)
}
