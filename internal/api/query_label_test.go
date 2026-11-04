package api

import (
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/layer1"
)

// TestQueryLabelHasOneOwner: the queue route and the message binding read the
// same parser, and neither re-bounds the limit or re-reads the review flag.
func TestQueryLabelHasOneOwner(t *testing.T) {
	for _, name := range []string{"routes_labels.go", "routes_messages.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if !strings.Contains(source, "queryLabel(r)") {
			t.Errorf("%s does not read the queue query through queryLabel", name)
		}
	}
	labels, err := os.ReadFile("routes_labels.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(labels), `boundedQueryInt(r, "limit", 0, 0, layer1.LabelQueueMax)`); got != 1 {
		t.Errorf("routes_labels.go bounds the queue limit %d times, want once in queryLabel", got)
	}
	if got := strings.Count(string(labels), "func queryReview("); got != 1 {
		t.Errorf("queryReview is defined %d times, want once", got)
	}
	if got := strings.Count(string(labels), "query.Limit < 0"); got != 0 {
		t.Errorf("labelQueueFor re-bounds the limit; queryLabel owns the bound")
	}
}

// TestQueryLabelShape: the parser accepts the documented shape and refuses a
// malformed limit or review flag, with the exact wire codes.
func TestQueryLabelShape(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want layer1.LabelQuery
		code string
	}{
		{"default", "?question=category", layer1.LabelQuery{Question: "category"}, ""},
		{"limit and review", "?question=category&limit=3&review=1", layer1.LabelQuery{Question: "category", Limit: 3, Review: true}, ""},
		{"review off", "?question=category&review=false", layer1.LabelQuery{Question: "category"}, ""},
		{"limit at bound", "?question=category&limit=" + strconv.Itoa(layer1.LabelQueueMax), layer1.LabelQuery{Question: "category", Limit: layer1.LabelQueueMax}, ""},
		{"limit past bound", "?question=category&limit=" + strconv.Itoa(layer1.LabelQueueMax+1), layer1.LabelQuery{}, "invalid_limit"},
		{"negative limit", "?question=category&limit=-1", layer1.LabelQuery{}, "invalid_limit"},
		{"bad review", "?question=category&review=maybe", layer1.LabelQuery{}, "invalid_review"},
	} {
		got, err := queryLabel(httptest.NewRequest("GET", "/api/labels/queue"+c.raw, nil))
		if c.code != "" {
			if err == nil || errorCodeString(err) != c.code {
				t.Errorf("%s: err = %v, want %s", c.name, err, c.code)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, err = %v; want %+v", c.name, got, err, c.want)
		}
	}
}
