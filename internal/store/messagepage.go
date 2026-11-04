package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// The mail-list page contract. The handler reuses these bounds so one
// setting owns the numbers.
const (
	MessageListDefaultLimit = 100
	MessageListMaxLimit     = 200
	// MessageListAll is the role value that means every primary role. It
	// is not a mail.FolderRole: mail.ParseRole stays the role validator.
	MessageListAll = "all"
)

var (
	// ErrInvalidRole is returned for a role that is neither a mail role nor
	// the all value.
	ErrInvalidRole = errors.New("store: invalid message-list role")
	// ErrInvalidLimit is returned for a limit outside the page bounds.
	ErrInvalidLimit = errors.New("store: invalid message-list limit")
	// ErrInvalidStore is returned for a store key that is not a StoreKey.
	ErrInvalidStore = errors.New("store: invalid message-list store")
	// ErrInvalidSender is returned for an empty, over-long, control-bearing
	// or domain-less sender filter. The mail package owns the validation;
	// this is the page's typed wrap of it.
	ErrInvalidSender = errors.New("store: invalid message-list sender")
	// ErrInvalidCursor is returned for a malformed page cursor or one
	// issued for another role, store or sender. A cursor from an inbox page
	// can never page an all-mail page.
	ErrInvalidCursor = errors.New("store: invalid message-list cursor")
)

// Cursor is the opaque keyset position of a mail-list page: the last row's
// materialized instant and row id, plus the normalized role, store, sender
// and read-state filter the page was issued for. The set makes equal
// instants total and stable and binds a page to its exact filters.
type Cursor struct {
	Instant string          `json:"instant"`
	ID      int64           `json:"id"`
	Role    mail.FolderRole `json:"role"`
	Store   string          `json:"store"`
	Sender  string          `json:"sender,omitempty"`
	Unread  bool            `json:"unread,omitempty"`
}

// IsZero reports whether c is the "no cursor" value.
func (c Cursor) IsZero() bool {
	return c.ID == 0 && c.Instant == "" && c.Role == "" && c.Store == "" && c.Sender == "" && !c.Unread
}

// String encodes c as the opaque before value of the next page.
func (c Cursor) String() string {
	raw, _ := json.Marshal(c) // strings and one integer: never fails
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ParseCursor decodes an opaque before value. It validates syntax only;
// MessageList rejects a cursor issued for another role, store or sender.
func ParseCursor(value string) (Cursor, error) {
	invalid := func(detail string) error { return fmt.Errorf("%w: %s", ErrInvalidCursor, detail) }
	if value == "" {
		return Cursor{}, invalid("empty")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, invalid("not base64url")
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, invalid("not a cursor")
	}
	if err := c.validate(); err != nil {
		return Cursor{}, err
	}
	return c, nil
}

func (c Cursor) validate() error {
	if c.ID <= 0 {
		return fmt.Errorf("%w: no row id", ErrInvalidCursor)
	}
	if c.Instant != "" {
		if _, ok := mail.Parse(c.Instant); !ok {
			return fmt.Errorf("%w: bad instant", ErrInvalidCursor)
		}
	}
	if c.Role != "" {
		if _, err := mail.ParseRole(string(c.Role)); err != nil {
			return fmt.Errorf("%w: bad role", ErrInvalidCursor)
		}
	}
	if c.Store != "" && !source.ValidStoreKey(c.Store) {
		return fmt.Errorf("%w: bad store", ErrInvalidCursor)
	}
	if c.Sender != "" {
		sender, err := mail.NormalizeSenderQuery(c.Sender)
		if err != nil || sender != c.Sender {
			return fmt.Errorf("%w: bad sender", ErrInvalidCursor)
		}
	}
	return nil
}

// MessageListQuery selects one page of the all-mail read.
//
// Role, Store, Sender and Unread are the four page filters. An empty Role
// (or all) means every primary folder role; an empty Store means every
// picked store; an empty Sender means every sender. The sender is one exact
// address or one domain, never free text or a display name
// (mail.NormalizeSenderQuery owns the rule). Unread=true keeps only unread
// mail (m.is_read = 0); false is the same as absent, every read state. Role
// matches the message's primary folder only: the further folders of a
// message (Gmail labels) are not counted in role pages for v1, so a message
// appears once, under the folder that holds it. All mail does include
// drafts, deleted and junk when they are not filtered out.
//
// Counts asks for the per-role totals under the same visibility, store,
// sender and read-state filter but ignoring Role and Before; the role tab
// counts never depend on the page being viewed.
type MessageListQuery struct {
	Role   mail.FolderRole
	Store  string
	Sender string
	Unread bool
	Before Cursor
	Limit  int
	Counts bool
}

// normalize applies the one defaults-and-bounds rule of a list request.
func (q *MessageListQuery) normalize() error {
	if q.Role == MessageListAll {
		q.Role = ""
	}
	if q.Role != "" {
		if _, err := mail.ParseRole(string(q.Role)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidRole, err)
		}
	}
	if q.Store != "" && !source.ValidStoreKey(q.Store) {
		return fmt.Errorf("%w: %q", ErrInvalidStore, q.Store)
	}
	if q.Sender != "" {
		sender, err := mail.NormalizeSenderQuery(q.Sender)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSender, err)
		}
		q.Sender = sender
	}
	switch {
	case q.Limit == 0:
		q.Limit = MessageListDefaultLimit
	case q.Limit < 1 || q.Limit > MessageListMaxLimit:
		return fmt.Errorf("%w: %d", ErrInvalidLimit, q.Limit)
	}
	if !q.Before.IsZero() {
		if err := q.Before.validate(); err != nil {
			return err
		}
		if q.Before.Role != q.Role || q.Before.Store != q.Store {
			return fmt.Errorf("%w: cursor belongs to another role or store", ErrInvalidCursor)
		}
		if q.Before.Sender != q.Sender {
			return fmt.Errorf("%w: cursor belongs to another sender", ErrInvalidCursor)
		}
		if q.Before.Unread != q.Unread {
			return fmt.Errorf("%w: cursor belongs to another read state", ErrInvalidCursor)
		}
	}
	return nil
}

// MessageListRow is one mail-list row: metadata and a bounded plain-text
// snippet. It never carries recipients, body HTML or transport headers;
// the single-message endpoint owns the full recipient list.
type MessageListRow struct {
	RowID                   int64  `json:"row_id"`
	Subject                 string `json:"subject"`
	SenderName              string `json:"sender_name"`
	SenderAddress           string `json:"sender_address"`
	SentRepresentingAddress string `json:"sent_representing_address"`
	// Instant is mail.Instant (sent items when they left): the one order
	// and cursor key. ReceivedAt and SentOn stay the source's two stamps.
	Instant        string          `json:"instant"`
	ReceivedAt     string          `json:"received_at"`
	SentOn         string          `json:"sent_on"`
	Role           mail.FolderRole `json:"role"`
	FolderPath     string          `json:"folder_path"`
	StoreKey       string          `json:"store_key"`
	StoreName      string          `json:"store_name"`
	IsRead         bool            `json:"is_read"`
	HasAttachments bool            `json:"has_attachments"`
	Snippet        string          `json:"snippet"`
}

// MessageListPage is one page plus the counts under the same population.
// Messages is never nil. Counts is nil unless requested; when present it
// carries every mail role, zero-filled.
type MessageListPage struct {
	Messages   []MessageListRow
	Total      int
	Counts     map[mail.FolderRole]int
	NextBefore string
	HasMore    bool
}

// messageListFromSQL is the one join of the list's aggregate reads (its plain
// total and its role counts). They aggregate, they never page, so they read
// the folder's message range through the covering folder index instead of
// walking scope_verdicts and probing messages by row id. ownerContentWhere
// owns the owner and shared/delegate/public boundary;
// threadContentRoleWhere is deliberately absent: All mail must show drafts,
// deleted and junk, and a selected role must show its folder.
const messageListFromSQL = ` FROM folders f
	CROSS JOIN messages m INDEXED BY messages_folder_run ON m.folder_id = f.id
	JOIN scope_verdicts v ON v.message_id = m.id`

// messageListRoleWhere is the one role predicate of a role page, over the
// folders f: the page total and the folder resolver both use it, and the
// folder-pinned page replaces it with the folder identity the resolver
// returned.
const messageListRoleWhere = "f.role = ?"

// The page-order indexes. The sender indexes (migration 0031) end in the
// same keyset order, so a sender page is a range read; every other page
// follows the instant index. A role page pins one folder per read, so
// messages_folder_instant keeps its order and the page is a range read that
// stops after limit+1 rows. The planner otherwise drives from
// scope_verdicts and sorts the whole population, so INDEXED BY pins the
// order.
const (
	messageListOrderIndex        = "messages_instant"
	messageListFolderIndex       = "messages_folder_instant"
	messageListSenderNormIndex   = "messages_sender_norm"
	messageListSenderDomainIndex = "messages_sender_domain"
)

// messageListPageFrom is the page's join. Total and role counts keep the
// aggregate join: they aggregate, they never page.
func messageListPageFrom(index string) string {
	return ` FROM messages m INDEXED BY ` + index + `
	JOIN folders f ON f.id = m.folder_id
	JOIN scope_verdicts v ON v.message_id = m.id`
}

// messageListQuery is the one page statement: the list row, the chosen page
// index, the caller's predicates and the keyset order. The caller owns the
// cursor predicate and appends its limit.
func messageListQuery(index, where string) string {
	return `SELECT m.id, m.subject, m.sender_name, m.sender_address, m.sent_representing_address,
			m.instant, m.received_at, m.sent_on, f.role, f.path, f.store_key, f.store_name,
			m.is_read, m.has_attachments, substr(m.body, 1, ` + strconv.Itoa(bodyPrefix) + `)` +
		messageListPageFrom(index) + ` WHERE ` + where + ` ORDER BY m.instant DESC, m.id DESC LIMIT ?`
}

// messageListCursorWhere appends the keyset position to a page predicate. The
// cursor rule lives here once for the single-index and per-folder pages.
func messageListCursorWhere(where string, args []any, before Cursor) (string, []any) {
	if before.IsZero() {
		return where, args
	}
	where += " AND (m.instant < ? OR (m.instant = ? AND m.id < ?))"
	return where, append(args, before.Instant, before.Instant, before.ID)
}

// MessageList reads one keyset page of owner-facing mail, newest first.
// Ordering is the stored instant then row id, both descending, so equal
// instants have one stable order and a page boundary never depends on an
// offset.
func (s *Store) MessageList(ctx context.Context, q MessageListQuery) (MessageListPage, error) {
	out := MessageListPage{Messages: []MessageListRow{}}
	if err := q.normalize(); err != nil {
		return out, err
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return out, err
	}
	// Counts ignore the role filter (every tab shows every role) but keep
	// the store and sender filters; the page and its total keep all three.
	baseWhere := []string{vis, ownerContentWhere}
	var baseArgs []any
	if q.Store != "" {
		baseWhere, baseArgs = append(baseWhere, "f.store_key = ?"), append(baseArgs, q.Store)
	}
	sender := senderFilterOf(q.Sender)
	if sender.cond != "" {
		baseWhere, baseArgs = append(baseWhere, sender.cond), append(baseArgs, sender.args...)
	}
	if q.Unread {
		baseWhere = append(baseWhere, "m.is_read = 0")
	}
	// The role predicate belongs to the page and its plain total only: the
	// counts always describe every role.
	where, args := append([]string(nil), baseWhere...), append([]any(nil), baseArgs...)
	if q.Role != "" {
		where, args = append(where, messageListRoleWhere), append(args, string(q.Role))
	}
	base := strings.Join(where, " AND ")
	if q.Counts {
		counts, err := s.messageListCountsCached(ctx, strings.Join(baseWhere, " AND "), baseArgs, q.Store, q.Sender, q.Unread)
		if err != nil {
			return out, err
		}
		out.Counts = counts
		// The counts group the same population by role and ignore role and
		// cursor, so the page total is their sum (or the one role's count).
		if q.Role != "" {
			out.Total = counts[q.Role]
		} else {
			for _, n := range counts {
				out.Total += n
			}
		}
	} else if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*)"+messageListFromSQL+" WHERE "+base, args...).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("store: message list total: %w", err)
	}

	// A role page pins one folder per read: messages_folder_instant ends in
	// the keyset order, so each folder is a range read that stops after
	// limit+1 rows, and the page is the newest limit+1 of their union. The
	// alternative — one scan ordered by the global instant index — walks the
	// whole mailbox for a role that holds few or no owner rows. A sender
	// page keeps its global sender index: it already stops early, and the
	// role predicate can only remove rows from it.
	page := []MessageListRow{}
	if q.Role != "" && sender.cond == "" {
		folders, err := s.messageListRoleFolders(ctx, vis, q.Store, q.Role)
		if err != nil {
			return out, err
		}
		for _, folderID := range folders {
			folderWhere, folderArgs := messageListCursorWhere(
				strings.Join(baseWhere, " AND ")+" AND m.folder_id = ?",
				append(append([]any(nil), baseArgs...), folderID),
				q.Before)
			folder, err := s.messageListPageRows(ctx, messageListQuery(messageListFolderIndex, folderWhere), append(folderArgs, q.Limit+1)...)
			if err != nil {
				return out, err
			}
			page = append(page, folder...)
		}
		sort.Slice(page, func(i, j int) bool {
			if page[i].Instant != page[j].Instant {
				return page[i].Instant > page[j].Instant
			}
			return page[i].RowID > page[j].RowID
		})
	} else {
		pageWhere, pageArgs := messageListCursorWhere(base, append([]any(nil), args...), q.Before)
		rows, err := s.messageListPageRows(ctx, messageListQuery(sender.index, pageWhere), append(pageArgs, q.Limit+1)...)
		if err != nil {
			return out, err
		}
		page = rows
	}
	out.Messages = page
	if len(out.Messages) > q.Limit {
		out.Messages = out.Messages[:q.Limit]
		out.HasMore = true
		last := out.Messages[len(out.Messages)-1]
		out.NextBefore = Cursor{Instant: last.Instant, ID: last.RowID, Role: q.Role, Store: q.Store, Sender: q.Sender, Unread: q.Unread}.String()
	}
	return out, nil
}

// messageListRoleFolders lists the visible folders of one role under the
// page's store filter, in id order. The page pins each one in turn.
func (s *Store) messageListRoleFolders(ctx context.Context, vis, store string, role mail.FolderRole) ([]int64, error) {
	query := `SELECT f.id FROM folders f WHERE ` + vis + ` AND ` + messageListRoleWhere
	args := []any{string(role)}
	if store != "" {
		query += ` AND f.store_key = ?`
		args = append(args, store)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY f.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: message list role folders: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: message list role folder row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: message list role folder rows: %w", err)
	}
	return ids, nil
}

// messageListPageRows reads one page statement into list rows. Each folder
// read closes before the next begins, so a store limited to one connection
// never waits on itself.
func (s *Store) messageListPageRows(ctx context.Context, query string, args ...any) ([]MessageListRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: message list: %w", err)
	}
	defer rows.Close()
	out := []MessageListRow{}
	for rows.Next() {
		var row MessageListRow
		var role string
		var isRead, hasAttachments int
		if err := rows.Scan(&row.RowID, &row.Subject, &row.SenderName, &row.SenderAddress, &row.SentRepresentingAddress,
			&row.Instant, &row.ReceivedAt, &row.SentOn, &role, &row.FolderPath, &row.StoreKey, &row.StoreName,
			&isRead, &hasAttachments, &row.Snippet); err != nil {
			return nil, fmt.Errorf("store: message list row: %w", err)
		}
		row.Role = mail.FolderRole(role)
		row.IsRead, row.HasAttachments = isRead != 0, hasAttachments != 0
		row.Snippet = threadSnippet(row.Snippet)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: message list rows: %w", err)
	}
	return out, nil
}

// messageListCounts groups the page population by primary role. It ignores
// Role and the cursor by design (the tabs always show every role), while
// keeping the same visibility, owner content, store, sender and read-state
// filters.
type messageCountsKey struct {
	generation uint64
	store      string
	sender     string
	unread     bool
}

type messageCountsEntry struct {
	counts map[mail.FolderRole]int
}

// messageListCountsCached memoizes the role counts for one mail generation,
// store filter, sender filter and read state. Counts are read on every
// All-mail open and page; recomputing them scans the whole visible population
// even though no write happened. A mail write bumps the generation, so the
// next read recomputes; a label write does not touch mail and must not.
func (s *Store) messageListCountsCached(ctx context.Context, base string, args []any, store, sender string, unread bool) (map[mail.FolderRole]int, error) {
	key := messageCountsKey{generation: s.MailGeneration(), store: store, sender: sender, unread: unread}
	s.countsMu.Lock()
	if entry, ok := s.counts[key]; ok {
		out := cloneCounts(entry.counts)
		s.countsMu.Unlock()
		return out, nil
	}
	s.countsMu.Unlock()
	counts, err := s.messageListCounts(ctx, base, args)
	if err != nil {
		return nil, err
	}
	s.countsMu.Lock()
	if s.counts == nil || len(s.counts) >= 8 {
		s.counts = make(map[messageCountsKey]messageCountsEntry, 8)
	}
	s.counts[key] = messageCountsEntry{counts: cloneCounts(counts)}
	s.countsMu.Unlock()
	return counts, nil
}

func cloneCounts(in map[mail.FolderRole]int) map[mail.FolderRole]int {
	out := make(map[mail.FolderRole]int, len(in))
	for role, n := range in {
		out[role] = n
	}
	return out
}

func (s *Store) messageListCounts(ctx context.Context, base string, args []any) (map[mail.FolderRole]int, error) {
	counts := make(map[mail.FolderRole]int, len(mail.Roles()))
	for _, role := range mail.Roles() {
		counts[role] = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.role, COUNT(*)`+messageListFromSQL+` WHERE `+base+` GROUP BY f.role`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: message list counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, fmt.Errorf("store: message list count row: %w", err)
		}
		counts[mail.FolderRole(role)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: message list count rows: %w", err)
	}
	return counts, nil
}
