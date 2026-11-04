// Package selective is the one definition of when an answer may be given
// on its own (docs/LAYER1.md, "Abstain and escalate"; lead directive "1.0
// accuracy"): the confidence of an answer per answer kind, the ranking of
// answers by confidence, the coverage at precision targets (point estimate
// and Wilson lower bound), the honest split certification of a decision
// rule (a threshold chosen on one part of the labels, or a fixed rule cut
// elsewhere, tested on a disjoint part), and the abstained answer form,
// which carries no value a consumer could use by mistake. Layer 1, the
// feedback escalation calibration and the triage profile's uncertainty all
// read it. Pure; no I/O.
package selective
