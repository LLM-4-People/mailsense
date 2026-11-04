// Package learning is the app-wide learning gate (user decision
// 2026-10-15): until the picked mailboxes are FULLY LEARNED nothing is
// allowed — no triage, no search, no Q&A; the app shows only the learning
// status. Each module that learns something reports its own Stage; the
// Machine combines them into one Status (stages, progress, ETA, blockers)
// and the Gate opens only when every stage is done. The gate never
// relaxes itself: a stage that is not built yet is a pending stage, and a
// stage that cannot finish reports a blocker with its reason and remedy.
package learning

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/selective"
)

// State is the state of one stage.
type State string

// Stage states.
const (
	Pending State = "pending" // not built yet, or waiting for an earlier stage
	Running State = "running" // progressing
	Blocked State = "blocked" // cannot progress without action (see Blockers)
	Done    State = "done"    // complete over every picked mailbox
)

// Blocker is one reason a stage cannot finish and the actions that fix
// it, all machine-readable: Code is stable (the UI maps it to its own
// wording), Subject names what is blocked (a store key, a question; never
// personal data), Actions are what the user can do (actions.go), and
// Detail is diagnostic text (an error from a source), never UI wording.
type Blocker struct {
	Code    string   `json:"code"`
	Subject string   `json:"subject,omitempty"`
	Actions []Action `json:"actions"`
	// Detail is the plain-language explanation shown to the user.
	Detail string `json:"detail,omitempty"`
	// Measure is the measured position behind the blocker (nil: none).
	Measure *Measure `json:"measure,omitempty"`
}

// Measure is what a blocker measured: what there is and what is needed,
// in Unit ("evaluation labels", "certified coverage", …).
type Measure struct {
	Have float64 `json:"have"`
	Need float64 `json:"need"`
	Unit string  `json:"unit"`
}

// Progress is a stage's measured position (Total 0 = unknown). Embedded,
// Unchanged and Failed optionally break down Done for a stage that reports
// the parts of its work (the semantic index while a run is active); other
// stages leave them zero.
type Progress struct {
	Done      int64  `json:"done"`
	Total     int64  `json:"total"`
	Unit      string `json:"unit,omitempty"`
	Embedded  int64  `json:"embedded,omitempty"`
	Unchanged int64  `json:"unchanged,omitempty"`
	Failed    int64  `json:"failed,omitempty"`
	// Stage and Detail are a running job's current phase: Stage is a stable
	// key the UI translates (jobs.stage.<stage>), Detail is data (a
	// question, a file). Both are empty outside a staged job.
	Stage  string `json:"stage,omitempty"`
	Detail string `json:"detail,omitempty"`
	// StepDone and StepTotal are the position within Stage, the same
	// numbers the job chip shows (0/0: unknown).
	StepDone  int64 `json:"step_done,omitempty"`
	StepTotal int64 `json:"step_total,omitempty"`
}

// QuestionCertification is one question's route certification in a stage
// report. Certification is embedded so its fields, including the per-class
// class_recall and class_collapsed report, serialize at this level.
type QuestionCertification struct {
	Question string `json:"question"`
	selective.Certification
}

// StageStatus is what one stage reports.
type StageStatus struct {
	ID       string    `json:"id"`
	State    State     `json:"state"`
	Progress Progress  `json:"progress"`
	Blockers []Blocker `json:"blockers,omitempty"`
	Note     string    `json:"note,omitempty"`
	// Certifications is the questions stage's route certification report,
	// one entry per question: the same verdict the stage judges, with the
	// per-class recall that explains a collapsed class.
	Certifications []QuestionCertification `json:"certifications,omitempty"`
	// ETA is filled by the Machine from the measured rate (nil: unknown).
	ETA *time.Duration `json:"eta,omitempty"`
}

// Stage is one part of learning, implemented by the module that owns it.
// Status must be cheap enough to run after every sync or index run.
type Stage interface {
	ID() string
	Status(ctx context.Context) (StageStatus, error)
}

// Progress units. The producer and its stage must name the same unit: the
// shell shows it verbatim next to the numbers, and a typo ("question") would
// quietly pass every test that only compares the count.
const (
	UnitMessages   = "messages"
	UnitQuestions  = "questions"
	UnitEvalLabels = "evaluation labels"
)

// Stage ids, in learning order (the Machine keeps the order it is given).
const (
	StageMailboxes   = "mailboxes"   // picked, readable, with an owner identity
	StageMachine     = "machine"     // the machine's facts confirmed (time zone)
	StageSync        = "sync"        // every picked message stored, complete and judged (scope)
	StageEvidence    = "evidence"    // structural evidence per message (evidence engine)
	StageEmbeddings  = "embeddings"  // a vector for every eligible message
	StageLabels      = "labels"      // teacher and judge labels on the required sample
	StageLayer1      = "layer1"      // per-question local heads trained
	StageCalibration = "calibration" // confidence calibrated, route chosen per question
	StageQuestions   = "questions"   // every question meets its requirement
)

// NotBuilt is a stage whose module does not exist yet: it is always
// pending, so the gate stays closed until the stage is built (it never
// counts as done).
func NotBuilt(id, what string) Stage { return notBuilt{id: id, what: what} }

type notBuilt struct{ id, what string }

func (n notBuilt) ID() string { return n.id }
func (n notBuilt) Status(context.Context) (StageStatus, error) {
	return StageStatus{ID: n.id, State: Pending, Note: "not built yet: " + n.what}, nil
}
