package fsx

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestValidatePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		ok   bool
	}{
		{"clean absolute", filepath.Join(string(filepath.Separator), "data", "file"), true},
		{"clean relative", filepath.Join("data", "file"), true},
		{"dot", ".", true},
		{"empty", "", false},
		{"nul", "a\x00b", false},
		{"not cleaned", "a" + string(filepath.Separator) + string(filepath.Separator) + "b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePath(tc.path)
			if tc.ok && err != nil {
				t.Fatalf("%q: %v", tc.path, err)
			}
			if !tc.ok && !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("%q: want ErrUnsafePath, got %v", tc.path, err)
			}
		})
	}
}

func TestWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "data", "sidecar")
	for _, tc := range []struct {
		name string
		root string
		path string
		want string
		ok   bool
	}{
		{"relative name", root, "model.bin", filepath.Join(root, "model.bin"), true},
		{"nested relative", root, filepath.Join("a", "b"), filepath.Join(root, "a", "b"), true},
		{"already inside", root, filepath.Join(root, "a"), filepath.Join(root, "a"), true},
		{"the root itself", root, root, root, true},
		{"dot", root, ".", root, true},
		{"volume root allows its tree", string(filepath.Separator), filepath.Join("etc", "x"), filepath.Join(string(filepath.Separator), "etc", "x"), true},

		{"absolute outside", root, filepath.Join(string(filepath.Separator), "etc", "passwd"), "", false},
		{"parent escape", root, filepath.Join("..", "outside"), "", false},
		{"prefix sibling", root, root + "x", "", false},
		{"empty root", "", "x", "", false},
		{"empty path", root, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Within(tc.root, tc.path)
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("Within(%q, %q) = %q, %v; want %q", tc.root, tc.path, got, err, tc.want)
				}
				return
			}
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Within(%q, %q) = %q, %v; want ErrUnsafePath", tc.root, tc.path, got, err)
			}
		})
	}
}
