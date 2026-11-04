// Package logx is the one logging path of the application. Every package
// obtains its logger with logx.For("component"); entries are redacted once
// here and fanned out to a rotating JSON file, an in-memory ring for the UI,
// the event bus, and (optionally) stderr.
//
// The active pipeline is a process-wide registry swapped atomically by
// Setup/Close and on configuration changes. Before Setup, warnings and errors
// go to stderr so nothing is lost during start-up.
package logx

import (
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// TopicEntry carries every log entry when log.events.enabled is on.
var TopicEntry = events.NewTopic[Entry]("log.entry")

// bootstrapLevel is the threshold before Setup: only warnings and errors
// reach stderr while the configuration is not loaded yet.
const bootstrapLevel = slog.LevelWarn

// core is one immutable pipeline configuration.
type core struct {
	levels   *levelTable
	redactor *redactor
	file     *fsx.Rotating // nil when file logging is off
	ring     *Ring         // nil before Setup
	bus      *events.Bus   // nil when event fan-out is off
	console  bool
	health   *health
}

var (
	active atomic.Pointer[core]
	seq    atomic.Uint64
)

func init() { active.Store(defaultCore()) }

// defaultCore is the pre-Setup pipeline: warnings and errors to stderr,
// redacted with the schema defaults.
func defaultCore() *core {
	c := &core{console: true, health: &health{}}
	if err := c.configure(defaultValues()); err != nil {
		panic("logx: schema defaults are invalid: " + err.Error())
	}
	c.levels = &levelTable{def: bootstrapLevel}
	return c
}

// For returns the logger of a component (dotted names inherit levels from
// their parent: "source.archive" falls back to "source").
func For(component string) *slog.Logger {
	return slog.New(&handler{component: component})
}

// RegisterSecret makes every future log entry replace value with the
// redaction marker. Values shorter than log.redact.min_secret_length are kept
// out (too short to replace safely) but still recorded.
func RegisterSecret(value string) { secrets.add(value) }

func (c *core) write(e Entry) {
	// The ring assigns Seq under its own lock, so insertion order equals
	// sequence order; the file and the bus see that same entry. Without a
	// ring (before Setup) the sequence is still assigned exactly once.
	if c.ring != nil {
		// One JSON copy per retained entry, bounded by log.ring.size and
		// released with the entry. Encode only when a JSON consumer exists:
		// the file line and the SSE log stream share these bytes, while the
		// Logs UI snapshot encodes its own response.
		e = c.ring.push(e, c.file != nil || c.bus != nil)
	} else {
		e.Seq = seq.Add(1)
	}
	if c.file != nil {
		c.health.fileResult(c.writeFile(e))
	}
	if c.bus != nil {
		events.Publish(c.bus, TopicEntry, e)
	}
	if c.console {
		_, _ = os.Stderr.WriteString(e.text())
	}
}

func (c *core) writeFile(e Entry) error {
	line, err := e.line()
	if err != nil {
		return err
	}
	return c.file.Write(line)
}
