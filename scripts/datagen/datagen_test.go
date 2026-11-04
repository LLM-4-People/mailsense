package main

import (
	"strings"
	"testing"
)

func TestCodeRangeRejectsOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"single", "0041", true},
		{"range", "0041..005A", true},
		{"last scalar", "10FFFF", true},
		{"past the last scalar", "110000", false},
		{"uint32 max", "FFFFFFFF", false},
		{"low surrogate", "D800", false},
		{"high surrogate", "DFFF", false},
		{"reversed", "005A..0041", false},
		{"not hex", "0x41", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi, err := codeRange(tc.in)
			if tc.ok {
				if err != nil || lo > hi {
					t.Fatalf("codeRange(%q) = %U..%U, %v", tc.in, lo, hi, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("codeRange(%q) = %U..%U, want an error", tc.in, lo, hi)
			}
		})
	}
}

func TestCodePointsRejectOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"one", "0041", "A", true},
		{"several", "0041 0042", "AB", true},
		{"last scalar", "10FFFF", "\U0010FFFF", true},
		{"past the last scalar", "110000", "", false},
		{"uint32 max", "FFFFFFFF", "", false},
		{"surrogate", "D800", "", false},
		{"not hex", "zz", "", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := codePoints(tc.in)
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("codePoints(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "code point") {
				t.Fatalf("codePoints(%q) = %q, %v; want a named error", tc.in, got, err)
			}
		})
	}
}
