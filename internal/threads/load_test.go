package threads

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func loadTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	return st
}

func loadTestSettings(t *testing.T) Settings {
	t.Helper()
	return Settings{Normalizer: norm(t), Domains: mail.NewDomains(nil), Roles: CausalRoles(), ExcerptChars: 80}
}

// seedLoadMessages writes the messages, marks their rows owner-scope (sent
// items with the sent-by-owner reason) and returns message id → row id.
func seedLoadMessages(t *testing.T, st *store.Store, msgs []mail.Message) map[string]int64 {
	t.Helper()
	ctx := t.Context()
	if err := st.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	want := map[string]mail.Message{}
	for _, m := range msgs {
		want[m.ID] = m
	}
	rows := map[string]int64{}
	var verdicts []store.VerdictRow
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		m, ok := want[r.Message.ID]
		if !ok {
			return nil
		}
		reason := scope.ReasonAddressedFlag
		if m.FolderRole == mail.RoleSent {
			reason = scope.ReasonSentByOwner
		}
		rows[m.ID] = r.RowID
		verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Revision: r.Revision, Verdict: scope.Owner, Reason: reason})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(msgs) {
		t.Fatalf("seeded %d of %d messages", len(rows), len(msgs))
	}
	if err := st.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}
	return rows
}

// fillMessageSentinels sets every mail.Message field to a non-zero value of
// its type. The caller overrides the constrained fields (keys, folder role,
// timestamps) and the ones a case reads through a fallback.
func fillMessageSentinels(m *mail.Message) {
	v := reflect.ValueOf(m).Elem()
	t := v.Type()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Type() {
		case reflect.TypeFor[string]():
			f.SetString("sentinel-" + t.Field(i).Name)
		case reflect.TypeFor[bool]():
			f.SetBool(true)
		case reflect.TypeFor[mail.OptInt]():
			f.Set(reflect.ValueOf(mail.Int(int64(i + 1))))
		case reflect.TypeFor[[]mail.Recipient]():
			f.Set(reflect.ValueOf([]mail.Recipient{
				{Kind: mail.To, Name: "Sentinel To", Address: "to@example.org"},
				{Kind: mail.Cc, Name: "Sentinel Cc", Address: "cc@example.org"},
				{Kind: mail.Bcc, Name: "Sentinel Bcc", Address: "bcc@example.org"},
			}))
		case reflect.TypeFor[[]mail.Attachment]():
			f.Set(reflect.ValueOf([]mail.Attachment{{
				Name: "sentinel.bin", ContentType: "application/octet-stream", Size: 9, Inline: true,
				ContentID: "sentinel-cid", ContentLocation: "sentinel-loc", SHA256: "sentinel-sha",
			}}))
		case reflect.TypeFor[[]mail.FolderRef]():
			f.Set(reflect.ValueOf([]mail.FolderRef{{Path: "Sentinel/2026", Role: mail.RoleArchive}}))
		case reflect.TypeFor[[]mail.Alteration]():
			f.Set(reflect.ValueOf([]mail.Alteration{{Code: mail.AltHeaderBytes, Detail: "sentinel"}}))
		}
	}
}

func sentinelMessage(id string) mail.Message {
	m := mailtest.Message("src", id)
	fillMessageSentinels(&m)
	m.SourceID, m.StoreID, m.ID = "src", "store-1", id
	m.FolderPath, m.FolderRole = "Inbox", mail.RoleInbox
	for i, f := range m.TimestampFields() {
		*f.Value = time.Date(2026, 1, 2, 3, 4, 5+i, 0, time.UTC).Format(mail.TimeLayout)
	}
	m.ReceivedAt, m.SentOn = "2026-10-12T15:48:34Z", "2026-10-12T15:48:25Z"
	m.Body = "Sentinel body with several words.\nA second line for the excerpt and count."
	m.Subject = "Sentinel subject " + id
	m.SenderAddress = "Sentinel.Sender@Example.org"
	m.ConversationID = "sentinel-conv-" + id
	m.ConversationIndex = "01D9SENTINEL" + id
	m.MessageClass = "IPM.Note"
	m.Headers = "X-Sentinel: " + id + "\r\n"
	m.CalendarMethod = "sentinel"
	m.InternetMessageID = "<sentinel-" + id + "@example.org>"
	m.InReplyTo = "<sentinel-parent-" + id + "@example.org>"
	m.References = "<sentinel-root-" + id + "@example.org>"
	return m
}

// projectionSentinelMessages covers every field FromMail and its helpers
// read: each direct field, each KindOf branch (class, transport headers,
// calendar method), both ThreadIDs branches (structured ids, header
// fallback) and both Instant branches (inbox received_at, sent sent_on).
func projectionSentinelMessages() []mail.Message {
	direct := sentinelMessage("p-direct")

	class := sentinelMessage("p-class")
	class.MessageClass = "IPM.Schedule.Meeting.Request"
	class.Headers = "X-Sentinel: class\r\n"
	class.CalendarMethod = "sentinel"

	header := sentinelMessage("p-header")
	header.MessageClass = "IPM.Note"
	header.Headers = "Auto-Submitted: auto-replied\r\n"
	header.CalendarMethod = "sentinel"

	calendar := sentinelMessage("p-calendar")
	calendar.MessageClass = "IPM.Note"
	calendar.Headers = "X-Sentinel: calendar\r\n"
	calendar.CalendarMethod = "REQUEST"

	headerThread := sentinelMessage("p-thread-headers")
	headerThread.MessageClass = "IPM.Note"
	headerThread.Headers = "In-Reply-To: <header-parent@example.org>\r\nReferences: <header-root@example.org> <header-parent@example.org>\r\n"
	headerThread.InternetMessageID, headerThread.InReplyTo, headerThread.References = "", "", ""

	fieldThread := sentinelMessage("p-thread-fields")
	fieldThread.MessageClass = "IPM.Note"
	fieldThread.Headers = "X-Sentinel: fields\r\n"
	fieldThread.InternetMessageID = "<self@example.org>"
	fieldThread.InReplyTo = "<parent@example.org>"
	fieldThread.References = "<root@example.org>"

	sent := sentinelMessage("p-sent")
	sent.FolderPath, sent.FolderRole = "Sent Items", mail.RoleSent
	sent.ReceivedAt = "2026-10-12T09:00:00Z"
	sent.SentOn = "2026-10-12T14:30:00Z"

	return []mail.Message{direct, class, header, calendar, headerThread, fieldThread, sent}
}

// TestThreadsProjectionIncludesEveryReadField is the contract between
// FromMail and threadFields: over messages whose every field is set, the
// projected read must produce exactly the threads.Message of the full read.
// A later field read added to FromMail without adding it to the projection
// makes the projected value zero and fails here.
func TestThreadsProjectionIncludesEveryReadField(t *testing.T) {
	st := loadTestStore(t)
	msgs := projectionSentinelMessages()
	rows := seedLoadMessages(t, st, msgs)
	ids := slices.Sorted(maps.Values(rows))
	domains := mail.NewDomains(nil)
	read := func(fields store.MessageFields) map[int64]Message {
		t.Helper()
		out := map[int64]Message{}
		err := st.EachMessage(t.Context(), store.MessageFilter{RowIDs: ids, Fields: fields}, func(r *store.MessageRow) error {
			out[r.RowID] = FromMail(r.RowID, &r.Message, r.Message.FolderRole == mail.RoleSent, 80, domains)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	full := read(store.MessageFields{})
	projected := read(threadFields)
	if !reflect.DeepEqual(full, projected) {
		t.Fatalf("the projected read differs from the full read\nfull:      %+v\nprojected: %+v", full, projected)
	}

	byID := func(id string) Message {
		t.Helper()
		m, ok := projected[rows[id]]
		if !ok {
			t.Fatalf("message %s was not read", id)
		}
		return m
	}
	if got := byID("p-class").Kind; got != mail.KindMeetingRequest {
		t.Fatalf("message-class kind %q", got)
	}
	if got := byID("p-header").Kind; got != mail.KindAutoReply {
		t.Fatalf("header kind %q", got)
	}
	if got := byID("p-calendar").Kind; got != mail.KindMeetingRequest {
		t.Fatalf("calendar kind %q", got)
	}
	if m := byID("p-thread-headers"); m.SelfID != "" || !slices.Equal(m.Parents, []string{"<header-parent@example.org>", "<header-root@example.org>"}) {
		t.Fatalf("header thread ids: self %q parents %v", m.SelfID, m.Parents)
	}
	if m := byID("p-thread-fields"); m.SelfID != "<self@example.org>" ||
		!slices.Equal(m.Parents, []string{"<parent@example.org>", "<root@example.org>"}) {
		t.Fatalf("structured thread ids: self %q parents %v", m.SelfID, m.Parents)
	}
	sentAt, _ := mail.Parse("2026-10-12T14:30:00Z")
	if m := byID("p-sent"); !m.At.Equal(sentAt) {
		t.Fatalf("a sent message's instant is sent_on: %v", m.At)
	}
	if m := byID("p-direct"); m.Excerpt == "" || m.Words == 0 || len(m.Recipients) != 2 {
		t.Fatalf("direct fields: %+v", m)
	}
}

// TestThreadsLoadMatchesFullRead runs the real fixture path twice: the
// production projection and the full-column read must analyse identically.
func TestThreadsLoadMatchesFullRead(t *testing.T) {
	st := loadTestStore(t)
	msgs := []mail.Message{
		mailMessage("budget-a", "alice@example.org", "Budget 2026", "C-budget", "2026-10-12T09:00:00Z", mail.RoleInbox, "<budget-a@example.org>", "", ""),
		mailMessage("budget-b", mailtest.Owner, "Re: Budget 2026", "C-budget", "2026-10-12T09:30:00Z", mail.RoleSent, "<budget-b@example.org>", "<budget-a@example.org>", "<budget-a@example.org>"),
		mailMessage("lunch-c", "bob@example.org", "Lunch", "", "2026-10-12T08:00:00Z", mail.RoleInbox, "", "", ""),
		mailMessage("budget-d", "alice@example.org", "Re: Budget 2026", "C-budget", "2026-10-12T10:00:00Z", mail.RoleInbox, "<budget-d@example.org>", "", ""),
		mailMessage("header-reply", "carol@example.org", "Re: Budget 2026", "", "2026-10-12T11:00:00Z", mail.RoleInbox, "", "", ""),
		mailMessage("meeting-f", "dave@example.org", "Accepted: Budget 2026", "C-budget", "2026-10-12T12:00:00Z", mail.RoleInbox, "<meeting-f@example.org>", "", ""),
	}
	msgs[0].Recipients = append(msgs[0].Recipients, mail.Recipient{Kind: mail.Bcc, Name: "Hidden", Address: "hidden@example.org"})
	msgs[4].Headers = "In-Reply-To: <budget-a@example.org>\r\n"
	msgs[5].MessageClass = "IPM.Schedule.Meeting.Resp"
	rows := seedLoadMessages(t, st, msgs)
	set := loadTestSettings(t)

	full, err := load(t.Context(), st, set, store.MessageFields{})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := Load(t.Context(), st, set)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full, projected) {
		t.Fatalf("the projected load differs from the full load\nfull:      %+v\nprojected: %+v", full, projected)
	}
	if len(full.Messages) != len(msgs) || len(full.Threads) != 2 {
		t.Fatalf("analysis: %d messages, %d threads", len(full.Messages), len(full.Threads))
	}
	if len(full.MyReplies) == 0 || len(full.TheirReplies) == 0 {
		t.Fatalf("reply matching: mine %v theirs %v", full.MyReplies, full.TheirReplies)
	}
	for _, id := range []string{"budget-a", "budget-b", "header-reply", "meeting-f"} {
		m, ok := full.Message(rows[id])
		if !ok || m.Excerpt == "" || m.Words == 0 {
			t.Fatalf("message %s: %+v", id, m)
		}
	}
	if m, _ := full.Message(rows["header-reply"]); !slices.Equal(m.Parents, []string{"<budget-a@example.org>"}) {
		t.Fatalf("header references: %v", m.Parents)
	}
	if m, _ := full.Message(rows["meeting-f"]); m.Kind != mail.KindMeetingResponse {
		t.Fatalf("meeting kind %q", m.Kind)
	}
}

func mailMessage(id, from, subject, conversation, at string, role mail.FolderRole, self, inReplyTo, references string) mail.Message {
	m := mailtest.Message("src", id)
	m.SenderAddress = from
	m.Subject = subject
	m.ConversationID = conversation
	m.FolderPath, m.FolderRole = "Inbox", role
	if role == mail.RoleSent {
		m.FolderPath = "Sent Items"
	}
	m.ReceivedAt = at
	m.SentOn = at
	m.InternetMessageID, m.InReplyTo, m.References = self, inReplyTo, references
	m.Body = "Bonjour,\nvoici le budget et le planning.\nMerci de confirmer avant lundi."
	return m
}

// TestThreadsLoadSelectsOnlyItsFields asserts the SQL surface of the
// threads projection: only the mapped fields it reads and the recipients
// subquery, never body_html, the attachment/folder item subquery or an
// unrelated message column.
func TestThreadsLoadSelectsOnlyItsFields(t *testing.T) {
	sel := threadFields.Select()
	got := messageColumnRefs(sel)
	want := []string{
		"m.body", "m.calendar_method", "m.conversation_id", "m.conversation_index",
		"m.headers", "m.id", "m.in_reply_to", "m.internet_message_id", "m.message_class",
		"m.message_references", "m.received_at", "m.sender_address", "m.sent_on", "m.subject",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("threads selects\n%v\nwant\n%v", got, want)
	}
	for _, forbidden := range []string{
		"body_html", "FROM attachments", "FROM message_folders", "m.attachments", "m.folders",
		"m.entry_id", "m.sender_name", "m.is_read", "m.headers_fts",
	} {
		if strings.Contains(sel, forbidden) {
			t.Fatalf("threads read selects %q:\n%s", forbidden, sel)
		}
	}
	if !strings.Contains(sel, "FROM recipients") {
		t.Fatalf("threads reads recipients (FromMail needs them):\n%s", sel)
	}
}

// messageColumnRefs lists the m.<column> references of a SELECT list,
// sorted and deduplicated. The recipients subquery contributes m.id.
func messageColumnRefs(sel string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; i < len(sel); i++ {
		if sel[i] != 'm' || i+1 >= len(sel) || sel[i+1] != '.' {
			continue
		}
		if i > 0 && (isIdent(sel[i-1])) {
			continue
		}
		j := i + 2
		for j < len(sel) && (sel[j] == '_' || sel[j] >= 'a' && sel[j] <= 'z') {
			j++
		}
		if j == i+2 {
			continue
		}
		ref := sel[i:j]
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
		i = j - 1
	}
	slices.Sort(out)
	return out
}

func isIdent(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
