package api

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestQueryStoreHasOneOwner: both pages read the same filter parser, and the
// source-key check is spelled once in the package.
func TestQueryStoreHasOneOwner(t *testing.T) {
	for _, name := range []string{"routes_messages.go", "routes_search.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "ValidStoreKey") {
			t.Errorf("%s validates the store filter itself; use queryStore", name)
		}
		if !strings.Contains(string(data), "queryStore(r)") {
			t.Errorf("%s does not read the store filter through queryStore", name)
		}
	}
	request := func(raw string) error {
		_, _, err := queryStore(httptest.NewRequest("GET", "/api/messages"+raw, nil))
		return err
	}
	if err := request("?store=not%20a%20key"); err == nil || errorCodeString(err) != "invalid_store" {
		t.Fatalf("queryStore(bad key) = %v, want invalid_store", err)
	}
	if err := request(""); err != nil {
		t.Fatalf("queryStore(absent) = %v", err)
	}
}

// TestQueryRoleGrammar: the All-mail grammar accepts "all" and an empty value;
// the search grammar rejects both. One parser, two declared vocabularies.
func TestQueryRoleGrammar(t *testing.T) {
	for _, c := range []struct {
		name    string
		raw     string
		grammar RoleGrammar
		want    string
		present bool
		ok      bool
	}{
		{"mail all", "?role=all", RoleGrammar{AllowAll: true, AllowEmpty: true}, "all", true, true},
		{"mail empty", "?role=", RoleGrammar{AllowAll: true, AllowEmpty: true}, "", true, true},
		{"mail inbox", "?role=inbox", RoleGrammar{AllowAll: true, AllowEmpty: true}, "inbox", true, true},
		{"mail bad", "?role=nope", RoleGrammar{AllowAll: true, AllowEmpty: true}, "", true, false},
		{"search empty", "?role=", RoleGrammar{}, "", true, false},
		{"search all", "?role=all", RoleGrammar{}, "", true, false},
		{"search sent", "?role=sent", RoleGrammar{}, "sent", true, true},
		{"absent", "", RoleGrammar{}, "", false, true},
	} {
		role, present, err := queryRole(httptest.NewRequest("GET", "/api/x"+c.raw, nil), c.grammar)
		if present != c.present {
			t.Errorf("%s: present = %v, want %v", c.name, present, c.present)
		}
		if c.ok {
			if err != nil || string(role) != c.want {
				t.Errorf("%s: role = %q, err = %v; want %q", c.name, role, err, c.want)
			}
			continue
		}
		if err == nil || errorCodeString(err) != "invalid_role" {
			t.Errorf("%s: err = %v, want invalid_role", c.name, err)
		}
	}
}

// errorCodeString reads the wire code out of a typed API error.
func errorCodeString(err error) string {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code
	}
	return ""
}
