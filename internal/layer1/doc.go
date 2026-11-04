// Package layer1 is the local per-install classifier (docs/LAYER1.md):
// per triage question, a linear head (multinomial, binary or ordinal
// logistic regression, fitted with L-BFGS) over the message's embedding
// and its causal layer-0 features, trained on soft teacher targets
// (the configured decider's answers, measured rules, labels) whose
// sources and precedence are data, cross-fitted on the install's
// evaluation labels (grouped by conversation), optionally pooled with a
// causal kNN distribution and calibrated (temperature), then measured
// against per-question requirements (data). It answers every eligible
// message locally (decider "layer1") and is the learning stage "layer1".
package layer1

import "github.com/LLM-4-People/mailsense/internal/logx"

var log = logx.For("layer1")
