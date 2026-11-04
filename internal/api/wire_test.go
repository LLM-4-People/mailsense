package api

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// The thread card is owner-facing. A shared/delegate reply sets ActorName
// but not OwnerActorName, so the wire must carry no actor rather than name a
// colleague.
func TestThreadStateViewNamesOnlyOwnerFacingActors(t *testing.T) {
	shared := ThreadStateViewOf(threadintel.Assessment{ActorName: "Colleague", OwnerActorName: ""})
	if shared.Actor != "" {
		t.Fatalf("a shared reply named its evidence actor: %q", shared.Actor)
	}
	own := ThreadStateViewOf(threadintel.Assessment{ActorName: "Owner", OwnerActorName: "Owner"})
	if own.Actor != "Owner" {
		t.Fatalf("an owner reply lost its actor: %q", own.Actor)
	}
}
