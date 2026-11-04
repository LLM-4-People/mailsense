package conversations

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// conversationPageQuery is the search every fixture subject carries, so the
// text path projects exactly the rows of the unfiltered base path. It folds to
// conversationPageSearch before the page is built.
const (
	conversationPageQuery  = "Budget"
	conversationPageSearch = "budget"
)

// conversationPageFixture seeds one mailbox where a single search matches
// every conversation. The seven rows span five states (two twice) and leave
// the sixth empty, so paging, a state filter and an empty chip all run over
// the same rows.
func conversationPageFixture(t *testing.T) *Service {
	t.Helper()
	states := map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
		"C2": stateFor("C2", threadintel.AwaitingOwner),
		"C3": stateFor("C3", threadintel.AwaitingOther),
		"C4": stateFor("C4", threadintel.AnsweredByOwner),
		"C5": stateFor("C5", threadintel.AnsweredByOther),
		"C6": stateFor("C6", threadintel.AwaitingOther),
		"C7": stateFor("C7", threadintel.NoAction),
	}
	svc, st, _, _ := newTestService(t, &fakePriority{states: states})
	seed := []struct {
		id, conversation, subject, instant string
	}{
		{"c1", "C1", "Budget kickoff", "2026-11-07T09:00:00Z"},
		{"c2", "C2", "Budget plan", "2026-11-06T09:00:00Z"},
		{"c3", "C3", "Budget review", "2026-11-05T09:00:00Z"},
		{"c4", "C4", "Budget invoice", "2026-11-04T09:00:00Z"},
		{"c5", "C5", "Budget archive", "2026-11-03T09:00:00Z"},
		{"c6", "C6", "Budget follow-up", "2026-11-02T09:00:00Z"},
		{"c7", "C7", "Budget chatter", "2026-11-01T09:00:00Z"},
	}
	for _, msg := range seed {
		seedConversation(t, st, msg.id, msg.conversation, msg.subject, msg.instant, nil)
	}
	return svc
}

// decodePageCursor decodes one page cursor, reporting false when the page is
// the last one and carries no cursor.
func decodePageCursor(t *testing.T, value string) (cursor, bool) {
	t.Helper()
	if value == "" {
		return cursor{}, false
	}
	decoded, err := decodeCursor(value)
	if err != nil {
		t.Fatalf("page cursor %q: %v", value, err)
	}
	return decoded, true
}

// assertConversationPagesAgree pins the parity of the two page paths over the
// same rows: the base path serves them unfiltered and the text path finds them
// with search, so every field the page step owns must match. The encoded
// cursor carries the query it was issued for (that tag is what rejects a
// cursor from another query), so the base cursor is untagged while the text
// cursor carries the folded search; the keyset position must still be the
// same.
func assertConversationPagesAgree(t *testing.T, base, text Page, search string) {
	t.Helper()
	if !reflect.DeepEqual(base.Conversations, text.Conversations) {
		t.Fatalf("conversations differ:\nbase %+v\ntext %+v", base.Conversations, text.Conversations)
	}
	if base.Total != text.Total {
		t.Fatalf("total %d on the base path, %d on the text path", base.Total, text.Total)
	}
	if base.HasMore != text.HasMore {
		t.Fatalf("has_more %v on the base path, %v on the text path", base.HasMore, text.HasMore)
	}
	if !reflect.DeepEqual(base.Counts, text.Counts) {
		t.Fatalf("counts differ:\nbase %+v\ntext %+v", base.Counts, text.Counts)
	}
	if base.GeneratedAt != text.GeneratedAt {
		t.Fatalf("generated_at %q on the base path, %q on the text path", base.GeneratedAt, text.GeneratedAt)
	}
	if base.Preview != text.Preview || base.Coverage != text.Coverage {
		t.Fatalf("preview/coverage differ:\nbase %+v\ntext %+v", base, text)
	}

	baseCursor, baseOK := decodePageCursor(t, base.NextBefore)
	textCursor, textOK := decodePageCursor(t, text.NextBefore)
	if baseOK != textOK {
		t.Fatalf("next cursor presence differs: base %q, text %q", base.NextBefore, text.NextBefore)
	}
	if !baseOK {
		return
	}
	if baseCursor.At != textCursor.At || baseCursor.ID != textCursor.ID || baseCursor.State != textCursor.State {
		t.Fatalf("next cursor position differs: base %+v, text %+v", baseCursor, textCursor)
	}
	if baseCursor.Q != "" || textCursor.Q != search {
		t.Fatalf("cursor query tag base %q text %q, want %q and %q",
			baseCursor.Q, textCursor.Q, "", search)
	}
}

// TestConversationPagePathsAgree serves one fixture through the snapshot base
// path (no query) and the text scan (the search every row carries). Both
// projections must produce one page: same rows, same total before the cursor,
// same counts, same next cursor position.
func TestConversationPagePathsAgree(t *testing.T) {
	svc := conversationPageFixture(t)

	for _, tc := range []struct {
		name  string
		query Query
		want  []string
		total int
		more  bool
	}{
		{"first page", Query{Limit: 3}, []string{"C1", "C2", "C3"}, 7, true},
		{"state filter", Query{Limit: 2, State: threadintel.AwaitingOwner}, []string{"C1", "C2"}, 2, false},
		{"empty result", Query{Limit: 3, State: threadintel.Unknown}, nil, 0, false},
		{"page boundary", Query{Limit: 7},
			[]string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := pageUncached(t, svc, tc.query)
			textQuery := tc.query
			textQuery.Q = conversationPageQuery
			text := pageUncached(t, svc, textQuery)

			var ids []string
			for _, row := range base.Conversations {
				ids = append(ids, row.ConversationID)
			}
			if !slices.Equal(ids, tc.want) || base.Total != tc.total || base.HasMore != tc.more {
				t.Fatalf("base page ids %v total %d more %v, want ids %v total %d more %v",
					ids, base.Total, base.HasMore, tc.want, tc.total, tc.more)
			}
			if got := base.NextBefore != ""; got != tc.more {
				t.Fatalf("base next cursor %q, want more %v", base.NextBefore, tc.more)
			}
			assertConversationPagesAgree(t, base, text, conversationPageSearch)
		})
	}

	t.Run("second page by cursor", func(t *testing.T) {
		baseFirst := pageUncached(t, svc, Query{Limit: 3})
		textFirst := pageUncached(t, svc, Query{Limit: 3, Q: conversationPageQuery})
		assertConversationPagesAgree(t, baseFirst, textFirst, conversationPageSearch)

		base := pageUncached(t, svc, Query{Limit: 3, Before: baseFirst.NextBefore})
		text := pageUncached(t, svc, Query{Limit: 3, Q: conversationPageQuery, Before: textFirst.NextBefore})
		var ids []string
		for _, row := range base.Conversations {
			ids = append(ids, row.ConversationID)
		}
		// Total still counts the state-filtered rows before the cursor.
		if want := []string{"C4", "C5", "C6"}; !slices.Equal(ids, want) || base.Total != 7 || !base.HasMore {
			t.Fatalf("second page ids %v total %d more %v, want ids %v total 7 more true",
				ids, base.Total, base.HasMore, want)
		}
		assertConversationPagesAgree(t, base, text, conversationPageSearch)
	})

	t.Run("empty mailbox", func(t *testing.T) {
		empty, _, _, _ := newTestService(t, &fakePriority{})
		base := pageUncached(t, empty, Query{Limit: 3})
		text := pageUncached(t, empty, Query{Limit: 3, Q: conversationPageQuery})
		if base.Total != 0 || len(base.Conversations) != 0 || base.NextBefore != "" || base.HasMore {
			t.Fatalf("empty base page %+v", base)
		}
		assertConversationPagesAgree(t, base, text, conversationPageSearch)
		// An empty result must not skip cursor validation: a malformed cursor
		// is a bad request even when there is nothing to page over.
		if _, err := empty.Page(t.Context(), Query{Limit: 3, Before: "not-a-cursor"}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("an empty mailbox accepted a malformed cursor: %v", err)
		}
	})
}

// TestConversationCursorValidationIsOneRule pins the one cursor contract on
// both paths: the same malformed cursor and the same cursor from another
// query/state fail with the identical ErrInvalidCursor error, and each path
// accepts its own cursor.
func TestConversationCursorValidationIsOneRule(t *testing.T) {
	svc := conversationPageFixture(t)
	const malformed = "conversations: invalid cursor: malformed before cursor"
	const foreign = "conversations: invalid cursor: cursor belongs to another query"

	baseFirst := pageUncached(t, svc, Query{Limit: 1, State: threadintel.AwaitingOwner})
	textFirst := pageUncached(t, svc, Query{Limit: 1, Q: conversationPageQuery, State: threadintel.AwaitingOwner})
	if baseFirst.NextBefore == "" || textFirst.NextBefore == "" {
		t.Fatalf("the fixture must page: base %q text %q", baseFirst.NextBefore, textFirst.NextBefore)
	}
	// The positive control: each path continues from its own cursor.
	pageUncached(t, svc, Query{Limit: 1, State: threadintel.AwaitingOwner, Before: baseFirst.NextBefore})
	pageUncached(t, svc, Query{Limit: 1, Q: conversationPageQuery, State: threadintel.AwaitingOwner, Before: textFirst.NextBefore})

	for _, tc := range []struct {
		name string
		base Query
		text Query
		want string
	}{
		{
			"malformed base64",
			Query{Before: "!!!"},
			Query{Q: conversationPageQuery, Before: "!!!"},
			malformed,
		},
		{
			"malformed payload",
			Query{Before: "bm90LWpzb24"},
			Query{Q: conversationPageQuery, Before: "bm90LWpzb24"},
			malformed,
		},
		{
			"another query",
			Query{Before: textFirst.NextBefore},
			Query{Q: "Budget review", Before: textFirst.NextBefore},
			foreign,
		},
		{
			"another state",
			Query{State: threadintel.NoAction, Before: baseFirst.NextBefore},
			Query{Q: conversationPageQuery, State: threadintel.NoAction, Before: textFirst.NextBefore},
			foreign,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseErr := pageUncachedErr(t, svc, tc.base)
			textErr := pageUncachedErr(t, svc, tc.text)
			for _, got := range []struct {
				path string
				err  error
			}{
				{"base", baseErr},
				{"text", textErr},
			} {
				if !errors.Is(got.err, ErrInvalidCursor) {
					t.Fatalf("%s path: error %v, want ErrInvalidCursor", got.path, got.err)
				}
				if got.err.Error() != tc.want {
					t.Fatalf("%s path: error %q, want %q", got.path, got.err, tc.want)
				}
			}
			if baseErr.Error() != textErr.Error() {
				t.Fatalf("the paths disagree: base %q, text %q", baseErr, textErr)
			}
		})
	}
}
