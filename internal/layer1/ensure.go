package layer1

import "github.com/LLM-4-People/mailsense/internal/jobs"

// Ensure asks for the run the current inputs need through the same gate as
// every trigger. A restart fires no evidence, index, label or settings event,
// so a feature-version upgrade or a change made while the app was down would
// otherwise leave the stage reporting "its inputs changed" forever.
func (r *Runner) Ensure(m *jobs.Manager) {
	r.startAfter(m, "startup")
}
