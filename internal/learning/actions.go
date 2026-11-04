package learning

// Action is one thing the user can do about a blocker, with its
// arguments: the UI offers it and calls the exposed function that
// implements it (named per action below).
type Action struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params,omitempty"`
}

// Action ids (one vocabulary for every stage).
const (
	// ActionPickMailboxes: pick stores from the discovered list
	// (syncer.Syncer.Pick).
	ActionPickMailboxes = "pick_mailboxes"
	// ActionUnpickMailbox: take a store out of the selection
	// (syncer.Syncer.Unpick; param "key").
	ActionUnpickMailbox = "unpick_mailbox"
	// ActionDiscover: list the stores of a source again
	// (syncer.Syncer.Discover; param "key").
	ActionDiscover = "discover_mailboxes"
	// ActionReconnect: bring the store's source online (network, Outlook
	// connected, access rights), then sync (param "key").
	ActionReconnect = "reconnect_mailbox"
	// ActionDeclareAddress: declare an owner address of a store
	// (syncer.Syncer.Declare; param "key").
	ActionDeclareAddress = "declare_address"
	// ActionConfirmTimeZone: confirm the owner's time zone
	// (learning.ConfirmZone; params: the machine's proposal).
	ActionConfirmTimeZone = "confirm_time_zone"
	// ActionChooseEmbedder: install the local runtime or select another
	// embedding provider (settings embed.provider, sidecar.*).
	ActionChooseEmbedder = "choose_embedder"
	// ActionReviewFailures: look at the failures (param "log": the log
	// component), then retry (a changed message is retried).
	ActionReviewFailures = "review_failures"
	// ActionRunEvidence: run the evidence engine again
	// (evidence.Engine.Start).
	ActionRunEvidence = "run_evidence"
	// ActionStartSource: start the source's application as the user would
	// (outlook.Connector.StartOutlook); background work never starts it.
	ActionStartSource = "start_source"
	// ActionResumeSource: resume a source whose connector stopped after
	// repeated lost connections (outlook.Connector.Resume).
	ActionResumeSource = "resume_source"
	// ActionRetryDetails: read again the messages whose detail read made
	// the source fail and that were set aside (syncer.Syncer.RetryDetails;
	// param "source").
	ActionRetryDetails = "retry_details"
	// ActionLabelMessages: label messages for a question (feedback.Labeler;
	// params "question" and, when known, "count": the labels still wanted).
	ActionLabelMessages = "label_messages"
	// ActionRunJudge: have the judge label messages for a question (the
	// Gemma layer; layer1.RunJudge; params "question", "provider" and
	// "count" and, as every paid action, "estimated_cost_usd": a number,
	// or "unknown" with "cost_note" saying why).
	ActionRunJudge = "run_judge"
	// ActionChooseTeacher: choose or configure a teacher source for a
	// question (layer1.teachers.file, triage.decider; param "question").
	ActionChooseTeacher = "choose_teacher"
	// ActionReviewRequirements: review a question's requirement
	// (layer1.requirements.file; param "question").
	ActionReviewRequirements = "review_requirements"
	// ActionRunLayer1: train layer 1 again (layer1.Runner.Start).
	ActionRunLayer1 = "run_layer1"
)

// Do builds an action.
func Do(id string, params ...string) Action {
	a := Action{ID: id}
	for i := 0; i+1 < len(params); i += 2 {
		if a.Params == nil {
			a.Params = map[string]string{}
		}
		a.Params[params[i]] = params[i+1]
	}
	return a
}
