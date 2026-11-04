package main

import (
	"context"
)

// cacheCloser is one read model's bounded shutdown join. It reports whether
// every detached rebuild it owns finished before its context ended.
type cacheCloser func(context.Context) bool

// readModelClosers collects the shutdown join of the read models that expose
// one. Each of them starts a detached rebuild the API's handler join cannot
// see, because the request that started it has already returned; the cache
// owner is the only place that can wait for it before the store closes.
//
// A model built on first use and never requested has no goroutine to join, so
// its closer reports true. A model that does not expose the join is simply
// absent, which keeps the caller's list limited to its real owners;
// readModelCloserList is that list.
func readModelClosers(models ...any) []cacheCloser {
	out := make([]cacheCloser, 0, len(models))
	for _, model := range models {
		if closer, ok := model.(interface{ Close(context.Context) bool }); ok {
			out = append(out, closer.Close)
		}
	}
	return out
}

// readModelCloserList is the one list of read models the app joins at
// shutdown. It lives beside the closers so a newly cached service is added
// once, where its closer is, instead of at a distant call site where a
// missing entry is invisible.
func readModelCloserList(w *wire, learning any) []cacheCloser {
	models := []any{hubAPI{w}, companiesAPI{w}, conversationsAPI{w}, commitmentsAPI{w},
		briefAPI{w}, triageAPI{w}, messagesAPI{w}, layer1API{w}, searchAPI{w}, learning}
	// The agenda service is passed as itself, so an app that never wired one
	// contributes no closer instead of a nil pointer the join would call.
	if w != nil && w.app != nil && w.app.agenda != nil {
		models = append(models, w.app.agenda)
	}
	return readModelClosers(models...)
}

// joinReadModels waits for every read model's detached rebuild, bounded by
// ctx, and reports the conjunction. One owner keeps the shutdown rule in one
// place: a miss anywhere means the store owner keeps the store open.
func joinReadModels(ctx context.Context, closers []cacheCloser) bool {
	joined := true
	for _, closeViews := range closers {
		if !closeViews(ctx) {
			joined = false
		}
	}
	return joined
}

// Close joins the hub's cached counts, bounded by ctx.
func (a hubAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.hub == nil {
		return true
	}
	return a.w.hub.Close(ctx)
}

// Close joins the companies list refresh and the profile cache, bounded by
// ctx. The service is built on first use, so an unrequested explorer has
// nothing to join.
func (a companiesAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.world == nil {
		return true
	}
	return a.w.world.Close(ctx)
}

// Close joins the conversation snapshot and page caches, bounded by ctx.
func (a conversationsAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.conversationsSvc == nil {
		return true
	}
	return a.w.conversationsSvc.Close(ctx)
}

// Close joins the open-loop read model's cache, bounded by ctx.
func (a commitmentsAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.commitmentsSvc == nil {
		return true
	}
	return a.w.commitmentsSvc.Close(ctx)
}

// Close joins the triage page and summary caches, bounded by ctx. Their
// rebuilds read the store after the request that started them returned.
func (a triageAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.app == nil || a.w.app.triage == nil {
		return true
	}
	return a.w.app.triage.Close(ctx)
}

// Close joins the Mail first-page build, bounded by ctx. It is detached from
// the click that started it, so only its owner can wait for it.
func (a messagesAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.messagesSvc == nil {
		return true
	}
	return a.w.messagesSvc.Close(ctx)
}

// Close joins the semantic worker a query detached, bounded by ctx. It can
// outlive the request that started it (a cold vector load, a rebuilt eligible
// set), and it is a store reader, so only the searcher can wait for it.
func (a searchAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.app == nil || a.w.app.search == nil {
		return true
	}
	return a.w.app.search.Close(ctx)
}

// Close joins the Brief's cache, bounded by ctx.
func (a briefAPI) Close(ctx context.Context) bool {
	if a.w == nil || a.w.briefSvc == nil {
		return true
	}
	return a.w.briefSvc.Close(ctx)
}
