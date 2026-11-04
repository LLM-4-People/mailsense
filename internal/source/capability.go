package source

import (
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// capabilityFields maps each capability name (its json tag, the one name
// layers and stored data use) to its field index.
var capabilityFields = sync.OnceValue(func() map[string]int {
	out := map[string]int{}
	t := reflect.TypeFor[Capabilities]()
	for i := range t.NumField() {
		out[t.Field(i).Tag.Get("json")] = i
	}
	return out
})

// CapabilityNames lists every capability name, sorted.
func CapabilityNames() []string {
	var out []string
	for k := range capabilityFields() {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Has reports whether the capability of that name is set; an unknown name
// is an error (a layer asking for a capability no source can declare).
func (c Capabilities) Has(name string) (bool, error) {
	i, ok := capabilityFields()[name]
	if !ok {
		return false, fmt.Errorf("source: unknown capability %q", name)
	}
	return reflect.ValueOf(c).Field(i).Bool(), nil
}
