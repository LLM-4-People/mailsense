package priority

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// rankedRows builds count rows that all enter the ranking with the same
// score. Ties break by message id descending, so the page boundaries are
// deterministic without depending on a store order.
func rankedRows(count int) []store.PriorityRow {
	rows := make([]store.PriorityRow, 0, count)
	for i := 1; i <= count; i++ {
		rows = append(rows, store.PriorityRow{MessageID: int64(i), Subject: "s", ReceivedAt: receivedAgo(0),
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}})
	}
	return rows
}

func TestRankPagePagesWithoutDuplicatesAndStopsHonestly(t *testing.T) {
	svc, _ := newService(t, rankedRows(120), false)
	first, err := svc.RankPage(t.Context(), FilterAll, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 50 || first.Offset != 0 || first.Available != 120 ||
		first.Total != 120 || !first.HasMore {
		t.Fatalf("first page %+v", first)
	}
	second, err := svc.RankPage(t.Context(), FilterAll, 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 50 || second.Offset != 50 || second.Available != 120 ||
		second.Total != 120 || !second.HasMore {
		t.Fatalf("second page %+v", second)
	}
	// Item 51 is the first item of page 2, and the two pages do not repeat
	// the last row of page 1.
	if second.Items[0].MessageID != first.Items[len(first.Items)-1].MessageID-1 {
		t.Fatalf("page 2 starts at %d after page 1 ends at %d",
			second.Items[0].MessageID, first.Items[len(first.Items)-1].MessageID)
	}
	third, err := svc.RankPage(t.Context(), FilterAll, 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Items) != 20 || third.Offset != 100 || third.Available != 120 ||
		third.Total != 120 || third.HasMore {
		t.Fatalf("final page %+v", third)
	}
	end, err := svc.RankPage(t.Context(), FilterAll, 50, 120)
	if err != nil {
		t.Fatal(err)
	}
	if end.Items == nil || len(end.Items) != 0 || end.Offset != 120 ||
		end.Available != 120 || end.Total != 120 || end.HasMore {
		t.Fatalf("end page %+v", end)
	}

	seen := map[int64]bool{}
	for _, page := range []Page{first, second, third} {
		for _, item := range page.Items {
			if seen[item.MessageID] {
				t.Fatalf("message %d appeared twice", item.MessageID)
			}
			seen[item.MessageID] = true
		}
	}
	if len(seen) != 120 {
		t.Fatalf("paged %d distinct messages, want 120", len(seen))
	}

	// A negative offset is caller misuse, not an empty page.
	if _, err := svc.RankPage(t.Context(), FilterAll, 50, -1); err == nil {
		t.Fatal("a negative offset was accepted")
	}
}

// The ranking snapshot keeps at most MaxCached rows even when the filter
// matches more. Total stays the filter's full count, while Available is the
// honest pageable depth.
func TestRankPageStopsAtTheCachedTopWhenTotalIsLarger(t *testing.T) {
	svc, _ := newService(t, rankedRows(250), false)
	first, err := svc.RankPage(t.Context(), FilterAll, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 250 || first.Available != MaxCached || !first.HasMore || len(first.Items) != 50 {
		t.Fatalf("first page %+v", first)
	}
	end, err := svc.RankPage(t.Context(), FilterAll, 50, MaxCached)
	if err != nil {
		t.Fatal(err)
	}
	if end.Items == nil || len(end.Items) != 0 || end.Offset != MaxCached ||
		end.Available != MaxCached || end.Total != 250 || end.HasMore {
		t.Fatalf("end of the pageable snapshot %+v", end)
	}
}
