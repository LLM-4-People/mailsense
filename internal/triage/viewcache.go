package triage

// viewKey identifies one rendered triage read model. Every input that
// changes the result is part of it: the store generations that cover the
// mail, labels, mailbox selection and route answers, the explicit test
// preview, the config version (every setting and the profile, rules and
// provider files), the profile the route rows are stored under, the decider
// key the response names, and the page bounds.
type viewKey struct {
	gen         uint64 // store.MailGeneration: mail, folders, selection (labels are not read)
	routeGen    uint64 // store.RouteGeneration: route and preview answers
	preview     bool   // learning.test_mode
	version     string // config version: settings and the data files
	fingerprint string // the paid-decision identity (profile fingerprint)
	routeKey    string // the route key the stored answers live under
	decider     string // name/model/fingerprint of the configured decider
	limit       int    // page size (0 for a summary)
	offset      int    // page offset (0 for a summary)
}

// viewIdentity is the profile fingerprint and decider key a response
// reports. It is cached under the config version, so a warm page or summary
// serves the snapshot without reloading the profile file and the provider
// catalog on every click.
type viewIdentity struct {
	key         string
	fingerprint string
	routeKey    string
	decider     string
}
