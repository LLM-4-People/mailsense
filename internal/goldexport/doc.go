// Package goldexport is the gold-judge sample of the whole chosen mailbox
// (scripts/bench-deciders.sh export): offline, seeded and stratified by
// dominant script, legacy category, sender kind and year, with rare legacy
// categories topped up to a floor; it never calls a provider ($0). The
// triage package feeds it the eligible mail and the model view
// (triage.Triager.ExportGold); this package owns the strata, the draw,
// the sender kinds, the legacy categories and the private files. Test and
// benchmark tooling: nothing in the app links it.
package goldexport
