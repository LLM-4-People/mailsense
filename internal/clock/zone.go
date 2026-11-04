package clock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Windows has no IANA database: embed it (≈450 KB) so a configured
	// time.zone resolves on every platform.
	_ "time/tzdata"
)

// SettingZone is the owner's IANA time zone. The key belongs to the clock
// because every reader resolves it through Zone, not to each package that
// happens to read it (five private copies had drifted apart).
const SettingZone = "time.zone"

// ErrZoneUnconfirmed is returned for an empty time.zone: the machine's
// zone is never assumed silently (a container or WSL reports UTC); the
// user confirms it at first run (learning stage "machine").
var ErrZoneUnconfirmed = errors.New("clock: the time zone is not confirmed (time.zone is empty)")

// Zone resolves a time.zone setting: an IANA name such as
// "Europe/Paris". "" is ErrZoneUnconfirmed, and "Local" is refused: Go
// resolves it to whatever zone the machine reports, which is exactly the
// silent assumption the setting exists to avoid.
// zoneCache memoizes successful loads. time.LoadLocation parses the zone
// file on every call, and the same configured zone is resolved on every
// Ask, company and triage view. Zone names come from one setting, so the
// map is bounded by the owner's configurations.
var zoneCache sync.Map // name → *time.Location

func Zone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrZoneUnconfirmed
	}
	if strings.EqualFold(name, "Local") {
		return nil, fmt.Errorf("clock: %q names the machine's zone, not a time zone: confirm an IANA name", name)
	}
	if cached, ok := zoneCache.Load(name); ok {
		return cached.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	zoneCache.Store(name, loc)
	return loc, nil
}

// ZoneOrUTC resolves name, or returns UTC when it is empty or invalid. A
// read-only view (a company profile, a relative time range) must still answer
// on a mailbox whose zone is not confirmed; explicit stages use Zone and
// report the blocker instead.
func ZoneOrUTC(name string) *time.Location {
	if loc, err := Zone(name); err == nil {
		return loc
	}
	return time.UTC
}

// Proposal is the zone the machine reports, for the user to confirm
// (never applied by itself): its IANA name when the machine names one
// ("" otherwise: Windows and most containers only give an offset), the
// abbreviation and the UTC offset at the given instant.
type Proposal struct {
	IANA   string `json:"iana,omitempty"`
	Abbrev string `json:"abbrev"`
	Offset string `json:"offset"` // "+02:00"
}

// Detected is the machine's zone at t as a proposal.
func Detected(t time.Time) Proposal {
	target, _ := filepath.EvalSymlinks("/etc/localtime")
	return propose(t.In(time.Local), ianaFrom(os.Getenv("TZ"), target))
}

func propose(t time.Time, iana string) Proposal {
	abbrev, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return Proposal{IANA: iana, Abbrev: abbrev, Offset: fmt.Sprintf("%s%02d:%02d", sign, off/3600, off%3600/60)}
}

// ianaFrom is the IANA name a machine configures: $TZ when it is one, else
// the zone a /etc/localtime link points to (Unix); "" when neither names one.
func ianaFrom(tz, localtime string) string {
	if tz = strings.TrimPrefix(tz, ":"); tz != "" {
		if _, err := Zone(tz); err == nil {
			return tz
		}
	}
	_, name, ok := strings.Cut(filepath.ToSlash(localtime), "/zoneinfo/")
	if !ok {
		return ""
	}
	if _, err := Zone(name); err != nil {
		return ""
	}
	return name
}
