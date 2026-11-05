package update

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/buildinfo"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Settings owned by this package (schema group Updates).
const (
	// SettingCheckEnabled turns the automatic check on or off. A manual
	// check from the update surface still runs when it is off.
	SettingCheckEnabled = "update.check.enabled"
	// SettingCheckInterval is the shortest gap between two automatic checks.
	SettingCheckInterval = "update.check.interval"
	// SettingRemindAfter is how long a dismissed offer stays quiet.
	SettingRemindAfter = "update.remind.after"
)

// TopicUpdate carries every state change of the update status.
var TopicUpdate = events.NewTopic[Status]("update.state")

// Error codes of a failed check. They are stable wire values; the UI shows a
// quiet state for each and never a toast on a check that did not succeed.
const (
	ErrorUnreachable = "unreachable"
	ErrorRateLimited = "rate_limited"
	ErrorHTTP        = "http_error"
	ErrorInvalid     = "invalid_response"
	ErrorTooLarge    = "too_large"
)

const (
	// checkTimeout bounds one release check. The shared client's total
	// timeout is much larger (http.timeout.total); a check is a small
	// conditional request and must never hold the single flight that long.
	checkTimeout = 30 * time.Second
	// checkJitter is the fraction of the interval added at random, so many
	// installations do not check at the same moment.
	checkJitter = 0.1
	// maxRateLimitWait caps a server-requested wait.
	maxRateLimitWait = 24 * time.Hour
)

// PreviousVersionReader reads the version the store was last opened with
// before this run ("" when the database was never opened). It is the one
// consumer of the store's previous-open-version fact.
type PreviousVersionReader interface {
	PreviousOpenVersion() string
}

// Options are the service's collaborators. Config is required; Bus, Clock,
// Store, Client and Rand may be nil (no events, wall clock, no previous
// version, the shared outbound client, math/rand). Current and BaseURL are
// seams for tests.
type Options struct {
	Config  *config.Config
	Bus     *events.Bus
	Clock   clock.Clock
	Store   PreviousVersionReader
	BaseURL string
	Client  *http.Client
	// Current is the running build version; "" = buildinfo.Version.
	Current string
	// Rand is the jitter source (uniform in [0,1)); nil = math/rand.
	Rand func() float64
}

// Status is the update view the UI renders. It never carries remote HTML:
// Notes is bounded plain text.
type Status struct {
	Current        string `json:"current"`
	Latest         string `json:"latest,omitempty"`
	Available      bool   `json:"available"`
	VersionsBehind int    `json:"versions_behind"`
	ReleaseURL     string `json:"release_url,omitempty"`
	Notes          string `json:"notes"`
	NotesSeen      bool   `json:"notes_seen"`
	Skipped        bool   `json:"skipped"`
	CheckEnabled   bool   `json:"check_enabled"`
	CheckedAt      string `json:"checked_at,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

// Service is the release-detection service: Status is a local read, Check
// performs one endpoint request, MaybeCheck adds the persisted interval gate
// and Skip/MarkSeen record the What's New markers.
type Service struct {
	cfg     *config.Config
	bus     *events.Bus
	clock   clock.Clock
	store   PreviousVersionReader
	current string
	rand    func() float64
	checker *Checker
	state   *stateFile

	// checkMu makes one check at a time: a startup pass and a manual check
	// can race, and neither may issue a duplicate request.
	checkMu sync.Mutex
	// retryMu guards retryAt, the moment a rate-limited server allows the
	// next automatic check.
	retryMu sync.Mutex
	retryAt time.Time
}

// New builds the service and loads the state file (an unreadable file starts
// empty and is reported in the log, never fatal).
func New(o Options) (*Service, error) {
	if o.Config == nil {
		return nil, errors.New("update: a config is required")
	}
	clk := clock.Or(o.Clock)
	s := &Service{cfg: o.Config, bus: o.Bus, clock: clk, store: o.Store, current: o.Current, rand: o.Rand}
	if s.current == "" {
		s.current = buildinfo.Version
	}
	if s.rand == nil {
		s.rand = rand.Float64
	}
	client := o.Client
	if client == nil {
		var err error
		client, err = httpx.New(o.Config, o.Bus, "update.check", httpx.WithClock(clk), httpx.WithTotalTimeout(checkTimeout), httpx.WithQuietSuccess())
		if err != nil {
			return nil, err
		}
	}
	s.checker = NewChecker(client, o.BaseURL)
	state, err := loadState(o.Config.Paths().Resolve(stateRelPath), o.Config.Snapshot().Replace(clk))
	if err != nil {
		logx.For("update").Warn("update state not loaded", "err", logx.RedactedError(err))
	}
	s.state = state
	return s, nil
}

// CheckEnabled reports whether the automatic check is on.
func (s *Service) CheckEnabled() bool { return s.cfg.Bool(SettingCheckEnabled) }

// updateAvailable reports whether version orders after the running build.
func (s *Service) updateAvailable(version string) bool {
	current, ok := ParseVersion(s.current)
	if !ok {
		return false
	}
	latest, ok := ParseVersion(version)
	if !ok {
		return false
	}
	return Compare(latest, current) > 0
}

// Status reads the current state. It makes no network call.
func (s *Service) Status() Status {
	st := s.state.snapshot()
	out := Status{
		Current:      s.current,
		CheckEnabled: s.CheckEnabled(),
		CheckedAt:    st.CheckedAt,
		ErrorCode:    st.ErrorCode,
		LastError:    st.LastError,
	}
	if st.Latest != nil {
		out.Latest = st.Latest.Version
		out.ReleaseURL = st.Latest.HTMLURL
		out.Notes = st.Latest.BodyText
		out.Skipped = s.skipped(st, st.Latest.Version)
	}
	if current, ok := ParseVersion(s.current); ok && st.Latest != nil {
		if latest, ok := ParseVersion(st.Latest.Version); ok && Compare(latest, current) > 0 {
			out.Available = true
			out.VersionsBehind = VersionsBehind(current, latest, st.Tags)
		}
	}
	out.NotesSeen = s.notesSeen(st)
	return out
}

// Check performs one release check. A failed endpoint answer is recorded as
// the quiet state (error_code/last_error, available unchanged) and reported
// as a nil error; only a local state-write failure returns an error. The
// check is single-flight: a concurrent caller reads the current status.
func (s *Service) Check(ctx context.Context) (Status, error) {
	if !s.checkMu.TryLock() {
		return s.Status(), nil
	}
	defer s.checkMu.Unlock()
	if err := ctx.Err(); err != nil {
		return s.Status(), err
	}
	now := s.clock.Now().UTC()
	prior := s.state.snapshot()
	result, err := s.checker.Latest(ctx, prior.ETag)
	if err != nil {
		if ctx.Err() != nil {
			// Shutdown or caller cancellation: not an upstream failure, so
			// the state must not record one.
			return s.Status(), nil
		}
		code, message, wait := checkFailure(err, now)
		if wait > 0 {
			s.setRetryAt(now.Add(min(wait, maxRateLimitWait)))
		}
		if uerr := s.state.update(func(st *State) {
			st.CheckedAt = now.Format(time.RFC3339)
			st.ErrorCode = code
			st.LastError = message
			st.CheckFailures++
		}); uerr != nil {
			return s.Status(), uerr
		}
		log := logx.For("update")
		if code == ErrorUnreachable || code == ErrorRateLimited {
			log.Debug("release check did not complete", "code", code, "err", logx.RedactedError(err))
		} else {
			log.Warn("release check failed", "code", code, "err", logx.RedactedError(err))
		}
		return s.publish(), nil
	}
	// The release list is a second request only when the answer names a
	// release newer than the running build: it feeds the versions-behind
	// count. Its failure never fails the check; the previous tags stay.
	tags := prior.Tags
	if result.Release != nil && s.updateAvailable(result.Release.Version) {
		if fetched, terr := s.checker.Tags(ctx); terr == nil {
			tags = fetched
		} else if ctx.Err() == nil {
			logx.For("update").Debug("release list did not refresh the versions-behind count", "err", logx.RedactedError(terr))
		}
	}
	if uerr := s.state.update(func(st *State) {
		st.CheckedAt = now.Format(time.RFC3339)
		st.ErrorCode = ""
		st.LastError = ""
		st.CheckFailures = 0
		if result.ETag != "" {
			st.ETag = result.ETag
		}
		if result.Release != nil {
			st.Latest = result.Release
			st.Tags = tags
		}
	}); uerr != nil {
		return s.Status(), uerr
	}
	s.setRetryAt(time.Time{})
	logx.For("update").Debug("release check complete", "not_modified", result.NotModified)
	return s.publish(), nil
}

// MaybeCheck runs Check only when the automatic check is enabled, no
// server-requested wait is active and the persisted interval (plus jitter)
// has elapsed since the last attempt. It is the startup and periodic path;
// Check is the manual one.
func (s *Service) MaybeCheck(ctx context.Context) (Status, error) {
	if !s.CheckEnabled() {
		return s.Status(), nil
	}
	now := s.clock.Now().UTC()
	s.retryMu.Lock()
	waiting := now.Before(s.retryAt)
	s.retryMu.Unlock()
	if waiting {
		return s.Status(), nil
	}
	st := s.state.snapshot()
	if st.CheckedAt != "" {
		// An unreadable timestamp means "never checked": a corrupt state
		// must not block detection forever.
		if last, err := time.Parse(time.RFC3339, st.CheckedAt); err == nil {
			interval := s.cfg.Duration(SettingCheckInterval)
			if now.Before(last.Add(interval + jitter(interval, s.rand()))) {
				return s.Status(), nil
			}
		}
	}
	return s.Check(ctx)
}

// Skip records that the owner dismissed the offer for version. The status
// keeps offering it once update.remind.after has elapsed since the skip.
func (s *Service) Skip(version string) (Status, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return s.Status(), errors.New("update: a version is required to skip")
	}
	now := s.clock.Now().UTC().Format(time.RFC3339)
	if err := s.state.update(func(st *State) {
		st.SkippedVersion = version
		st.SkippedAt = now
	}); err != nil {
		return s.Status(), err
	}
	return s.publish(), nil
}

// MarkSeen records that the owner saw the notes of version: the What's New
// marker of the one state file.
func (s *Service) MarkSeen(version string) (Status, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return s.Status(), errors.New("update: a version is required to mark seen")
	}
	if err := s.state.update(func(st *State) {
		st.NotesSeenVersion = version
	}); err != nil {
		return s.Status(), err
	}
	return s.publish(), nil
}

// skipped reports whether the offer for latest is currently muted. A skip
// without a readable timestamp stays muted; update.remind.after is the one
// window that unmutes it.
func (s *Service) skipped(st State, latest string) bool {
	if latest == "" || st.SkippedVersion != latest {
		return false
	}
	if st.SkippedAt == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339, st.SkippedAt)
	if err != nil {
		return true
	}
	return s.clock.Now().UTC().Before(at.Add(s.cfg.Duration(SettingRemindAfter)))
}

// notesSeen reports whether the notes carried by the status were already
// shown: the marker matches their version, or the database was last opened
// on that version (the first open of a version whose notes predate this
// install already counts as seen). The first open after an update has a
// previous version older than the notes' version, so it is unseen.
func (s *Service) notesSeen(st State) bool {
	version := ""
	if st.Latest != nil {
		version = st.Latest.Version
	}
	if version == "" {
		return true
	}
	if st.NotesSeenVersion == version {
		return true
	}
	previous := ""
	if s.store != nil {
		previous = s.store.PreviousOpenVersion()
	}
	return previous != "" && previous == version
}

func (s *Service) setRetryAt(t time.Time) {
	s.retryMu.Lock()
	s.retryAt = t
	s.retryMu.Unlock()
}

// publish broadcasts the current status and returns it.
func (s *Service) publish() Status {
	st := s.Status()
	if s.bus != nil {
		events.Publish(s.bus, TopicUpdate, st)
	}
	return st
}

// jitter returns up to checkJitter of the interval; r is uniform in [0,1).
func jitter(interval time.Duration, r float64) time.Duration {
	if interval <= 0 || r <= 0 {
		return 0
	}
	return time.Duration(float64(interval) * checkJitter * min(r, 1))
}

// checkFailure maps one failed check to its quiet state: a stable code, a
// bounded message and how long the server asked to wait (0 when it did not).
func checkFailure(err error, now time.Time) (code, message string, wait time.Duration) {
	var httpErr *HTTPError
	switch {
	case errors.As(err, &httpErr):
		code = ErrorHTTP
		if httpErr.Status == http.StatusTooManyRequests || httpErr.Remaining == 0 {
			code = ErrorRateLimited
		}
		message = httpErr.Message
		if message == "" {
			message = http.StatusText(httpErr.Status)
		}
		wait = max(httpErr.RetryAfter, httpErr.ResetAt.Sub(now))
		if wait < 0 {
			wait = 0
		}
	case errors.Is(err, ErrResponseTooLarge):
		code, message = ErrorTooLarge, err.Error()
	case errors.Is(err, ErrInvalidResponse):
		code, message = ErrorInvalid, err.Error()
	default:
		code, message = ErrorUnreachable, err.Error()
	}
	return code, clampText(message, maxErrorMessageBytes), wait
}
