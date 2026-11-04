// Package fixtureconf is the fixture's source configuration: the legacy
// export is one mailbox that names no owner, so the fixture picks it and
// declares its owner explicitly, from FIXTURE_OWNER (the owner's
// addresses, comma-separated) and FIXTURE_ZONE (the owner's IANA time
// zone; the export names none and the machine's is never assumed).
// scripts/lib.sh fixture_owner reads both from a private file next to the
// export, never from the repository. Test support only; it imports nothing
// that imports the syncer.
package fixtureconf

import (
	"errors"
	"os"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/clock"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// SourceID is the source id every fixture import uses.
const SourceID = "legacy"

// Settings are the overrides that pick the export's mailbox and declare
// its owner.
func Settings() (map[string]any, error) {
	var declared []any
	key := source.StoreKey(SourceID, "")
	for _, a := range strings.FieldsFunc(os.Getenv("FIXTURE_OWNER"), func(r rune) bool { return r == ',' || r == '\n' }) {
		if a = strings.TrimSpace(a); a != "" {
			declared = append(declared, key+"="+a)
		}
	}
	if len(declared) == 0 {
		return nil, errors.New("FIXTURE_OWNER is empty: the legacy export names no owner; list the owner's addresses " +
			"(scripts/fixture-import.sh reads them from <export>.owner)")
	}
	zone := strings.TrimSpace(os.Getenv("FIXTURE_ZONE"))
	if zone == "" {
		return nil, errors.New("FIXTURE_ZONE is empty: the owner's time zone is never assumed; name it (a zone= line in <export>.owner)")
	}
	return map[string]any{source.SettingSelected: []any{key}, source.SettingDeclared: declared, clock.SettingZone: zone}, nil
}
