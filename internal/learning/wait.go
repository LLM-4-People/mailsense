package learning

import "context"

// WaitFor reports whether every prerequisite stage is done. A stage that
// waits on another one says so the same way everywhere: pending, with the
// name of the stage it is waiting for. Three stage implementations repeated
// the loop; a change of the wording or of the memoization had to be made
// three times.
func WaitFor(ctx context.Context, after ...Stage) (StageStatus, bool, error) {
	for _, a := range after {
		st, err := StatusOf(ctx, a)
		if err != nil {
			return StageStatus{}, false, err
		}
		if st.State != Done {
			return StageStatus{State: Pending, Note: "waits for the " + a.ID() + " stage"}, false, nil
		}
	}
	return StageStatus{}, true, nil
}
