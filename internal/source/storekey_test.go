package source

import (
	"regexp"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// Every per-store setting accepts exactly the keys StoreKey produces.
func TestStoreKeyMatchesTheSchemaPattern(t *testing.T) {
	s, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{SettingSelected} {
		st, ok := s.Lookup(setting)
		if !ok {
			t.Fatalf("%s missing", setting)
		}
		re := regexp.MustCompile(st.Pattern)
		for _, k := range []string{StoreKey("outlook", "any store id"), StoreKey("", "")} {
			if !re.MatchString(k) {
				t.Fatalf("%s: key %q does not match %v", setting, k, re)
			}
		}
	}
	if StoreKey("s", "a") == StoreKey("s", "b") || StoreKey("s1", "a") == StoreKey("s2", "a") {
		t.Fatal("keys of different stores (or of the same store id in two sources) differ")
	}
	if StoreKey("s", "a") != StoreKey("s", "a") {
		t.Fatal("a key is stable")
	}
	// Misuse: the separator keeps ("ab","c") and ("a","bc") apart.
	if StoreKey("ab", "c") == StoreKey("a", "bc") {
		t.Fatal("source and store ids must not run together")
	}
	for _, k := range []string{StoreKey("outlook", "any store id"), StoreKey("", "")} {
		if !ValidStoreKey(k) {
			t.Fatalf("generated key %q is not valid", k)
		}
	}
	for _, k := range []string{"", "store-", "store-0123456789", "store-0123456789a", "store-0123456789g",
		"STORE-0123456789ab", "store-0123456789AB", " store-0123456789ab", "store-0123456789ab=x"} {
		if ValidStoreKey(k) {
			t.Fatalf("invalid key %q accepted", k)
		}
	}
}
