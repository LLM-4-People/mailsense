package evidence

import (
	"context"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/psl"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Env is a run's shared, read-only context: what the sources can report,
// the owners, the owner's organisations, the causal history, the threads,
// the look-alike targets and the template counts. Built once per run
// (pass 1), read by every extractor.
type Env struct {
	set    Settings
	caps   map[string]source.Capabilities // by source id
	owners map[string]scope.Identity      // by store key
	orgs   map[string]bool                // the owners' organisations (mail.Domains: shared domains are none)
	hist   *threads.History               // the causal history: exchanges and the owner's replies as known at each instant
	thread *threadView
	look   *lookalikes
	tmpl   *templates // nil: no vectors of a current space
	ctxs   *contexts  // the parts of the context digests of message-only values
}

// buildEnv reads the visible mail once: every message for the template
// counts, the causal view (threads.CausalRoles: the owner-scope mail of
// every folder but drafts, wherever the owner filed it since) for the
// threads, the history and the correspondents.
func buildEnv(ctx context.Context, st *store.Store, set Settings, space string) (*Env, error) {
	env := &Env{set: set, orgs: map[string]bool{}, look: newLookalikes(set.MaxEdits, set.MinNameChars)}
	if err := env.readOwners(ctx, st); err != nil {
		return nil, err
	}
	env.ctxs = newContexts(env)
	threaded, err := st.MessageIDs(ctx, store.MessageFilter{Roles: threads.CausalRoles(), OwnerOnly: true})
	if err != nil {
		return nil, err
	}
	inThreads := make(map[int64]bool, len(threaded))
	for _, id := range threaded {
		inThreads[id] = true
	}
	all, err := readMail(ctx, st, inThreads, set)
	if err != nil {
		return nil, fmt.Errorf("evidence: read the mail: %w", err)
	}
	var msgs []threads.Message
	items := make([]templateItem, 0, len(all))
	asked := map[int64]bool{}
	for i := range all {
		items = append(items, all[i].item)
		if !all[i].threaded {
			continue
		}
		tm := &all[i].msg
		msgs = append(msgs, *tm)
		if tm.Role == mail.RoleSent {
			asked[tm.RowID] = all[i].asked
			for _, rc := range tm.Recipients {
				if tm.Dated() {
					env.look.correspond(unicodeDomain(rc), tm.At, false)
				}
			}
		}
	}
	env.look.seal()
	an := threads.Analyze(msgs, set.Threads.Normalizer)
	env.thread = newThreadView(an, asked)
	env.hist = an.KnownHistory(threads.Floats(set.Threads.Options.BucketMinutes), 0, set.Zone, set.Domains)
	if space != "" {
		vecs, err := loadVectors(ctx, st, space)
		if err != nil {
			return nil, fmt.Errorf("evidence: read the vectors: %w", err)
		}
		if env.tmpl, err = buildTemplates(ctx, items, vecs, set.TemplateCosine, set.TemplateWindow, set.Workers); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// readOwners reads the sources' capabilities and the stores' owners: the
// owners' organisations and own domains (look-alike targets from the start).
func (env *Env) readOwners(ctx context.Context, st *store.Store) error {
	var err error
	if env.caps, err = st.SourceCapabilities(ctx); err != nil {
		return err
	}
	owners, err := st.Owners(ctx)
	if err != nil {
		return err
	}
	env.owners = map[string]scope.Identity{}
	for key, o := range owners {
		env.owners[key] = o.Identity
		for _, a := range o.Identity.Addresses {
			if org := env.set.Domains.Of(a); org != "" {
				env.orgs[org] = true
			}
			env.look.correspond(unicodeDomain(a), time.Time{}, true)
		}
	}
	return nil
}

// unicodeDomain is the registrable domain of an address in its Unicode
// form (the form a reader sees and look-alikes are compared in); "" for
// none. A label that is no valid A-label has no Unicode form: it is
// compared as written.
func unicodeDomain(address string) string {
	d := mail.RegistrableDomain(address)
	if d == "" {
		return ""
	}
	u, ok := psl.Unicode(d)
	if !ok {
		return d
	}
	return u
}
