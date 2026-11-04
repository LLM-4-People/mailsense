package outlook

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Rules review 1b §6.1: misuse paths on the fake.

func TestCancellationMidOperation(t *testing.T) {
	for _, at := range []string{"Folders.GetNext", "Table.GetArray", "Item.Body", "Item.HTMLBody"} {
		o := profile()
		c, _ := testConnector(t, o, nil)
		folders, err := c.Discover(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		in := folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
		ctx, cancel := context.WithCancel(t.Context())
		o.onCall = func(key string) {
			if key == at {
				cancel()
			}
		}
		var opErr error
		switch at {
		case "Folders.GetNext":
			_, opErr = c.Discover(ctx)
		case "Table.GetArray":
			cur, err := c.Sync(ctx, in, "", &memSink{})
			if cur != "" {
				t.Errorf("a cancelled sync returns no cursor: %v", cur)
			}
			opErr = err
		case "Item.Body", "Item.HTMLBody":
			o.onCall = nil
			sk := &memSink{}
			if _, err := c.Sync(t.Context(), in, "", sk); err != nil {
				t.Fatal(err)
			}
			o.onCall = func(key string) {
				if key == at {
					cancel()
				}
			}
			_, opErr = c.ReadDetails(ctx, refOf(sk.msgs[0]))
		}
		if !errors.Is(opErr, context.Canceled) {
			t.Errorf("cancel at %s: %v", at, opErr)
		}
		o.onCall = nil
		cancel()
	}
}

func TestWrongLengthGetPropertiesResultIsAnError(t *testing.T) {
	o := profile()
	o.shortProps = true
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 0 || c.Stats().Discover.Skipped["unreadable"] == 0 {
		t.Fatalf("a folder whose properties come back short is unreadable, not misread: %+v", folders)
	}
	if _, err := valuesOf([]any{1}, []string{"a", "b"}); err == nil {
		t.Fatal("valuesOf checks the length")
	}
	if _, err := valuesOf("not an array", []string{"a"}); err == nil {
		t.Fatal("valuesOf checks the type")
	}
}

func TestUseAfterClose(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close() // idempotent
	if _, err := c.Discover(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Discover after Close: %v", err)
	}
	if _, err := c.ReadDetails(t.Context(), refOf(mail.Message{ID: "x"})); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadDetails after Close: %v", err)
	}
}

// Run under -race: every operation is serialised onto the COM thread.
func TestConcurrentUseIsSerialised(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	in := folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { _, _ = c.Discover(t.Context()) })
		wg.Go(func() { _, _ = c.Sync(t.Context(), in, "", &memSink{}) })
		wg.Go(func() { _ = c.Stats(); _ = c.Stores() })
	}
	wg.Wait()
	if c.Stats().Discover.Runs != 5 {
		t.Fatalf("runs %d", c.Stats().Discover.Runs)
	}
}

// Rules review 1b §6.5: with time passing during calls, every phase and
// sub-step is measured.
func TestTimingsAreMeasured(t *testing.T) {
	o := profile()
	c, msgs := syncedInbox(t, o, nil)
	if _, err := c.ReadDetails(t.Context(), refOf(msgs[0])); err != nil {
		t.Fatal(err)
	}
	st := c.Stats()
	if st.Discover.Time <= 0 || st.Sync.SnapshotTime <= 0 || st.Sync.TableTime <= 0 || st.Details.Open <= 0 ||
		st.Details.Body <= 0 || st.Details.Props <= 0 || st.Details.Recipients <= 0 || st.Members["Item.Body"].Max <= 0 {
		t.Fatalf("unmeasured: %+v %+v", st.Sync, st.Details)
	}
}
