package index

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestAttemptedAddsUpToSelected: progress and the persisted report derive the
// attempted count from one place, and the parts always add up to Selected.
func TestAttemptedAddsUpToSelected(t *testing.T) {
	for _, rep := range []Report{
		{},
		{Selected: 10, Embedded: 3},
		{Selected: 10, Unchanged: 4, Failed: 1},
		{Selected: 10, Embedded: 2, Unchanged: 2, Failed: 2, NotAttempted: 4},
		{Selected: 0},
	} {
		if got, want := rep.Attempted(), rep.Embedded+rep.Unchanged+rep.Failed; got != want {
			t.Fatalf("Attempted() = %d, want %d for %+v", got, want, rep)
		}
		if rep.Selected > 0 && rep.Attempted()+rep.NotAttempted != rep.Selected && rep.NotAttempted != 0 {
			t.Fatalf("Attempted %d + NotAttempted %d != Selected %d for %+v",
				rep.Attempted(), rep.NotAttempted, rep.Selected, rep)
		}
	}

	// A real run: the report it persists satisfies the invariant.
	w := newWorld(t, nil)
	for i := range 4 {
		w.mail(string(rune('a'+i)), mail.RoleInbox, i, "body")
	}
	rep := w.run()
	if rep.Attempted()+rep.NotAttempted != rep.Selected {
		t.Fatalf("ran %+v: attempted %d + not attempted %d != selected %d",
			rep, rep.Attempted(), rep.NotAttempted, rep.Selected)
	}
}

// TestNoEmbedderBlockerIsOneAnswer: the stage's blocked-by-embedder answer names
// one code and one remedy wherever it is built.
func TestNoEmbedderBlockerIsOneAnswer(t *testing.T) {
	blocker := noEmbedderBlocker("the embedder is not installed")
	if blocker.Code != BlockNoEmbedder || blocker.Detail != "the embedder is not installed" {
		t.Fatalf("blocker %+v", blocker)
	}
	if len(blocker.Actions) != 1 || blocker.Actions[0].ID != learning.ActionChooseEmbedder {
		t.Fatalf("blocker actions %+v", blocker.Actions)
	}

	data, err := os.ReadFile("stage.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "learning.Blocker{Code: BlockNoEmbedder"); got != 1 {
		t.Fatalf("the no-embedder blocker is built %d times, want the one helper", got)
	}
}
