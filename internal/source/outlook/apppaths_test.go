package outlook

import (
	"errors"
	"slices"
	"testing"
)

// appPathFake stands in for the Windows registry: it answers a root's
// registration from a map and records what it was asked, in order.
type appPathFake struct {
	values  map[appPathRoot]string
	roots   []appPathRoot
	subkeys []string
}

func (f *appPathFake) lookup(root appPathRoot, subkey string) (string, bool) {
	f.roots = append(f.roots, root)
	f.subkeys = append(f.subkeys, subkey)
	v, ok := f.values[root]
	return v, ok
}

// Finding 2026-11-04: Start must launch the absolute path its App Paths
// registration holds (HKLM first, then HKCU), never a bare "OUTLOOK.EXE"
// the shell could resolve to a planted copy in its search path.
func TestResolveOutlookExeAppPaths(t *testing.T) {
	const (
		machineExe = `C:\Program Files\Microsoft Office\root\Office16\OUTLOOK.EXE`
		userExe    = `C:\Program Files\Outlook\OUTLOOK.EXE`
	)
	cases := []struct {
		name    string
		values  map[appPathRoot]string
		want    string
		wantErr error
		asked   []appPathRoot
	}{
		{"the machine registration", map[appPathRoot]string{appPathHKLM: machineExe},
			machineExe, nil, []appPathRoot{appPathHKLM}},
		{"the user registration when the machine has none", map[appPathRoot]string{appPathHKCU: userExe},
			userExe, nil, []appPathRoot{appPathHKLM, appPathHKCU}},
		{"the machine registration wins", map[appPathRoot]string{appPathHKLM: machineExe, appPathHKCU: userExe},
			machineExe, nil, []appPathRoot{appPathHKLM}},
		{"an empty machine value falls through to the user", map[appPathRoot]string{appPathHKLM: "", appPathHKCU: userExe},
			userExe, nil, []appPathRoot{appPathHKLM, appPathHKCU}},
		{"no registration", nil, "", ErrOutlookExeNotFound, []appPathRoot{appPathHKLM, appPathHKCU}},
		{"empty registrations", map[appPathRoot]string{appPathHKLM: "", appPathHKCU: ""},
			"", ErrOutlookExeNotFound, []appPathRoot{appPathHKLM, appPathHKCU}},
	}
	for _, c := range cases {
		reg := &appPathFake{values: c.values}
		got, err := resolveOutlookExe(reg.lookup)
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s: %v, want %v", c.name, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("%s: resolved %q, want %q", c.name, got, c.want)
		}
		if !slices.Equal(reg.roots, c.asked) {
			t.Errorf("%s: read the roots %v, want %v", c.name, reg.roots, c.asked)
		}
		for _, subkey := range reg.subkeys {
			if subkey != appPathsSubKey {
				t.Errorf("%s: read subkey %q, want %q", c.name, subkey, appPathsSubKey)
			}
		}
	}
}

// The resolved path reaches the shell exactly as read: no cleaning and no
// bare-name fallback.
func TestLaunchOutlookRunsTheResolvedPathVerbatim(t *testing.T) {
	const exe = `C:\Program Files\Microsoft Office\root\Office16\OUTLOOK.EXE`
	var ran []string
	lookup := func(appPathRoot, string) (string, bool) { return exe, true }
	shell := func(path string) error { ran = append(ran, path); return nil }
	if err := launchOutlook(lookup, shell); err != nil {
		t.Fatalf("launchOutlook: %v", err)
	}
	if !slices.Equal(ran, []string{exe}) {
		t.Fatalf("ran %q, want exactly [%q]", ran, exe)
	}
}

// With no registration, the shell is never called: certainly not with the
// bare OUTLOOK.EXE it would search for.
func TestLaunchOutlookNeverFallsBackToTheBareName(t *testing.T) {
	var ran []string
	lookup := func(appPathRoot, string) (string, bool) { return "", false }
	shell := func(path string) error { ran = append(ran, path); return nil }
	if err := launchOutlook(lookup, shell); !errors.Is(err, ErrOutlookExeNotFound) {
		t.Fatalf("launchOutlook: %v, want ErrOutlookExeNotFound", err)
	}
	if len(ran) != 0 {
		t.Fatalf("ran %q; nothing must be launched without a registration", ran)
	}
}
