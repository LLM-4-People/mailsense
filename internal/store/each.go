package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// ownerWhere is the scope predicate (over scope_verdicts v) of the owner's
// own mail: the one definition of the owner population of every read.
const ownerWhere = "v.verdict = '" + string(scope.Owner) + "'"

// sentByOwnerWhere is the direction predicate (over scope_verdicts v) of a
// message the owner sent; notSentByOwnerWhere is its complement. The
// counterpart predicates that also require a To/Cc recipient, the
// owner-address joins and the folder-role Sent flag answer different
// questions and keep their own SQL.
const sentByOwnerWhere = "v.reason = '" + string(scope.ReasonSentByOwner) + "'"
const notSentByOwnerWhere = "v.reason <> '" + string(scope.ReasonSentByOwner) + "'"

// sentByOwnerFlag is sentByOwnerWhere as the 0/1 direction column of a row
// projection.
const sentByOwnerFlag = "CASE WHEN " + sentByOwnerWhere + " THEN 1 ELSE 0 END"

// ownerContentWhere is the owner-facing content boundary. Scope verdicts are
// per store: a picked shared/delegate store can mark its own mailbox owner's
// mail verdict=owner, so the store kind is the reliable boundary for a
// subject, body or participant. ThreadStates deliberately does not use this:
// a shared reply must still change the thread state, but never its content.
// The SQL list comes from the one Go predicate, mail.StoreKind.OwnerContent.
var ownerContentWhere = ownerWhere + " AND f.store_kind NOT IN (" + ownerContentStoreKinds() + ")"

// ownerContentStoreKinds renders the non-owner-content store kinds as a SQL
// literal list. mail.StoreKind.OwnerContent is the one definition; this only
// spells it for SQL.
func ownerContentStoreKinds() string {
	kinds := make([]string, 0, 3)
	for _, kind := range mail.StoreKinds() {
		if !kind.OwnerContent() {
			kinds = append(kinds, "'"+string(kind)+"'")
		}
	}
	return strings.Join(kinds, ", ")
}

// threadContentRoleWhere excludes the folders that are never part of a
// conversation's owner-facing content. Drafts, deleted mail and junk never
// win a subject, a count, a participant or a deadline; the role vocabulary
// is spelled from its mail owner.
const threadContentRoleWhere = "f.role NOT IN ('" + string(mail.RoleDrafts) + "', '" + string(mail.RoleDeleted) + "', '" + string(mail.RoleJunk) + "')"

// ownerFacingWhere is the one owner-facing content boundary of the digest,
// ranking, people, search, counterpart and deadline reads: visible folders,
// owner scope, a store kind that carries owner content, and no draft, deleted
// or junk folder. ownerContentWhere owns the shared/delegate/public boundary
// and threadContentRoleWhere the role boundary; a read must not combine them
// by hand, because one that forgets a part leaks content the owner must not
// see. A read that deliberately takes only part of the boundary (attachments,
// identity, All mail, ThreadStates) says so at its own site.
func ownerFacingWhere(vis string) string {
	return vis + ` AND ` + ownerContentWhere + ` AND ` + threadContentRoleWhere
}

// MessageRow is a stored message with its row id, store and scope verdict
// ("" when scope has not run).
type MessageRow struct {
	RowID     int64
	Revision  int64 // messages.revision: moves on every write of the message
	Message   mail.Message
	StoreKind mail.StoreKind
	Verdict   scope.Verdict
	Reason    scope.Reason // the rule behind Verdict ("" without one)
}

// MessageFilter selects the messages of EachMessage: visible mail only
// (visibleSQL), optionally limited to roles (rolesSQL) and to owner-scope
// mail.
type MessageFilter struct {
	Roles     []mail.FolderRole
	OwnerOnly bool
	// OwnerContent narrows to mail whose subject, body and participants the
	// owner may see: the owner verdict plus a store kind that carries owner
	// content. OwnerOnly is the classification population, which legitimately
	// includes owner-verdict mail in a picked shared or delegate store; a
	// single-message content read must use this boundary instead.
	OwnerContent bool
	RowIDs       []int64 // only these rows (nil = all)
	// Fields projects the mail.Message columns EachMessage loads; the zero
	// value reads every mapped column and the recipients/items subqueries.
	// The row identity EachMessage always reads (row id, revision,
	// source/store/folder identity, store kind, verdict and reason) is not
	// part of the projection.
	Fields MessageFields
	// WithBodyHTML also loads the display-only raw HTML body. List, triage
	// and search reads leave it off; only a single message render asks.
	WithBodyHTML bool
}

// EachMessage streams the matching messages (with the recipients,
// attachments and further folders the filter's projection reads) in row-id
// order without holding them all in memory; fn returning an error stops.
func (s *Store) EachMessage(ctx context.Context, f MessageFilter, fn func(*MessageRow) error) error {
	vis, err := s.visible(ctx)
	if err != nil {
		return err
	}
	where, args := f.sql(vis)
	fields := f.Fields.Select()
	if fields != "" {
		fields = ", " + fields
	}
	selectBody := ""
	if f.WithBodyHTML {
		selectBody = ", " + bodyHTMLList("m.")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.revision, m.source_id, m.store_id, m.msg_id, f.store_name, f.path, f.role, f.store_kind,
		COALESCE(v.verdict, ''), COALESCE(v.reason, '')`+fields+selectBody+` FROM messages m JOIN folders f ON f.id = m.folder_id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id WHERE `+where+` ORDER BY m.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	p := f.Fields.projection()
	for rows.Next() {
		var r MessageRow
		var role, kind, verdict, reason, recips, atts, folders string
		var htmlKnown, htmlTruncated int
		m := &r.Message
		dest := []any{&r.RowID, &r.Revision, &m.SourceID, &m.StoreID, &m.ID, &m.StoreName, &m.FolderPath, &role, &kind, &verdict, &reason}
		if p.recipients {
			dest = append(dest, &recips)
		}
		if p.items {
			dest = append(dest, &atts, &folders)
		}
		dest = append(dest, p.columns.dests(m)...)
		if f.WithBodyHTML {
			dest = append(dest, bodyHTMLDests(m, &htmlKnown, &htmlTruncated)...)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		m.FolderRole, r.StoreKind, r.Verdict, r.Reason = mail.FolderRole(role), mail.StoreKind(kind), scope.Verdict(verdict), scope.Reason(reason)
		if f.WithBodyHTML {
			setBodyHTMLFlags(m, htmlKnown, htmlTruncated)
		}
		if p.recipients {
			if m.Recipients, err = parseRecipients(recips); err != nil {
				return err
			}
		}
		if p.items {
			if err := parseItems(atts, folders, m); err != nil {
				return err
			}
		}
		if err := fn(&r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (f MessageFilter) sql(visible string) (string, []any) {
	where := []string{visible}
	var args []any
	if len(f.Roles) > 0 {
		cond, a := rolesSQL(f.Roles)
		where, args = append(where, cond), append(args, a...)
	}
	if f.OwnerOnly {
		where = append(where, ownerWhere)
	}
	if f.OwnerContent {
		where = append(where, ownerContentWhere)
	}
	if f.RowIDs != nil {
		where = append(where, "m.id IN "+idSetSQL)
		args = append(args, idSet(f.RowIDs))
	}
	return strings.Join(where, " AND "), args
}

// recipientsSQL selects the recipients of message m as one JSON array of
// [kind, name, address] in position order: each row streams with its own
// recipients, nothing is preloaded for the whole mailbox.
const recipientsSQL = `COALESCE((SELECT json_group_array(json_array(r.kind, r.name, r.address)) FROM
	(SELECT kind, name, address FROM recipients WHERE message_id = m.id ORDER BY position) r), '[]')`

// parseRecipients decodes a recipientsSQL value.
func parseRecipients(raw string) ([]mail.Recipient, error) {
	var triples [][3]string
	if err := json.Unmarshal([]byte(raw), &triples); err != nil {
		return nil, fmt.Errorf("store: recipients: %w", err)
	}
	var out []mail.Recipient
	for _, t := range triples {
		out = append(out, mail.Recipient{Kind: mail.RecipientKind(t[0]), Name: t[1], Address: t[2]})
	}
	return out, nil
}

// MessageByRow reads one message by row id.
func (s *Store) MessageByRow(ctx context.Context, rowID int64) (mail.Message, error) {
	var src, store, id string
	err := s.db.QueryRowContext(ctx, "SELECT source_id, store_id, msg_id FROM messages WHERE id = ?", rowID).Scan(&src, &store, &id)
	if err == sql.ErrNoRows {
		return mail.Message{}, ErrNotFound
	}
	if err != nil {
		return mail.Message{}, err
	}
	return s.GetMessage(ctx, src, store, id)
}

// MessageRef is a message's row id and revision: the two facts a reader
// needs to order a population, mark a stored answer stale, and avoid
// decoding the message.
type MessageRef struct {
	RowID    int64
	Revision int64
}

// MessageRefs lists the rows EachMessage would stream for the filter, in
// row-id order, with their revisions but without reading or decoding the
// messages.
func (s *Store) MessageRefs(ctx context.Context, f MessageFilter) ([]MessageRef, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	where, args := f.sql(vis)
	return query(ctx, s.db, `SELECT m.id, m.revision FROM messages m JOIN folders f ON f.id = m.folder_id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id WHERE `+where+` ORDER BY m.id`, args,
		func(r *sql.Rows, ref *MessageRef) error { return r.Scan(&ref.RowID, &ref.Revision) })
}

// MessageIDs lists the row ids of the messages EachMessage would stream
// for the filter, in row-id order, without reading them.
func (s *Store) MessageIDs(ctx context.Context, f MessageFilter) ([]int64, error) {
	refs, err := s.MessageRefs(ctx, f)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(refs))
	for i, ref := range refs {
		ids[i] = ref.RowID
	}
	return ids, nil
}
