package outlook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Kind is the connector kind of classic Outlook. It is a protocol/storage
// identity, not a screen label; the label is outlook.display_name.
const Kind = "outlook"

// Origin is the Message.Origin of Outlook mail. It is message provenance, not
// a screen label.
const Origin = "outlook"

// Connector reads every mail folder of every store of the running classic
// Outlook profile through COM, read-only. All COM work happens on one STA
// thread, one operation at a time.
type Connector struct {
	id    string
	cfg   *config.Config
	bus   *events.Bus
	clock clock.Clock
	log   *slog.Logger
	host  host

	// opMu serialises operations (one sequence of COM calls at a time) and
	// guards apt and sess. mu guards everything else and is never held
	// across a call into Outlook, so state reads and actions (Breaker,
	// Resume, Stores, Close) never wait for an operation in progress.
	opMu    sync.Mutex
	apt     *apartment
	sess    *session
	life    context.Context // ends at Close: an operation in progress stops at its next wait
	kill    context.CancelFunc
	mu      sync.Mutex
	folders map[folderKey]folderState // from the last Discover
	kinds   map[string]mail.StoreKind // store id → kind, from the last Discover
	stores  []StoreInfo               // every store of the last Discover
	counted map[string]StoreInfo      // unpicked stores already counted once, by key
	dns     *resolver
	count   counters
	stats   *Stats
	pace    *pacer
	brk     breaker
	shown   BreakerStatus // the breaker state last published
	keeper  source.StateKeeper
	closed  bool

	setMu   sync.Mutex // guards the settings cache
	setSnap *config.Snapshot
	setVal  settings
}

var (
	_ source.Connector      = (*Connector)(nil)
	_ source.Named          = (*Connector)(nil)
	_ source.Prober         = (*Connector)(nil)
	_ source.DetailReader   = (*Connector)(nil)
	_ source.FolderProber   = (*Connector)(nil)
	_ source.ContactSource  = (*Connector)(nil)
	_ source.CalendarSource = (*Connector)(nil)
)

// New returns the connector for this machine's Outlook (ErrUnsupported
// off Windows). bus and clk may be nil (no events; wall clock).
func New(id string, cfg *config.Config, bus *events.Bus, clk clock.Clock) (*Connector, error) {
	h, err := newHost()
	if err != nil {
		return nil, err
	}
	return newConnector(id, cfg, bus, clk, h)
}

func newConnector(id string, cfg *config.Config, bus *events.Bus, clk clock.Clock, h host) (*Connector, error) {
	if id == "" || cfg == nil || h == nil {
		return nil, errors.New("outlook: id, config and host are required")
	}
	st := newStats()
	clk = clock.Or(clk)
	log := logx.For("source.outlook").With("source", id)
	life, kill := context.WithCancel(context.Background())
	return &Connector{id: id, cfg: cfg, bus: bus, clock: clk, log: log, host: h, folders: map[folderKey]folderState{},
		counted: map[string]StoreInfo{}, dns: newResolver(st), stats: st, life: life, kill: kill,
		pace: &pacer{clock: clk, idle: h.UserIdle, log: log}}, nil
}

// ID returns the source id.
func (c *Connector) ID() string { return c.id }

// Kind returns "outlook".
func (c *Connector) Kind() string { return Kind }

// DisplayName returns the configured owner-facing name of this source.
func (c *Connector) DisplayName() string { return c.cfg.String("outlook.display_name") }

// Capabilities: incremental folder cursors, full bodies and recipients in
// the detail pass, removals from complete snapshots, the owner addresses
// of picked stores, MAPI addressed-to-me flags, default-folder roles,
// message classes, transport headers, attachment identity and inline image
// bytes, and the owner's actions (read state, follow-up flags, categories,
// last verb). Outlook events are not wired yet (LiveEvents false); a message
// lives in one folder.
func (c *Connector) Capabilities() source.Capabilities {
	return source.Capabilities{Incremental: true, Bodies: true, Recipients: true, Deletes: true, OwnerIdentity: true,
		AddressedToMe: true, FolderRoles: true, MessageClass: true, Headers: true, Attachments: true,
		ReadState: true, Flags: true, Categories: true, LastVerb: true,
		Contacts: true, Calendar: true}
}

// Stats returns a copy of the connector's measurements.
func (c *Connector) Stats() *Stats {
	s := c.stats.Snapshot()
	s.Calls, s.Retries, s.Members = c.count.calls.Load(), c.count.retries.Load(), c.count.snapshot()
	s.Pace = c.pace.stats()
	return s
}

// ErrClosed is returned by every operation after Close.
var ErrClosed = errors.New("outlook: connector closed")

// Close disconnects from Outlook and stops the COM thread; the connector
// cannot be used afterwards (ErrClosed). An operation in progress stops
// at its next wait (only a call already inside Outlook is waited for).
func (c *Connector) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.kill()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if c.apt == nil {
		return
	}
	_ = c.apt.do(context.Background(), func() error {
		if c.sess != nil {
			c.sess.close()
			c.sess = nil
		}
		return nil
	})
	c.apt.close()
	c.apt = nil
}

// settings are the connector's settings, read once per operation.
type settings struct {
	pageRows       int
	preview        bool
	bodyMax        int
	htmlMax        int
	headersMax     int
	inlinePartMax  int
	inlineTotalMax int
	inlineParts    int
	retry          retryPolicy
	selected       map[string]bool // picked store keys (source.SettingSelected)
	recheck        time.Duration   // snapshot backstop
	excludeFolders []*regexp.Regexp
	countUnpicked  bool   // walk unpicked stores once for their counts
	profile        string // configured mail profile ("" = whatever is open)
	times          mail.Times
	pace           paceSettings
	breaker        breakerSettings
}

// settings returns the connector's settings for the current configuration
// snapshot, compiled once per snapshot (ReadDetails runs ~100k times).
func (c *Connector) settings() (settings, error) {
	snap := c.cfg.Snapshot()
	c.setMu.Lock()
	defer c.setMu.Unlock()
	if snap == c.setSnap {
		return c.setVal, nil
	}
	s, err := compileSettings(snap)
	if err == nil {
		c.setSnap, c.setVal = snap, s
	}
	return s, err
}

func compileSettings(snap *config.Snapshot) (settings, error) {
	lim := mail.LimitsFromConfig(snap)
	s := settings{
		pageRows: snap.Int("outlook.table.page_rows"), preview: snap.Bool("outlook.table.body_preview"),
		bodyMax: lim.BodyChars, htmlMax: lim.HTMLChars, headersMax: lim.HeaderChars,
		inlinePartMax: lim.InlinePartBytes, inlineTotalMax: lim.InlineTotalBytes, inlineParts: lim.InlineParts,
		retry: retryPolicy{attempts: snap.Int("outlook.com.retry.attempts"), base: snap.Duration("outlook.com.retry.base_delay"),
			max: snap.Duration("outlook.com.retry.max_delay")},
		selected: source.Selected(snap), times: mail.TimesFromConfig(snap), recheck: snap.Duration("outlook.sync.recheck_after"),
		pace: paceFrom(snap), breaker: breakerFrom(snap), countUnpicked: snap.Bool("outlook.discover.count_unpicked"),
		profile: strings.TrimSpace(snap.String("outlook.profile")),
	}
	for _, p := range snap.Strings("outlook.folders.exclude_patterns") {
		re, err := regexp.Compile(p)
		if err != nil { // the schema validates the format; this is defence in depth
			return s, fmt.Errorf("outlook: outlook.folders.exclude_patterns: %w", err)
		}
		s.excludeFolders = append(s.excludeFolders, re)
	}
	return s, nil
}

// op runs fn on the COM thread with a guarded view of the Namespace,
// connecting first when needed — the ONLY path that connects. Connecting
// asks the breaker first (breaker.go); a lost session is dropped and
// recorded there, so the next operation reconnects only when the breaker
// allows it, at the pace it allows.
func (c *Connector) op(ctx context.Context, fn func(ns Object, s settings) error) error {
	s, err := c.settings()
	if err != nil {
		return err
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.admit(s, c.sess == nil); err != nil {
		return err
	}
	if err := c.startApartment(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer context.AfterFunc(c.life, cancel)()
	defer cancel()
	view := func(ctx context.Context, obj Object, class string) *guard { return c.view(ctx, s, obj, class) }
	err = c.apt.do(ctx, func() error {
		// A configured profile change applies to the next operation: a running
		// Outlook cannot switch profiles, so drop the old session and let
		// connect verify the configured profile (and fail with the cause when
		// the open Outlook uses another one).
		if c.sess != nil && s.profile != "" && !strings.EqualFold(c.sess.profile, s.profile) {
			c.sess.close()
			c.sess = nil
		}
		if c.sess == nil {
			sess, err := connect(ctx, c.host, view, s.profile)
			if err != nil {
				return err
			}
			c.sess = sess
			c.log.Info("connected to Outlook", "pace", c.Breaker(), "profile", sess.profile)
		}
		err := fn(view(ctx, c.sess.ns, classNamespace), s)
		if errors.Is(err, ErrSessionLost) {
			c.sess.close()
			c.sess = nil
		}
		return err
	})
	switch {
	case c.life.Err() != nil: // closed under the operation
		return ErrClosed
	case errors.Is(err, ErrSessionLost):
		c.lost(s, err)
	}
	return err
}

// admit checks that the connector is open and, before connecting, asks
// the breaker (c.mu taken briefly; host.Started is a process-list read,
// never a call into Outlook).
func (c *Connector) admit(s settings, connecting bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if !connecting {
		return nil
	}
	err := c.brk.allow(c.clock.Now(), s.breaker, c.host.Started)
	c.disclose(s)
	return err
}

// lost records a lost connection in the breaker, discloses and keeps it.
func (c *Connector) lost(s settings, err error) {
	c.mu.Lock()
	c.brk.lost(c.clock.Now(), s.breaker, err)
	st := c.brk.status(c.clock.Now(), s.breaker)
	c.disclose(s)
	kept := c.brk.kept()
	c.mu.Unlock()
	c.log.Error("Outlook connection lost; no reconnect before the cooldown ends", "err", err, "losses", st.Losses,
		"state", st.State, "until", st.Until)
	c.persist(kept)
}

// startApartment starts the COM thread once (c.opMu held).
func (c *Connector) startApartment() error {
	if c.apt != nil {
		return nil
	}
	apt, err := startApartment(c.host.Init, c.host.Uninit)
	if err != nil {
		return fmt.Errorf("outlook: cannot start the COM thread: %w", err)
	}
	c.apt = apt
	return nil
}

// view guards one root object for one operation (never released by it),
// paced by the connector's pacer at the breaker's factor.
func (c *Connector) view(ctx context.Context, s settings, obj Object, class string) *guard {
	c.mu.Lock()
	factor := c.brk.factor(c.clock.Now(), s.breaker)
	c.mu.Unlock()
	return &guard{inner: obj, class: class, ctx: ctx, policy: s.retry, clock: c.clock, count: &c.count,
		pace: pacing{p: c.pace, s: s.pace, factor: factor}}
}

// disclose publishes the breaker's state when it changed (c.mu held; the
// bus never blocks).
func (c *Connector) disclose(s settings) {
	st := c.brk.status(c.clock.Now(), s.breaker)
	st.Source = c.id
	if st.State == c.shown.State && st.Losses == c.shown.Losses {
		return
	}
	c.shown = st
	if st.State == BreakerStopped {
		c.log.Error("Outlook sync stopped after repeated lost connections; it waits for the user to resume it",
			"losses", st.Losses, "err", st.Error)
	} else {
		c.log.Info("Outlook connection breaker", "state", st.State, "losses", st.Losses, "until", st.Until, "factor", st.Factor)
	}
	if c.bus != nil {
		events.Publish(c.bus, TopicBreaker, st)
	}
}

// Breaker is the breaker's state now.
func (c *Connector) Breaker() BreakerStatus {
	s, err := c.settings()
	if err != nil {
		return BreakerStatus{Source: c.id, State: BreakerClosed, Error: err.Error()}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.brk.status(c.clock.Now(), s.breaker)
	st.Source = c.id
	return st
}

// Resume is the user's action after the breaker stopped (or to skip a
// cooldown): the recorded losses are forgotten, so the next operation
// connects as a first one would — Outlook must be running (StartOutlook),
// and one started less than outlook.breaker.min_uptime ago is read at the
// reduced pace until it has been up that long. It never waits for an
// operation in progress.
func (c *Connector) Resume() error {
	s, err := c.settings()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.brk.resume()
	c.disclose(s)
	kept := c.brk.kept()
	c.mu.Unlock()
	c.log.Info("Outlook sync resumed by the user")
	c.persist(kept)
	return nil
}

// StartOutlook is the user's action to start classic Outlook as they
// would themselves (its window, the configured profile). Background work
// never calls it: the connector only attaches to a running Outlook. A
// running Outlook is left alone, but a configured profile must match: COM
// cannot switch a running Outlook to another profile.
func (c *Connector) StartOutlook(ctx context.Context) error {
	s, err := c.settings()
	if err != nil {
		return err
	}
	c.opMu.Lock()
	err = c.admit(s, false)
	c.opMu.Unlock()
	if err != nil {
		return err
	}

	// Outlook already runs: it owns its profile. Verify the configured one
	// instead of pretending a start succeeded.
	if _, started := c.host.Started(); started == nil {
		if s.profile == "" {
			return nil
		}
		h, err := c.Health(ctx)
		if err != nil {
			return err
		}
		if strings.EqualFold(h.Profile, s.profile) {
			return nil
		}
		return fmt.Errorf("%w: Outlook is running with profile %q; the configured profile is %q. "+
			"Close Outlook, then use Start Outlook", ErrWrongProfile, h.Profile, s.profile)
	}

	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.admit(s, false); err != nil {
		return err
	}
	// Another caller (or the user) may have started Outlook while we waited.
	if _, started := c.host.Started(); started == nil {
		if s.profile == "" {
			return nil
		}
		return fmt.Errorf("%w: Outlook started before Mailsense could open profile %q; "+
			"close Outlook, then use Start Outlook", ErrWrongProfile, s.profile)
	}
	if err := c.host.Registered(); err != nil {
		return fmt.Errorf("%w (%w)", ErrNoClassicOutlook, err)
	}
	if c.host.NewOutlook() {
		return ErrNewOutlook
	}
	if err := c.startApartment(); err != nil {
		return err
	}
	// ShellExecute wants COM initialized on its thread: the apartment's.
	if err := c.apt.do(ctx, func() error { return c.host.Start(s.profile) }); err != nil {
		return fmt.Errorf("outlook: cannot start Outlook: %w", err)
	}
	c.log.Info("Outlook started at the user's request", "profile", s.profile)
	return nil
}

// Health is the connector's preflight state for the UI.
type Health struct {
	Connected  bool          `json:"connected"`
	Version    string        `json:"version,omitempty"` // Outlook's version
	Profile    string        `json:"profile,omitempty"` // Namespace.CurrentProfileName
	Elevated   bool          `json:"elevated"`
	NewOutlook bool          `json:"new_outlook"` // the profile is switched to the new Outlook
	Breaker    BreakerStatus `json:"breaker"`
	Error      string        `json:"error,omitempty"`
}

// Health connects if needed and reports Outlook's state; a failed
// precondition is in Error (and returned), never a panic.
func (c *Connector) Health(ctx context.Context) (Health, error) {
	h := Health{NewOutlook: c.host.NewOutlook()}
	h.Elevated, _ = c.host.Elevated()
	err := c.op(ctx, func(ns Object, s settings) error {
		v, err := c.view(ctx, s, c.sess.app, classApplication).Get("Version")
		if err != nil {
			return err
		}
		h.Version = asString(v)
		if p, perr := c.view(ctx, s, ns, classNamespace).Get("CurrentProfileName"); perr == nil {
			h.Profile = strings.TrimSpace(asString(p))
		}
		return nil
	})
	h.Connected = err == nil
	h.Breaker = c.Breaker()
	if err != nil {
		h.Error = err.Error()
	}
	return h, err
}

// Probe reports whether the connector can be used now. It uses Health, so a
// missing Outlook, a COM failure, an elevated process or a configured profile
// mismatch all become an unavailable readiness with the connector's named
// cause. The source stays a passive list entry otherwise.
func (c *Connector) Probe(ctx context.Context) source.Readiness {
	h, err := c.Health(ctx)
	if err != nil {
		cause, _ := source.Cause(err)
		return source.Readiness{Available: false, Cause: cause, Detail: err.Error(), Profile: h.Profile}
	}
	return source.Readiness{Available: true, Profile: h.Profile}
}

// publish emits a phase measurement.
func (c *Connector) publish(p Phase) {
	p.Source = c.id
	if p.Duration > 0 {
		p.RowsPerSecond = float64(p.Rows) / p.Duration.Seconds()
	}
	c.log.Info("phase", "phase", p.Phase, "store_key", p.StoreKey, "kind", p.Kind, "folder", p.Folder, "rows", p.Rows, "calls", p.Calls,
		"duration", p.Duration, "rows_per_second", int(p.RowsPerSecond))
	if c.bus != nil {
		events.Publish(c.bus, TopicPhase, p)
	}
}
