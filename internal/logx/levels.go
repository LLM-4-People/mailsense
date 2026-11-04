package logx

import (
	"fmt"
	"log/slog"
	"strings"
)

// levelTable resolves the minimum level of a component.
type levelTable struct {
	def  slog.Level
	comp map[string]slog.Level
}

func parseLevel(name string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("logx: unknown level %q", name)
	}
	return l, nil
}

// newLevelTable builds the table from log.level and log.components
// ("component=level" pairs, validated by the config schema).
func newLevelTable(def string, pairs []string) (*levelTable, error) {
	d, err := parseLevel(def)
	if err != nil {
		return nil, err
	}
	t := &levelTable{def: d, comp: map[string]slog.Level{}}
	for _, p := range pairs {
		name, lvl, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("logx: component level %q is not name=level", p)
		}
		l, err := parseLevel(lvl)
		if err != nil {
			return nil, err
		}
		t.comp[name] = l
	}
	return t, nil
}

func (t *levelTable) levelFor(component string) slog.Level {
	for name := component; name != ""; {
		if l, ok := t.comp[name]; ok {
			return l
		}
		i := strings.LastIndexByte(name, '.')
		if i < 0 {
			break
		}
		name = name[:i]
	}
	return t.def
}
