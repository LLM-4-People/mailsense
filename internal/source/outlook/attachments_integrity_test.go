package outlook

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// inlinePNG is a minimal inline image: eight bytes are enough for the
// connector to keep it (the inline budget defaults far above).
var inlinePNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// attachmentProfile gives the item whose details are re-read three
// attachments: a file, an inline image (its bytes become a stored blob) and
// a second file. The first and third are metadata only.
func attachmentProfile(t *testing.T) (*fakeOutlook, *fakeItem) {
	t.Helper()
	o := profile()
	it := o.stores[1].defaults[6].items[4]
	it.props[propHasAttach] = true
	it.atts = []fakeAttachment{
		{name: "facture.pdf", size: 120, props: map[string]any{propAttachMimeTag: "application/pdf"}},
		{name: "logo.png", size: int64(len(inlinePNG)), props: map[string]any{
			propAttachContentID: "logo@example.test",
			propAttachMimeTag:   "image/png",
			propAttachSize:      int64(len(inlinePNG)),
			propAttachDataBin:   inlinePNG,
		}},
		{name: "notes.txt", size: 12, props: map[string]any{propAttachMimeTag: "text/plain"}},
	}
	return o, it
}

// storedAttachmentRow finds the message's row id (AttachmentData reads by
// row and position).
func storedAttachmentRow(t *testing.T, st *store.Store, id string) int64 {
	t.Helper()
	var rowID int64
	err := st.EachMessage(t.Context(), store.MessageFilter{}, func(row *store.MessageRow) error {
		if row.Message.StoreID == "S-PRIMARY" && row.Message.ID == id {
			rowID = row.RowID
		}
		return nil
	})
	if err != nil || rowID == 0 {
		t.Fatalf("stored row of %s: %d %v", id, rowID, err)
	}
	return rowID
}

// testDetailAttachmentFailure completes a message with three attachments,
// makes it partial again, then makes its second Attachments.Item read fail
// while its detail pass runs. The authoritative detail write must not run on
// a prefix: the stored rows, the inline blob and partial must be unchanged.
func testDetailAttachmentFailure(t *testing.T, inject func(*fakeAttachment)) {
	t.Helper()
	o, it := attachmentProfile(t)
	c, s, st := syncRig(t, o)
	run(t, s, c)

	m, err := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", it.id())
	if err != nil || m.Partial || len(m.Attachments) != 3 {
		t.Fatalf("first read: partial=%v attachments=%d err=%v", m.Partial, len(m.Attachments), err)
	}
	rowID := storedAttachmentRow(t, st, it.id())
	sha := m.Attachments[1].SHA256
	if sha == "" {
		t.Fatalf("inline attachment was not stored: %+v", m.Attachments[1])
	}
	if asset, err := st.AttachmentData(t.Context(), rowID, 1); err != nil || !bytes.Equal(asset.Bytes, inlinePNG) {
		t.Fatalf("inline blob before the failure: %+v %v", asset, err)
	}

	// A change makes the message partial again, so the next run re-reads
	// its details; the second attachment now fails for this run only.
	it.body = "corps modifié"
	it.props[propLastModified] = DateOf(t0.Add(72 * time.Hour))
	inject(&it.atts[1])
	rep := run(t, s, c)

	m, err = st.GetMessage(t.Context(), "outlook", "S-PRIMARY", it.id())
	if err != nil {
		t.Fatal(err)
	}
	var lost []string
	if !m.Partial {
		lost = append(lost, "partial=false")
	}
	if len(m.Attachments) != 3 {
		lost = append(lost, fmt.Sprintf("rows=%d want 3 (%v)", len(m.Attachments), attachmentNames(m.Attachments)))
	} else {
		if got := m.Attachments[1].SHA256; got != sha {
			lost = append(lost, fmt.Sprintf("inline sha %q -> %q", sha, got))
		}
		for i, name := range []string{"facture.pdf", "logo.png", "notes.txt"} {
			if m.Attachments[i].Name != name {
				lost = append(lost, fmt.Sprintf("row %d is %q, want %q", i, m.Attachments[i].Name, name))
			}
		}
	}
	asset, err := st.AttachmentData(t.Context(), rowID, 1)
	if err != nil || !bytes.Equal(asset.Bytes, inlinePNG) || asset.SHA256 != sha {
		lost = append(lost, fmt.Sprintf("inline blob gone or changed (%v)", err))
	}
	if len(lost) > 0 {
		t.Fatalf("an incomplete detail read destroyed stored data: %s", strings.Join(lost, "; "))
	}
	if rep.Details.Failed != 1 || rep.Details.Completed != 0 {
		t.Fatalf("an incomplete attachment list must fail, not complete: %+v", rep.Details)
	}
	if n, err := st.CountPending(t.Context(), "outlook", time.Time{}); err != nil || n != 1 {
		t.Fatalf("the message must stay partial for the next run: pending=%d %v", n, err)
	}
}

// attachmentNames lists the stored attachment names for a failure message.
func attachmentNames(atts []mail.Attachment) []string {
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return names
}

// A transient COM failure on one attachment used to break the loop and
// return the attachments before it; the store's authoritative replace then
// deleted the missing rows and pruned their blobs.
func TestDetailItemFailureKeepsStoredAttachments(t *testing.T) {
	testDetailAttachmentFailure(t, func(a *fakeAttachment) { a.failRead = 0x80004005 })
}

// Outlook answering Nothing for one attachment is a failed read too, not an
// absent attachment: the same stored data must survive.
func TestDetailNilItemIsAFailure(t *testing.T) {
	testDetailAttachmentFailure(t, func(a *fakeAttachment) { a.nothing = true })
}
