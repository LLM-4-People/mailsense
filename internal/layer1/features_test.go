package layer1

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestFeatureCellsAreBoundedDeterministic(t *testing.T) {
	t.Parallel()
	m := Member{
		Subject:        "Réunion budget Q4",
		Body:           "Bonjour, the budget is ready. Réunion demain.",
		SenderAddress:  "alice@atelier-nord.test",
		HasAttachments: true,
		RecipientCount: 2,
		IsReply:        true,
		Depth:          1,
		Hour:           14,
	}
	const base = 100
	total := base + featureCellsCount()
	first := featureCells(m, base)
	second := featureCells(m, base)
	if !slices.Equal(first, second) {
		t.Fatalf("feature cells are not deterministic: %v vs %v", first, second)
	}
	if len(first) == 0 {
		t.Fatal("no feature cells")
	}
	for i, cell := range first {
		if cell < base || int(cell) >= total {
			t.Fatalf("cell %d out of range [%d,%d)", cell, base, total)
		}
		if i > 0 && cell <= first[i-1] {
			t.Fatalf("cells are not sorted and unique: %v", first)
		}
	}
}

func TestFeatureTokensFoldAndDropStopwords(t *testing.T) {
	t.Parallel()
	tokens := featureTokens("Réunion the budget et le projet")
	want := map[string]bool{"reunion": true, "budget": true, "projet": true}
	for _, token := range tokens {
		if !want[token] {
			t.Fatalf("unexpected token %q in %v", token, tokens)
		}
	}
	for token := range want {
		if !slices.Contains(tokens, token) {
			t.Fatalf("missing token %q in %v", token, tokens)
		}
	}
}

func TestFeatureCellsIncludeSenderAndMetadata(t *testing.T) {
	t.Parallel()
	m := Member{Subject: "Invoice", SenderAddress: "billing@vendor.example", Hour: 9}
	cells := featureCells(m, 0)
	domainBase := subjectBuckets + bodyBuckets
	if !slices.ContainsFunc(cells, func(cell int32) bool {
		return int(cell) >= domainBase && int(cell) < domainBase+domainBuckets
	}) {
		t.Fatalf("no sender-domain cell in %v", cells)
	}
	metaBase := subjectBuckets + bodyBuckets + domainBuckets + senderBuckets
	if !slices.Contains(cells, int32(metaBase+8)) { // 7-9h bucket
		t.Fatalf("no hour bucket in %v", cells)
	}
}

// The history block is causal: a message sees only the sender, domain and
// conversation that arrived before it, never the future.
func TestAddHistoryIsCausal(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	members := []Member{
		{ID: 1, Instant: t0, SenderAddress: "alice@vendor.example", Group: "g1"},
		{ID: 2, Instant: t0.Add(48 * time.Hour), SenderAddress: "alice@vendor.example", Group: "g1"},
		{ID: 3, Instant: t0.Add(24 * time.Hour), SenderAddress: "bob@client.example", Group: "g2"},
	}
	addHistory(members)
	if members[1].SenderMessagesBefore != 1 || members[1].SenderFirstDays != 2 ||
		members[1].DomainMessagesBefore != 1 || members[1].DomainFirstDays != 2 {
		t.Fatalf("second message history %+v", members[1])
	}
	if members[2].SenderMessagesBefore != 0 || members[2].SenderFirstDays != 0 ||
		members[2].DomainMessagesBefore != 0 {
		t.Fatalf("first message of a new sender has history %+v", members[2])
	}
}

// The thread block is causal too: it comes from the conversation as it stood
// at the message's instant, never from the final thread groups (a later
// message bridging two conversations changed them).
func TestAddThreadHistoryIsCausal(t *testing.T) {
	t.Parallel()
	var m Member
	addThreadHistory(&m, []bool{false, true})
	if m.ThreadMessagesBefore != 2 || m.ThreadOwnerMessagesBefore != 1 || !m.ThreadLastFromOwner {
		t.Fatalf("after an incoming then an owner message: %+v", m)
	}
	var empty Member
	addThreadHistory(&empty, nil)
	if empty.ThreadMessagesBefore != 0 || empty.ThreadOwnerMessagesBefore != 0 || empty.ThreadLastFromOwner {
		t.Fatalf("a first message has a thread history: %+v", empty)
	}
	var afterOwner Member
	addThreadHistory(&afterOwner, []bool{true, false})
	if afterOwner.ThreadOwnerMessagesBefore != 1 || afterOwner.ThreadLastFromOwner {
		t.Fatalf("the owner spoke first, the correspondent last: %+v", afterOwner)
	}
}

// Reciprocity is direction-aware: only the owner's own mail to a sender
// makes that sender a correspondent, and a quiet spell or a recent burst
// are measured before this message.
func TestAddHistoryReciprocityAndRecency(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	members := []Member{
		{ID: 1, Instant: t0, FromOwner: true, SenderAddress: "owner@example.com",
			Recipients: []string{"alice@vendor.example"}, Group: "g1"},
		{ID: 2, Instant: t0.Add(24 * time.Hour), SenderAddress: "alice@vendor.example", Group: "g1"},
		{ID: 3, Instant: t0.Add(48 * time.Hour), SenderAddress: "alice@vendor.example", Group: "g1"},
		{ID: 4, Instant: t0.AddDate(0, 0, 60), SenderAddress: "alice@vendor.example", Group: "g1"},
		{ID: 5, Instant: t0.Add(25 * time.Hour), SenderAddress: "bob@other.example", Group: "g2"},
	}
	addHistory(members)
	if members[0].OwnerWroteCountBefore != 0 {
		t.Fatalf("an owner message has a reciprocity count: %+v", members[0])
	}
	if members[1].OwnerWroteCountBefore != 1 {
		t.Fatalf("the first reply from alice: the owner's prior mail to her was not counted: %+v", members[1])
	}
	if members[2].SenderMessagesBefore != 1 || members[2].SenderDaysSinceLast != 1 || members[2].SenderMessagesLast30 != 1 {
		t.Fatalf("the second message from alice: %+v", members[2])
	}
	if members[3].SenderMessagesBefore != 2 || members[3].SenderDaysSinceLast != 58 || members[3].SenderMessagesLast30 != 0 {
		t.Fatalf("after a 58-day quiet spell: %+v", members[3])
	}
	if members[4].SenderMessagesBefore != 0 || members[4].OwnerWroteCountBefore != 0 || members[4].SenderMessagesLast30 != 0 {
		t.Fatalf("a new sender has history: %+v", members[4])
	}
}

// A future message never changes an earlier member's history. This is the
// invariant the old final-thread grouping broke: a later bridge re-grouped
// past messages and rewrote their thread counts.
func TestFutureMessagesNeverChangeHistory(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	base := []Member{
		{ID: 1, Instant: t0, FromOwner: true, SenderAddress: "owner@example.com", Recipients: []string{"alice@vendor.example"}, Group: "g1"},
		{ID: 2, Instant: t0.Add(time.Hour), SenderAddress: "alice@vendor.example", Group: "g1"},
		{ID: 3, Instant: t0.Add(2 * time.Hour), SenderAddress: "bob@other.example", Group: "g2"},
	}
	addHistory(base)
	before := append([]Member(nil), base...)
	all := append(append([]Member(nil), base...),
		Member{ID: 4, Instant: t0.Add(3 * time.Hour), FromOwner: true, SenderAddress: "owner@example.com", Recipients: []string{"bob@other.example"}, Group: "g2"},
		Member{ID: 5, Instant: t0.Add(4 * time.Hour), SenderAddress: "alice@vendor.example", Group: "g2"},
	)
	addHistory(all)
	for i := range before {
		if !reflect.DeepEqual(before[i], all[i]) {
			t.Fatalf("message %d changed when later mail arrived: before %+v, after %+v", before[i].ID, before[i], all[i])
		}
	}
}

// The history cells are part of the design and stay inside its bounds.
func TestFeatureCellsIncludeCausalHistory(t *testing.T) {
	t.Parallel()
	m := Member{SenderMessagesBefore: 5, SenderFirstDays: 40, DomainMessagesBefore: 2,
		DomainFirstDays: 10, ThreadMessagesBefore: 3, FromOwner: true,
		OwnerWroteCountBefore: 4, SenderDaysSinceLast: 40, SenderMessagesLast30: 2,
		ThreadOwnerMessagesBefore: 3, ThreadLastFromOwner: true}
	cells := featureCells(m, 0)
	base := subjectBuckets + bodyBuckets + domainBuckets + senderBuckets + metadataCells
	want := []int32{
		int32(base + countBucket(5)),
		int32(base + 8 + countBucket(2)),
		int32(base + 16 + daysBucket(40)),
		int32(base + 23 + daysBucket(10)),
		int32(base + 30 + threadBucket(3)),
		int32(base + 35),
		int32(base + 37 + countBucket(4)),
		int32(base + 45 + daysBucket(40)),
		int32(base + 52 + countBucket(2)),
		int32(base + 60 + threadBucket(3)),
		int32(base + 65),
	}
	for _, cell := range want {
		if !slices.Contains(cells, cell) {
			t.Fatalf("missing history cell %d in %v", cell, cells)
		}
	}
	for _, cell := range cells {
		if int(cell) >= base+historyCells {
			t.Fatalf("history cell %d overflows the %d-cell block", cell, historyCells)
		}
	}
	// A thread the owner did not speak in last has no last-from-owner cell.
	m.ThreadLastFromOwner = false
	if slices.Contains(featureCells(m, 0), int32(base+65)) {
		t.Fatal("a thread whose last message was not the owner's carries the last-from-owner cell")
	}
}
