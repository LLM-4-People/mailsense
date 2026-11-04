package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// TestOrderCompanyThreadsIsTheListContract pins the one ordering and cap of a
// company's open threads: state order, then newest first, then conversation id
// so equal timestamps have a stable order; and never more than the display cap.
func TestOrderCompanyThreadsIsTheListContract(t *testing.T) {
	open := []CompanyThread{
		{ConversationID: "z-noaction", State: string(threadintel.NoAction), LastAt: "2026-03-01T10:00:00Z"},
		{ConversationID: "b-owner-old", State: string(threadintel.AwaitingOwner), LastAt: "2026-03-01T09:00:00Z"},
		{ConversationID: "a-owner-new", State: string(threadintel.AwaitingOwner), LastAt: "2026-03-02T09:00:00Z"},
		{ConversationID: "c-owner-tie", State: string(threadintel.AwaitingOwner), LastAt: "2026-03-02T09:00:00Z"},
		{ConversationID: "d-bogus", State: "zzz", LastAt: "2026-03-03T09:00:00Z"},
		{ConversationID: "e-unknown", State: string(threadintel.Unknown), LastAt: "2026-03-03T09:00:00Z"},
		{ConversationID: "f-other", State: string(threadintel.AwaitingOther), LastAt: "2026-03-01T09:00:00Z"},
	}
	got := OrderCompanyThreads(open)
	want := []string{"a-owner-new", "c-owner-tie", "b-owner-old", "f-other", "z-noaction", "e-unknown", "d-bogus"}
	if ids := conversationIDs(got); !slices.Equal(ids, want) {
		t.Fatalf("order %v, want %v", ids, want)
	}

	// The cap is the same fact as the order: a caller cannot receive the whole
	// list by sorting it another way.
	many := make([]CompanyThread, 0, CompanyProfileOpenCap+7)
	for i := range CompanyProfileOpenCap + 7 {
		many = append(many, CompanyThread{ConversationID: string(rune('a' + i)), State: string(threadintel.AwaitingOwner),
			LastAt: "2026-03-02T09:00:00Z"})
	}
	capped := OrderCompanyThreads(many)
	if len(capped) != CompanyProfileOpenCap {
		t.Fatalf("%d threads after the cap, want %d", len(capped), CompanyProfileOpenCap)
	}
	if capped[0].ConversationID != "a" || capped[len(capped)-1].ConversationID != string(rune('a'+CompanyProfileOpenCap-1)) {
		t.Fatalf("the cap kept the wrong window: first %q last %q", capped[0].ConversationID, capped[len(capped)-1].ConversationID)
	}
	// An already-empty list stays empty and is not nil-filled.
	if got := OrderCompanyThreads(nil); len(got) != 0 {
		t.Fatalf("nil list became %v", got)
	}
}

func conversationIDs(threads []CompanyThread) []string {
	out := make([]string, len(threads))
	for i, th := range threads {
		out[i] = th.ConversationID
	}
	return out
}

// TestCompanyThreadOrderHasOneOwner: the profile route and the hub's company
// projection show the same open-thread list, so its order and cap live only in
// OrderCompanyThreads. world used to spell the sort and the cap itself, which
// could drift by a tie-break or an uncapped list without any test noticing.
func TestCompanyThreadOrderHasOneOwner(t *testing.T) {
	orderIn := func(dir string) []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var files []string
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			if strings.Contains(source, "threadintel.StateOrder") {
				files = append(files, name)
			}
			if strings.Contains(source, "CompanyProfileOpenCap") && name != "companyprofile.go" {
				t.Fatalf("%s applies the open-thread cap outside companyprofile.go", filepath.Join(dir, name))
			}
		}
		return files
	}
	if files := orderIn("."); !slices.Equal(files, []string{"companyprofile.go"}) {
		t.Fatalf("the open-thread order lives in %v, want only companyprofile.go's OrderCompanyThreads", files)
	}
	if files := orderIn(filepath.Join("..", "world")); len(files) != 0 {
		t.Fatalf("world spells the open-thread order itself in %v", files)
	}
	world, err := os.ReadFile(filepath.Join("..", "world", "world.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(world), "store.OrderCompanyThreads(open)") {
		t.Fatal("world no longer delegates the open-thread order to store.OrderCompanyThreads")
	}
}
