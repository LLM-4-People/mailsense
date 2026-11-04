package secrets

import (
	"maps"
	"slices"
)

// lookup reads one value from a store's values map. A missing name is
// ErrNotFound, the one absence answer of both stores; each store keeps its own
// locking around the call.
func lookup(values map[string]string, name string) (string, error) {
	v, ok := values[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// sortedNames lists a store's names in the one order both stores report (the
// shell and the settings screen compare the lists).
func sortedNames(values map[string]string) []string {
	return slices.Sorted(maps.Keys(values))
}
