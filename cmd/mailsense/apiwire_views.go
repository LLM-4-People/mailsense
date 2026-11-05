package main

// The owner-facing read-model adapters of the API wire: the companies
// explorer, the thread-first conversation page, the deterministic open-loop
// read model and the Brief. They are deliberately together because they share
// one rule — a cached service is built once and the handler never knows the
// store.

import (
	"context"
	"errors"

	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/learning"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

type companiesAPI struct{ w *wire }

func (a companiesAPI) List(ctx context.Context, q worldsvc.ListQuery) (worldsvc.ListResult, error) {
	svc, err := a.w.companiesService()
	if err != nil {
		return worldsvc.ListResult{}, err
	}
	return svc.List(ctx, q)
}

func (a companiesAPI) Profile(ctx context.Context, key string, months, eventDays int) (worldsvc.ProfileView, error) {
	svc, err := a.w.companiesService()
	if err != nil {
		return worldsvc.ProfileView{}, err
	}
	return svc.Profile(ctx, key, months, eventDays)
}

func (a companiesAPI) Profiles(ctx context.Context, keys []string, months, eventDays int) (map[string]worldsvc.ProfileView, error) {
	svc, err := a.w.companiesService()
	if err != nil {
		return nil, err
	}
	return svc.Profiles(ctx, keys, months, eventDays)
}

// companiesService builds the world service once from the app's store and
// priority ranking. The priority ranking owns the classifier-backed thread
// states, so the company profile never falls back to a second interpretation
// of whether the owner owes a reply. The commitments service is attached when
// it is buildable; an app without priority has no commitment set, so its
// profiles omit open loops instead of failing on the overlay. A typed-nil
// pointer in an interface would read as present and panic on use, so each
// optional dependency is assigned only when it exists.
func (w *wire) companiesService() (*worldsvc.Service, error) {
	w.worldOnce.Do(func() {
		var ranked worldsvc.Priority
		if w.app.priority != nil {
			ranked = w.app.priority
		}
		var loops worldsvc.OpenLoops
		if svc, err := w.commitmentsService(); err == nil {
			loops = svc
		}
		w.world, w.worldErr = worldsvc.New(worldsvc.Deps{
			Store:     w.app.st,
			Priority:  ranked,
			OpenLoops: loops,
			Config:    w.app.cfg,
			Clock:     clock.Real(),
		})
	})
	return w.world, w.worldErr
}

type conversationsAPI struct{ w *wire }

func (a conversationsAPI) Page(ctx context.Context, q conversations.Query) (conversations.Page, error) {
	svc, err := a.w.conversationsService()
	if err != nil {
		return conversations.Page{}, err
	}
	return svc.Page(ctx, q)
}

// previewGate is the app's one learning machine as the shared preview rule.
// It is nil only in a partially wired app, where preview is off.
func (w *wire) previewGate() learning.Previewer {
	if w.app.learning == nil {
		return nil
	}
	return w.app.learning
}

func (w *wire) conversationsService() (*conversations.Service, error) {
	w.conversationsOnce.Do(func() {
		var priority conversations.Priority
		if w.app.priority != nil {
			priority = w.app.priority
		}
		w.conversationsSvc, w.conversationsErr = conversations.New(conversations.Deps{
			Store:    w.app.st,
			Priority: priority,
			Gate:     w.previewGate(),
			Clock:    clock.Real(),
		})
	})
	return w.conversationsSvc, w.conversationsErr
}

type commitmentsAPI struct{ w *wire }

func (a commitmentsAPI) List(ctx context.Context, f commitments.Filter) (commitments.Result, error) {
	svc, err := a.w.commitmentsService()
	if err != nil {
		return commitments.Result{}, err
	}
	return svc.List(ctx, f)
}

// commitmentsService builds the read model once from the app's store and
// priority service. Priority is built by wireEngines before the API is
// wired, so production always has it; an app wired without it gets a clear
// endpoint error rather than a set that silently omits replies and
// decisions.
func (w *wire) commitmentsService() (*commitments.Service, error) {
	w.commitmentsOnce.Do(func() {
		if w.app.priority == nil {
			w.commitmentsErr = errors.New("commitments: the priority service is not wired")
			return
		}
		w.commitmentsSvc = commitments.New(commitments.Deps{
			Store:     w.app.st,
			Threads:   w.app.priority,
			Decisions: w.app.priority,
			Actions:   w.app.priority,
			Clock:     clock.Real(),
		})
	})
	return w.commitmentsSvc, w.commitmentsErr
}

type briefAPI struct{ w *wire }

func (a briefAPI) Brief(ctx context.Context, since string) (brief.Result, error) {
	svc, err := a.w.briefService()
	if err != nil {
		return brief.Result{}, err
	}
	return svc.Brief(ctx, since)
}

// briefService builds the Brief once from the existing conversation and
// commitment services. Reusing those instances is deliberate: the Brief must
// read the same digest snapshot, thread truth and cache generation the
// dedicated screens serve, not parallel copies. Priority is the app's one
// ranking; a nil ranking leaves Topics empty instead of inventing a census.
func (w *wire) briefService() (*brief.Service, error) {
	w.briefOnce.Do(func() {
		conversationFacts, err := w.conversationsService()
		if err != nil {
			w.briefErr = err
			return
		}
		openLoops, err := w.commitmentsService()
		if err != nil {
			w.briefErr = err
			return
		}
		var ranked brief.Priority
		if w.app.priority != nil {
			ranked = w.app.priority
		}
		w.briefSvc, w.briefErr = brief.New(brief.Deps{
			Commitments:   openLoops,
			Conversations: conversationFacts,
			Priority:      ranked,
			Store:         w.app.st,
			Gate:          w.previewGate(),
			Clock:         clock.Real(),
		})
	})
	return w.briefSvc, w.briefErr
}
