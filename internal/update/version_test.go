package update

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		text string
	}{
		{"0.1.0", true, "0.1.0"},
		{"v0.1.0", true, "0.1.0"},
		{" 1.2.3 ", true, "1.2.3"},
		{"0.3.0-dev+20261105", true, "0.3.0-dev+20261105"},
		{"1.2.3-rc.1+build.5", true, "1.2.3-rc.1+build.5"},
		{"1.0.0-0", true, "1.0.0-0"},
		{"1.0.0+x-y.z", true, "1.0.0+x-y.z"},
		{"1.0.0+0001", true, "1.0.0+0001"}, // build metadata may carry leading zeros
		{"dev", false, ""},
		{"", false, ""},
		{"   ", false, ""},
		{"vdev", false, ""},
		{"V0.1.0", false, ""},
		{"0.1", false, ""},
		{"0.1.0.0", false, ""},
		{"01.0.0", false, ""},
		{"1.01.0", false, ""},
		{"1.0.0-", false, ""},
		{"1.0.0+", false, ""},
		{"1.0.0-alpha..1", false, ""},
		{"1.0.0-01", false, ""},
		{"1.0.0-alpha_1", false, ""},
		{"1.0.0+meta..1", false, ""},
		{"2026-03-10", false, ""},
		{"v", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			v, ok := ParseVersion(tc.in)
			if ok != tc.ok {
				t.Fatalf("ParseVersion(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if ok && v.String() != tc.text {
				t.Fatalf("ParseVersion(%q).String() = %q, want %q", tc.in, v.String(), tc.text)
			}
			if !ok && v.String() != "" {
				t.Fatalf("invalid version %q has text %q", tc.in, v.String())
			}
		})
	}
}

func TestCompare(t *testing.T) {
	must := func(s string) Version {
		t.Helper()
		v, ok := ParseVersion(s)
		if !ok {
			t.Fatalf("test version %q does not parse", s)
		}
		return v
	}
	invalid, _ := ParseVersion("dev")
	cases := []struct {
		name string
		a, b string
		want int
	}{
		{"equal", "0.1.0", "0.1.0", 0},
		{"v prefix is equal", "v0.1.0", "0.1.0", 0},
		{"build metadata is ignored", "1.0.0+a", "1.0.0+b", 0},
		{"build metadata on a prerelease is ignored", "0.3.0-dev+20261105", "0.3.0-dev", 0},
		{"patch wins", "0.1.1", "0.1.0", 1},
		{"minor wins over patch", "0.2.0", "0.1.9", 1},
		{"major wins", "1.0.0", "0.99.99", 1},
		{"downgrade", "0.1.0", "0.2.0", -1},
		{"prerelease sorts before release", "1.0.0-rc.1", "1.0.0", -1},
		{"release sorts after prerelease", "1.0.0", "1.0.0-rc.1", 1},
		{"numeric prerelease before alphanumeric", "1.0.0-1", "1.0.0-alpha", -1},
		{"numeric prerelease compares numerically", "1.0.0-11", "1.0.0-2", 1},
		{"fewer prerelease fields first", "1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"semver spec chain alpha < alpha.1", "1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"semver spec chain alpha.1 < alpha.beta", "1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"semver spec chain alpha.beta < beta", "1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"semver spec chain beta < beta.2", "1.0.0-beta", "1.0.0-beta.2", -1},
		{"semver spec chain beta.2 < beta.11", "1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"semver spec chain beta.11 < rc.1", "1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"semver spec chain rc.1 < release", "1.0.0-rc.1", "1.0.0", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Compare(must(tc.a), must(tc.b)); got != tc.want {
				t.Fatalf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			if got := Compare(invalid, must(tc.b)); got != 0 {
				t.Fatalf("Compare(invalid, %q) = %d, want 0", tc.b, got)
			}
		})
	}
}

// TestCompareUnparseableIsNotAZeroVersion pins the documented behavior: "dev"
// and every malformed string are unparseable, never a 0.0.0 build. Compare
// answers 0 for an invalid side because it is not comparable at all; the
// parsed flag is the only authority, and the status checks it before ordering.
func TestCompareUnparseableIsNotAZeroVersion(t *testing.T) {
	for _, raw := range []string{"dev", "not-a-version", ""} {
		t.Run(raw, func(t *testing.T) {
			v, ok := ParseVersion(raw)
			if ok {
				t.Fatalf("ParseVersion(%q) unexpectedly parsed", raw)
			}
			if v.valid {
				t.Fatalf("ParseVersion(%q) marked the zero value comparable", raw)
			}
			if v.String() != "" {
				t.Fatalf("unparseable %q rendered as %q", raw, v.String())
			}
			for _, other := range []string{"0.0.0", "0.0.1", "0.1.0"} {
				parsed, ok := ParseVersion(other)
				if !ok {
					t.Fatalf("test version %q does not parse", other)
				}
				if got := Compare(v, parsed); got != 0 {
					t.Fatalf("Compare(%q, %q) = %d, want the incomparable answer 0", raw, other, got)
				}
				if got := Compare(parsed, v); got != 0 {
					t.Fatalf("Compare(%q, %q) = %d, want the incomparable answer 0", other, raw, got)
				}
			}
		})
	}
}

func TestVersionsBehind(t *testing.T) {
	must := func(s string) Version {
		t.Helper()
		v, ok := ParseVersion(s)
		if !ok {
			t.Fatalf("test version %q does not parse", s)
		}
		return v
	}
	cases := []struct {
		name    string
		current string
		latest  string
		tags    []string
		want    int
	}{
		{"counts strictly between", "0.1.0", "0.5.0", []string{"v0.5.0", "v0.4.0", "v0.3.0", "v0.2.0", "v0.1.0"}, 3},
		{"endpoints excluded", "0.1.0", "0.2.0", []string{"0.1.0", "0.2.0"}, 0},
		{"duplicate precedence counted once", "0.1.0", "0.4.0", []string{"0.3.0+a", "0.3.0+b", "0.3.0"}, 1},
		{"malformed and unrelated tags ignored", "0.1.0", "0.4.0", []string{"nightly", "dev", "0.9.0", "0.4.0"}, 0},
		{"prerelease inside the range counts", "0.1.0", "0.2.0", []string{"0.2.0-rc.1", "0.2.0"}, 1},
		{"prerelease endpoints excluded", "0.1.0-rc.1", "0.1.0", []string{"0.1.0-beta.1", "0.1.0-rc.2"}, 1},
		{"equal endpoints count zero", "0.2.0", "0.2.0", []string{"0.1.0", "0.3.0"}, 0},
		{"downgrade counts zero", "0.5.0", "0.2.0", []string{"0.3.0", "0.4.0"}, 0},
		{"no tags counts zero", "0.1.0", "0.2.0", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VersionsBehind(must(tc.current), must(tc.latest), tc.tags); got != tc.want {
				t.Fatalf("VersionsBehind(%q, %q, %v) = %d, want %d", tc.current, tc.latest, tc.tags, got, tc.want)
			}
		})
	}
}
