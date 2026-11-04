package feedback

import "github.com/LLM-4-People/mailsense/internal/store"

// Label sources decide who may teach and who may be trusted as reference.
// The owner's manual labels and the judge's independent reference labels are
// teaching and evaluation truth. The judge's **production answer**
// (store.LabelJudgeAnswer) is the model's own output, written for audit; it
// never becomes an example, a prior, or ground truth. Excluding it is the
// whole point: a model must not learn from answers it produced itself.
// Every consumer applies this policy here and never compares a source at its
// own call site; Load deliberately returns every source so the evaluation can
// report the per-source counts.
//
// If a product decision ever admits another source, it belongs in Teaching
// or Reference below, not at a consumer.

// Teaching returns the labels that may be shown to the model as examples or
// used as priors: the owner's manual labels and the judge's reference labels.
// The judge's production answer is never teaching input. Call it once at a
// load boundary (the triage core does), not per message: the per-message
// selectors take this filtered set.
func Teaching(labels []Label) []Label {
	return filterLabels(labels, referenceSource)
}

// Reference returns at most one independent label per (message, question):
// the owner's manual label outranks the judge's independent reference label,
// matching the trainer's evaluation precedence. The judge's production answer
// is never ground truth. The resolved label keeps the input position of its
// pair's first label, so evaluation lists stay deterministic whatever order
// the sources were loaded in; SourceCounts still reports every loaded source
// before this resolution.
func Reference(labels []Label) []Label {
	kept := filterLabels(labels, referenceSource)
	seen := make(map[labelKey]struct{}, len(kept))
	for _, l := range kept {
		k := labelKey{MessageID: l.MessageID, Question: l.Question}
		if _, dup := seen[k]; dup {
			return resolveReference(kept)
		}
		seen[k] = struct{}{}
	}
	return kept
}

// resolveReference returns one label per (message, question): each pair
// keeps its first label's position, and a later manual label replaces the
// judge's value there. Only Reference labels reach it.
func resolveReference(kept []Label) []Label {
	out := make([]Label, 0, len(kept))
	at := make(map[labelKey]int, len(kept))
	for _, l := range kept {
		k := labelKey{MessageID: l.MessageID, Question: l.Question}
		if i, dup := at[k]; dup {
			if referenceRank(l.Source) < referenceRank(out[i].Source) {
				out[i] = l
			}
			continue
		}
		at[k] = len(out)
		out = append(out, l)
	}
	return out
}

// labelKey is one message's question: the unit Reference resolves.
type labelKey struct {
	MessageID int64
	Question  string
}

// referenceRank is the precedence of the reference sources for one pair:
// the owner's manual label (0) outranks the judge's independent reference
// label (1), as the trainer's evaluation labels do. Only the sources that
// pass referenceSource reach it.
func referenceRank(source string) int {
	if source == store.LabelManual {
		return 0
	}
	return 1
}

// referenceSource is the one predicate of both policies today: independent
// labels, never the production answer.
func referenceSource(source string) bool {
	return source == store.LabelManual || source == store.LabelJudge
}

// SourceCounts reports how many labels came from each known source. Every
// known source is present, zero when it has none, so a quality report shows
// that a deployment has no manual labels instead of hiding the key. It
// counts identifiers only: never a prompt or a message body.
func SourceCounts(labels []Label) map[string]int {
	out := make(map[string]int, len(labelSources))
	for _, source := range labelSources {
		out[source] = 0
	}
	for _, l := range labels {
		if _, known := out[l.Source]; known {
			out[l.Source]++
		}
	}
	return out
}

// labelSources is the source vocabulary the quality report always names.
// store owns the values; this is only the reporting order/content.
var labelSources = [...]string{store.LabelManual, store.LabelJudge, store.LabelJudgeAnswer}

// filterLabels is the one filter implementation of the policy; keep is the
// policy predicate of the caller. All-kept input is returned as is: the
// common deployment has no production answers in its label set, so a load
// boundary filter copies nothing.
func filterLabels(labels []Label, keep func(source string) bool) []Label {
	for _, l := range labels {
		if !keep(l.Source) {
			out := make([]Label, 0, len(labels)-1)
			for _, l := range labels {
				if keep(l.Source) {
					out = append(out, l)
				}
			}
			return out
		}
	}
	return labels
}
