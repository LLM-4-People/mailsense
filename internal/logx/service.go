package logx

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

const mib = 1 << 20

// Applied is published each time the pipeline (re)applies the configuration.
type Applied struct {
	Generation int `json:"generation"`
}

// TopicApplied announces that new log settings are in effect.
var TopicApplied = events.NewTopic[Applied]("log.applied")

// Service owns the configured pipeline between Setup and Close.
type Service struct {
	cfg    *config.Config
	bus    *events.Bus
	file   *fsx.Rotating
	ring   *Ring
	health *health
	// stop is events.On's unsubscribe-and-wait handle for the configuration
	// follower; Close calls it instead of keeping a subscription and a WaitGroup.
	stop func()
	mu   sync.Mutex // serialises apply
	gen  int
}

func defaultValues() *config.Snapshot {
	snap, err := config.Defaults()
	if err != nil {
		panic("logx: " + err.Error())
	}
	return snap
}

// configure sets the level table and redactor from snap.
func (c *core) configure(snap *config.Snapshot) error {
	levels, err := newLevelTable(snap.String("log.level"), snap.Strings("log.components"))
	if err != nil {
		return err
	}
	red, err := newRedactor(snap.Strings("log.redact.patterns"), config.SecretMask, snap.Int("log.redact.min_secret_length"))
	if err != nil {
		return err
	}
	c.levels, c.redactor = levels, red
	return nil
}

// Setup installs the configured pipeline and follows configuration changes
// until Close. bus may be nil (no event fan-out, no live reconfiguration).
func Setup(cfg *config.Config, bus *events.Bus) (*Service, error) {
	snap := cfg.Snapshot()
	s := &Service{cfg: cfg, bus: bus, ring: NewRing(snap.Int("log.ring.size")), health: &health{bus: bus}}
	if snap.Bool("log.file.enabled") {
		dir := cfg.Paths().Resolve(snap.String("log.file.dir"))
		f, err := fsx.OpenRotating(filepath.Join(dir, snap.String("log.file.name")), int64(snap.Int("log.file.max_size_mb"))*mib, snap.Int("log.file.max_files"))
		if err != nil {
			return nil, err
		}
		s.file = f
	}
	if bus != nil {
		stop, err := events.On(bus, config.TopicChanged, snap.EventBuffer(), s.followChange)
		if err != nil {
			_ = s.closeFile()
			return nil, err
		}
		s.stop = stop
	}
	if err := s.apply(); err != nil {
		if s.stop != nil {
			s.stop()
		}
		_ = s.closeFile()
		return nil, err
	}
	return s, nil
}

// followChange applies a change that touches the log pipeline or a secret the
// redactor consumes. events.On owns the subscribe-and-loop and the join.
func (s *Service) followChange(change config.Change) {
	for _, k := range change.Keys {
		if st, _ := s.cfg.Schema().Lookup(k); strings.HasPrefix(k, "log.") || (st != nil && st.Secret) {
			if err := s.apply(); err != nil {
				For("logx").Error("log reconfiguration failed; previous settings kept", "err", err)
			}
			return
		}
	}
}

// apply builds a pipeline from the current configuration and swaps it in.
func (s *Service) apply() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.cfg.Snapshot()
	for _, v := range s.cfg.SecretValues() {
		RegisterSecret(v)
	}
	c := &core{file: s.file, ring: s.ring, console: snap.Bool("log.console.enabled"), health: s.health}
	if err := c.configure(snap); err != nil {
		return err
	}
	s.health.mu.Lock()
	s.health.redact = c.redactor.redact
	s.health.mu.Unlock()
	if snap.Bool("log.events.enabled") {
		c.bus = s.bus
	}
	if s.file != nil {
		s.file.SetLimits(int64(snap.Int("log.file.max_size_mb"))*mib, snap.Int("log.file.max_files"))
	}
	s.ring.Resize(snap.Int("log.ring.size"))
	active.Store(c)
	s.gen++
	if s.bus != nil {
		events.Publish(s.bus, TopicApplied, Applied{Generation: s.gen})
	}
	return nil
}

// Ring returns the in-memory entries for the UI.
func (s *Service) Ring() *Ring { return s.ring }

// Health returns the pipeline health for the UI.
func (s *Service) Health() Health { return s.health.snapshot() }

// Close restores the stderr pipeline and closes the log file.
func (s *Service) Close() error {
	if s.stop != nil {
		s.stop()
	}
	active.Store(defaultCore())
	return s.closeFile()
}

func (s *Service) closeFile() error {
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}
