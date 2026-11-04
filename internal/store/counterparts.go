package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
)

// counterpartMessage is one distinct owner-visible counterpart message. The
// identity components are kept with the database id so every consumer can
// use the same messageIdentity dedup key as the SQL folds. The conversation
// and instant belong to the message row, so every pair of one message reads
// them here instead of repeating them.
type counterpartMessage struct {
	sourceID       string
	storeID        string
	msgID          string
	dbID           int64
	subject        string
	senderName     string
	senderAddress  string
	conversationID string
	instant        string
	hasAttachments bool
	isRead         bool
}

// counterpartContact is one interned counterpart address: address keeps the
// exact display string of the first message that carried it, norm is the
// folded address the ranking scopes on. Interning is by the exact
// (address, norm) pair, so two spellings of one address stay distinct.
type counterpartContact struct {
	address string
	norm    string
}

// counterpartPair is one (message, counterpart address, direction) fact. The
// indices name the message in CompanyCounterparts.Messages, the contact in
// Contacts and the observed display name in Names; the pair itself holds no
// strings, so a repeated counterpart costs 16 bytes here.
type counterpartPair struct {
	messageIndex int32
	addressIndex int32
	nameIndex    int32
	isSent       bool
}

// counterpartMarker is the change key of one visible owner-content message.
// It records every row fact the counterpart population derives from: the
// message revision (the write path bumps it for the message and its
// recipients) plus the scope verdict revision and values. The folder role
// and kind are covered by marker membership: a role or store kind that
// leaves owner content drops the message from this set, which is exactly the
// change the pair CTE would apply, while a move between two allowed roles
// changes no pair fact. messageIndex names the message in
// CompanyCounterparts.Messages, or -1 for an eligible message that produced
// no pair.
type counterpartMarker struct {
	dbID int64
	// identity names the physical message row. A deleted maximum rowid can
	// be reused by the next insert with a fresh revision of zero, so the
	// revision alone cannot tell a replacement from an untouched message.
	identity        string
	revision        int64
	verdictRevision int64
	verdict         int32
	reason          int32
	messageIndex    int32
}

// counterpartMarkerValues interns the small verdict/reason vocabulary a
// marker set refers to. Comparing markers from two snapshots therefore
// resolves indices through their own table instead of assuming the two
// builds numbered the strings alike.
type counterpartMarkerValues struct {
	values []string
	index  map[string]int32
}

func newCounterpartMarkerValues() *counterpartMarkerValues {
	return &counterpartMarkerValues{values: []string{""}, index: map[string]int32{"": 0}}
}

func (v *counterpartMarkerValues) intern(s string) int32 {
	if index, ok := v.index[s]; ok {
		return index
	}
	index := int32(len(v.values))
	v.index[s] = index
	v.values = append(v.values, s)
	return index
}

// at resolves one interned marker value; an absent table and the empty
// string compare alike.
func (v *counterpartMarkerValues) at(index int32) string {
	if v == nil || index < 0 || int(index) >= len(v.values) {
		return ""
	}
	return v.values[index]
}

// CompanyCounterparts is the all-time counterpart population of one mail
// generation: every owner-visible sender pair and every sent To/Cc pair,
// with the owner's own addresses already removed by counterpartCTE. It is
// facts only: company resolution stays with CompanyBook, and no per-org
// aggregation is cached. The returned value is shared and read-only.
//
// Contacts and Names are the intern tables the pairs index into. Index 0 is
// the empty contact and the empty name, so a zero counterpartPair is valid;
// indices are append-only for the life of the value.
//
// markers is the change map of the next incremental refresh: one entry per
// eligible visible message, including messages that produced no pair. It is
// unexported because it is cache evidence, not a mail fact.
type CompanyCounterparts struct {
	Messages []counterpartMessage
	Pairs    []counterpartPair
	Contacts []counterpartContact
	Names    []string

	markers          []counterpartMarker
	markerValues     *counterpartMarkerValues
	ownerFingerprint string
	fullBuilds       uint64 // full scans in this snapshot's lineage; refresh diagnostics
}

// Build-time capacities keep an average mailbox from regrowing the four
// slices repeatedly. They are hints only: the build clips every slice to its
// final length before publishing it.
const (
	counterpartMessageCapacity = 4096
	counterpartPairCapacity    = 8192
	counterpartContactCapacity = 1024
	counterpartNameCapacity    = 1024
)

// counterpartInterns owns the intern tables of one build: the exact contact
// pair and the exact observed name each map to one append-only index, and
// index 0 stays the empty value.
type counterpartInterns struct {
	contacts  []counterpartContact
	names     []string
	byContact map[counterpartContact]int32
	byName    map[string]int32
}

func newCounterpartInterns() *counterpartInterns {
	return &counterpartInterns{
		contacts:  make([]counterpartContact, 1, counterpartContactCapacity),
		names:     make([]string, 1, counterpartNameCapacity),
		byContact: map[counterpartContact]int32{{}: 0},
		byName:    map[string]int32{"": 0},
	}
}

// internContact returns the index of the exact (address, norm) pair, adding
// one table entry the first time it is seen. An existing index never moves.
func (i *counterpartInterns) internContact(address, norm string) (int32, error) {
	contact := counterpartContact{address: address, norm: norm}
	if index, found := i.byContact[contact]; found {
		return index, nil
	}
	if len(i.contacts) > math.MaxInt32 {
		return 0, fmt.Errorf("store: company counterparts: too many contacts")
	}
	index := int32(len(i.contacts))
	i.byContact[contact] = index
	i.contacts = append(i.contacts, contact)
	return index, nil
}

// internName returns the index of the exact observed display name, adding one
// table entry the first time it is seen. Name variants are never folded.
func (i *counterpartInterns) internName(name string) (int32, error) {
	if index, found := i.byName[name]; found {
		return index, nil
	}
	if len(i.names) > math.MaxInt32 {
		return 0, fmt.Errorf("store: company counterparts: too many names")
	}
	index := int32(len(i.names))
	i.byName[name] = index
	i.names = append(i.names, name)
	return index, nil
}

// CompanyCounterparts reads the counterpart facts of the current mail
// generation. The build is shared by the company ranking and every profile
// read: one scan serves the companies screen instead of one scan per
// consumer. A failed build returns its error to every waiter.
//
// A refresh derives from the installed snapshot instead of rescanning the
// whole pair population: the message and verdict revision markers identify
// the messages whose facts changed, and only those rows are read again. The
// returned value is exact for the generation the call observed (unless a
// writer moves it during the build, in which case the next read applies the
// delta). Callers that want the last coherent snapshot while a refresh runs
// use companyCounterpartsSnapshot.
func (s *Store) CompanyCounterparts(ctx context.Context) (*CompanyCounterparts, error) {
	return s.companyCounterparts(ctx, false)
}

// companyCounterpartsSnapshot is the stale-view form of CompanyCounterparts:
// while a refresh runs it returns the previously installed population, which
// is complete and coherent, never a half-merged one. The company ranking and
// the profile reads deliberately do not use it: their results are tagged
// with the mail generation, so they wait for that generation's snapshot.
func (s *Store) companyCounterpartsSnapshot(ctx context.Context) (*CompanyCounterparts, error) {
	return s.companyCounterparts(ctx, true)
}

func (s *Store) companyCounterparts(ctx context.Context, snapshot bool) (*CompanyCounterparts, error) {
	return s.counterparts.get(ctx, s.MailGeneration(), s.cfg.Snapshot(), snapshot,
		func(buildCtx context.Context, _ *config.Snapshot, previous *CompanyCounterparts, _ bool) (*CompanyCounterparts, error) {
			// The test seam runs inside the detached build, so a panic there
			// is contained the same way a panicking scan is instead of taking
			// the process down with the latch held.
			if hook := s.counterpartsBuildHook; hook != nil {
				hook()
			}
			return s.buildCompanyCounterpartsFrom(buildCtx, previous)
		})
}

// buildCompanyCounterparts reads one complete counterpart population without
// touching the cache. It is the cold-build entry point the benchmarks use;
// the refresh path adds the previous population as its incremental base.
func (s *Store) buildCompanyCounterparts(ctx context.Context) (*CompanyCounterparts, error) {
	return s.buildCompanyCounterpartsFrom(ctx, nil)
}

// buildCompanyCounterpartsFrom builds one population inside a read
// transaction. The marker scan and the pair scan must see the same database
// snapshot: a write that commits between them would otherwise mix a new
// change map with old pair rows.
func (s *Store) buildCompanyCounterpartsFrom(ctx context.Context, previous *CompanyCounterparts) (*CompanyCounterparts, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	return inReadTx(ctx, s.db, "company counterparts", func(tx *sql.Tx) (*CompanyCounterparts, error) {
		ownerFingerprint, err := counterpartOwnerFingerprint(ctx, tx)
		if err != nil {
			return nil, err
		}
		markers, values, err := counterpartMarkers(ctx, tx, vis)
		if err != nil {
			return nil, err
		}

		fullBuilds := uint64(0)
		if previous != nil {
			fullBuilds = previous.fullBuilds
		}
		if previous != nil && previous.ownerFingerprint == ownerFingerprint && len(previous.markers) > 0 {
			changed := counterpartMarkerDiff(previous, markers, values)
			if len(changed) == 0 {
				// The generation moved, but no fact the population reads did
				// (a label click or a contact write is not a counterpart change).
				// Adopt the installed snapshot instead of scanning the pairs.
				return previous, nil
			}
			changedSet := make(map[int64]bool, len(changed))
			for _, id := range changed {
				changedSet[id] = true
			}
			cte, args, err := s.counterpartCTEForMessages(ctx,
				mailquery.Plan{Direction: mailquery.DirectionBoth}, changed)
			if err != nil {
				return nil, err
			}
			partial, err := scanCompanyCounterparts(ctx, tx, cte, args...)
			if err != nil {
				return nil, err
			}
			return mergeCompanyCounterparts(previous, partial, changedSet, markers, values, ownerFingerprint)
		}

		cte, args, err := s.counterpartCTE(ctx, mailquery.Plan{Direction: mailquery.DirectionBoth})
		if err != nil {
			return nil, err
		}
		counterparts, err := scanCompanyCounterparts(ctx, tx, cte, args...)
		if err != nil {
			return nil, err
		}
		counterparts.attachMarkers(markers, values, ownerFingerprint)
		counterparts.fullBuilds = fullBuilds + 1
		return counterparts, nil
	})
}

// counterpartMarkersQuery is the one change-map statement of the
// incremental refresh. It reads only the facts the pair population derives
// from; the folder and selection predicates contribute membership.
func counterpartMarkersQuery(vis string) string {
	// char(31) is the unit separator: a printable-safe field boundary that
	// no source, store, message id or timestamp carries.
	return `SELECT m.id,
		m.source_id || char(31) || m.store_id || char(31) || m.msg_id || char(31) || m.first_seen_at,
		m.revision, COALESCE(v.revision, -1),
		COALESCE(v.verdict, ''), COALESCE(v.reason, '')
		FROM messages m
		CROSS JOIN folders f
		CROSS JOIN scope_verdicts v
		WHERE f.id = m.folder_id AND v.message_id = m.id
			AND ` + ownerFacingWhere(vis) + `
		ORDER BY m.id`
}

// counterpartMarkers reads the change key of every visible owner-content
// message, in message-id order. The set is a superset of the messages that
// can produce a pair (a sent message without To/Cc, or a sender that is an
// owner address, has no pair), so a message that starts or stops producing a
// pair is always detected by its membership.
func counterpartMarkers(ctx context.Context, q queryer, vis string) ([]counterpartMarker, *counterpartMarkerValues, error) {
	values := newCounterpartMarkerValues()
	rows, err := q.QueryContext(ctx, counterpartMarkersQuery(vis))
	if err != nil {
		return nil, nil, fmt.Errorf("store: company counterpart markers: %w", err)
	}
	defer rows.Close()

	markers := make([]counterpartMarker, 0, counterpartMessageCapacity)
	for rows.Next() {
		var marker counterpartMarker
		var verdict, reason string
		if err := rows.Scan(&marker.dbID, &marker.identity, &marker.revision, &marker.verdictRevision,
			&verdict, &reason); err != nil {
			return nil, nil, fmt.Errorf("store: company counterpart marker row: %w", err)
		}
		marker.verdict = values.intern(verdict)
		marker.reason = values.intern(reason)
		marker.messageIndex = -1
		markers = append(markers, marker)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: company counterpart marker rows: %w", err)
	}
	return markers, values, nil
}

// counterpartOwnerFingerprint is the exact owner-address set the pair CTE
// removes. Owner identity writes bump the mail generation and can change
// every message's pairs, so they invalidate the incremental base and force
// one full rebuild. The set is small (one mailbox's addresses).
func counterpartOwnerFingerprint(ctx context.Context, q queryer) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT source_id, store_id, address
		FROM owner_identities ORDER BY source_id, store_id, address`)
	if err != nil {
		return "", fmt.Errorf("store: owner identity fingerprint: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	var sourceID, storeID, address string
	for rows.Next() {
		if err := rows.Scan(&sourceID, &storeID, &address); err != nil {
			return "", fmt.Errorf("store: owner identity fingerprint row: %w", err)
		}
		b.WriteString(sourceID)
		b.WriteByte('\x00')
		b.WriteString(storeID)
		b.WriteByte('\x00')
		b.WriteString(address)
		b.WriteByte('\x00')
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("store: owner identity fingerprint rows: %w", err)
	}
	return b.String(), nil
}

// counterpartMarkerDiff returns the message ids whose marker facts differ
// between the installed population and the current marker set, including
// additions and removals. Both marker slices are sorted by message id.
func counterpartMarkerDiff(previous *CompanyCounterparts, current []counterpartMarker,
	currentValues *counterpartMarkerValues) []int64 {
	oldMarkers, oldValues := previous.markers, previous.markerValues
	changed := make([]int64, 0)
	i, j := 0, 0
	for i < len(oldMarkers) || j < len(current) {
		switch {
		case j >= len(current) || (i < len(oldMarkers) && oldMarkers[i].dbID < current[j].dbID):
			changed = append(changed, oldMarkers[i].dbID)
			i++
		case i >= len(oldMarkers) || current[j].dbID < oldMarkers[i].dbID:
			changed = append(changed, current[j].dbID)
			j++
		default:
			if !counterpartMarkerSame(oldMarkers[i], oldValues, current[j], currentValues) {
				changed = append(changed, current[j].dbID)
			}
			i++
			j++
		}
	}
	return changed
}

// counterpartMarkerSame compares the stable facts of two markers. The
// message index is deliberately excluded: it is recomputed after the merge,
// and a message moving in the population is not a mail change. The two
// strings resolve through each snapshot's own intern table, so a rebuild
// that numbered the vocabulary differently still compares equal.
func counterpartMarkerSame(a counterpartMarker, av *counterpartMarkerValues,
	b counterpartMarker, bv *counterpartMarkerValues) bool {
	return a.identity == b.identity &&
		a.revision == b.revision &&
		a.verdictRevision == b.verdictRevision &&
		av.at(a.verdict) == bv.at(b.verdict) &&
		av.at(a.reason) == bv.at(b.reason)
}

// attachMarkers installs a marker set on a freshly scanned population and
// points every marker at its message index (or -1 when it produced no pair).
func (c *CompanyCounterparts) attachMarkers(markers []counterpartMarker,
	values *counterpartMarkerValues, ownerFingerprint string) {
	index := make(map[int64]int32, len(c.Messages))
	for i := range c.Messages {
		index[c.Messages[i].dbID] = int32(i)
	}
	c.assignMarkers(markers, values, ownerFingerprint, index)
}

// assignMarkers uses the message index map the caller already built, so a
// merge does not walk the population a second time.
func (c *CompanyCounterparts) assignMarkers(markers []counterpartMarker,
	values *counterpartMarkerValues, ownerFingerprint string, index map[int64]int32) {
	for i := range markers {
		if messageIndex, ok := index[markers[i].dbID]; ok {
			markers[i].messageIndex = messageIndex
		} else {
			markers[i].messageIndex = -1
		}
	}
	c.markers = markers
	c.markerValues = values
	c.ownerFingerprint = ownerFingerprint
}

// mergeCompanyCounterparts applies one incremental partial population to the
// installed one. It rebuilds the intern tables from the surviving and new
// pairs, so removed contacts and names do not accumulate across refreshes,
// and it replaces the cached pointer only after the merged value is whole.
func mergeCompanyCounterparts(previous, partial *CompanyCounterparts, changed map[int64]bool,
	markers []counterpartMarker, values *counterpartMarkerValues,
	ownerFingerprint string) (*CompanyCounterparts, error) {
	merged := &CompanyCounterparts{fullBuilds: previous.fullBuilds}
	merged.Messages = make([]counterpartMessage, 0, len(previous.Messages)+len(partial.Messages))
	index := make(map[int64]int32, len(previous.Messages)+len(partial.Messages))
	appendMessage := func(message counterpartMessage) {
		index[message.dbID] = int32(len(merged.Messages))
		merged.Messages = append(merged.Messages, message)
	}
	// Both inputs are sorted by dbID. Merge them linearly: a changed message
	// is represented by its partial version (or dropped when it no longer
	// produces a pair), an unchanged one by its previous version.
	i, j := 0, 0
	for i < len(previous.Messages) && j < len(partial.Messages) {
		switch {
		case previous.Messages[i].dbID < partial.Messages[j].dbID:
			if !changed[previous.Messages[i].dbID] {
				appendMessage(previous.Messages[i])
			}
			i++
		case partial.Messages[j].dbID < previous.Messages[i].dbID:
			appendMessage(partial.Messages[j])
			j++
		default:
			appendMessage(partial.Messages[j])
			i++
			j++
		}
	}
	for ; i < len(previous.Messages); i++ {
		if !changed[previous.Messages[i].dbID] {
			appendMessage(previous.Messages[i])
		}
	}
	for ; j < len(partial.Messages); j++ {
		appendMessage(partial.Messages[j])
	}

	oldPairCount := 0
	merged.Pairs = make([]counterpartPair, 0, len(previous.Pairs)+len(partial.Pairs))
	for i := range previous.Pairs {
		dbID := previous.Messages[previous.Pairs[i].messageIndex].dbID
		if changed[dbID] {
			continue
		}
		pair := previous.Pairs[i]
		pair.messageIndex = index[dbID]
		merged.Pairs = append(merged.Pairs, pair)
		oldPairCount++
	}
	for i := range partial.Pairs {
		dbID := partial.Messages[partial.Pairs[i].messageIndex].dbID
		pair := partial.Pairs[i]
		pair.messageIndex = index[dbID]
		merged.Pairs = append(merged.Pairs, pair)
	}

	// Intern surviving and new pairs again in one pass. The first
	// oldPairCount pairs read the previous tables; the rest read the partial
	// tables, whose indices are independent.
	interns := newCounterpartInterns()
	for i := range merged.Pairs {
		addressIndex, nameIndex := merged.Pairs[i].addressIndex, merged.Pairs[i].nameIndex
		var contact counterpartContact
		var name string
		if i < oldPairCount {
			contact = previous.Contacts[addressIndex]
			name = previous.Names[nameIndex]
		} else {
			contact = partial.Contacts[addressIndex]
			name = partial.Names[nameIndex]
		}
		nextAddress, err := interns.internContact(contact.address, contact.norm)
		if err != nil {
			return nil, err
		}
		nextName, err := interns.internName(name)
		if err != nil {
			return nil, err
		}
		merged.Pairs[i].addressIndex, merged.Pairs[i].nameIndex = nextAddress, nextName
	}
	merged.Contacts = slices.Clip(interns.contacts)
	merged.Names = slices.Clip(interns.names)
	merged.assignMarkers(markers, values, ownerFingerprint, index)
	return merged, nil
}

// scanCompanyCounterparts reads the pair rows of one CTE into a population.
// The full build and the partial refresh share this one row fold, so the two
// paths cannot drift. Messages are stored by database id; the pairs are
// remapped after the message sort.
func scanCompanyCounterparts(ctx context.Context, q queryer, cte string, args ...any) (*CompanyCounterparts, error) {
	rows, err := q.QueryContext(ctx, companyCounterpartRowsQuery(cte), args...)
	if err != nil {
		return nil, fmt.Errorf("store: company counterparts: %w", err)
	}
	defer rows.Close()

	counterparts := &CompanyCounterparts{
		Messages: make([]counterpartMessage, 0, counterpartMessageCapacity),
		Pairs:    make([]counterpartPair, 0, counterpartPairCapacity),
	}
	interns := newCounterpartInterns()
	indexByMessage := map[int64]int32{}
	for rows.Next() {
		var sourceID, storeID, msgID, conversationID, instant, addressNorm, address, name string
		var messageID int64
		var isSent, hasAttachments, isRead int
		var subject, senderName, senderAddress string
		if err := rows.Scan(&sourceID, &storeID, &msgID, &messageID, &conversationID, &instant, &isSent,
			&addressNorm, &address, &name, &subject, &senderName, &senderAddress, &hasAttachments, &isRead); err != nil {
			return nil, fmt.Errorf("store: company counterpart row: %w", err)
		}
		index, found := indexByMessage[messageID]
		if !found {
			if len(counterparts.Messages) > math.MaxInt32 {
				return nil, fmt.Errorf("store: company counterparts: too many messages")
			}
			index = int32(len(counterparts.Messages))
			indexByMessage[messageID] = index
			counterparts.Messages = append(counterparts.Messages, counterpartMessage{
				sourceID: sourceID, storeID: storeID, msgID: msgID,
				dbID: messageID, subject: subject, senderName: senderName, senderAddress: senderAddress,
				conversationID: conversationID, instant: instant,
				hasAttachments: hasAttachments != 0, isRead: isRead != 0,
			})
		}
		addressIndex, err := interns.internContact(address, addressNorm)
		if err != nil {
			return nil, err
		}
		nameIndex, err := interns.internName(name)
		if err != nil {
			return nil, err
		}
		counterparts.Pairs = append(counterparts.Pairs, counterpartPair{
			messageIndex: index, addressIndex: addressIndex, nameIndex: nameIndex, isSent: isSent != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: company counterpart rows: %w", err)
	}

	// The scan has no ORDER BY (the folds are order-independent). Sorting
	// by database id gives the incremental diff and merge one stable key.
	order := make([]int32, len(counterparts.Messages))
	for i := range order {
		order[i] = int32(i)
	}
	sort.Slice(order, func(i, j int) bool {
		return counterparts.Messages[order[i]].dbID < counterparts.Messages[order[j]].dbID
	})
	sortedMessages := make([]counterpartMessage, len(counterparts.Messages))
	remap := make([]int32, len(counterparts.Messages))
	for newIndex, oldIndex := range order {
		sortedMessages[newIndex] = counterparts.Messages[oldIndex]
		remap[oldIndex] = int32(newIndex)
	}
	counterparts.Messages = sortedMessages
	for i := range counterparts.Pairs {
		counterparts.Pairs[i].messageIndex = remap[counterparts.Pairs[i].messageIndex]
	}
	counterparts.Messages = slices.Clip(counterparts.Messages)
	counterparts.Pairs = slices.Clip(counterparts.Pairs)
	counterparts.Contacts = slices.Clip(interns.contacts)
	counterparts.Names = slices.Clip(interns.names)
	return counterparts, nil
}

// companyCounterpartRowsQuery is the one fact scan behind CompanyCounterparts:
// the profile row shape plus the counterpart address and the message's read
// state. It has no ORDER BY; the folds are order-independent.
func companyCounterpartRowsQuery(cte string) string {
	return cte + ` SELECT p.source_id, p.store_id, p.msg_id, p.message_id,
		p.conversation_id, p.instant, p.is_sent, p.address_norm, p.address, p.name,
		m.subject, m.sender_name, m.sender_address, m.has_attachments, m.is_read
		FROM clean p JOIN messages m ON m.id = p.message_id`
}

// counterpartRow is the common row shape of both company folds. The ranking's
// SQL fallback fills only the pair fields; the cached ranking and every
// profile row fill the message metadata too.
type counterpartRow struct {
	sourceID       string
	storeID        string
	msgID          string
	messageID      int64
	conversationID string
	instant        string
	isSent         bool
	addressNorm    string
	address        string
	name           string
	subject        string
	senderName     string
	senderAddress  string
	hasAttachments bool
	isRead         bool
}

// counterpartRowReader streams counterpart rows to the shared profile fold.
// The SQL parity oracle and the cached production path differ only here.
type counterpartRowReader interface {
	Next() bool
	Row() (counterpartRow, error)
	Err() error
	Close() error
}

// sqlCounterpartRows adapts a profile-shaped SQL result to counterpartRow.
type sqlCounterpartRows struct{ rows *sql.Rows }

func (r *sqlCounterpartRows) Next() bool { return r.rows.Next() }

func (r *sqlCounterpartRows) Row() (counterpartRow, error) {
	var row counterpartRow
	var isSent, hasAttachments int
	if err := r.rows.Scan(&row.sourceID, &row.storeID, &row.msgID, &row.messageID,
		&row.conversationID, &row.instant, &isSent, &row.addressNorm, &row.name,
		&row.subject, &row.senderName, &row.senderAddress, &hasAttachments); err != nil {
		return counterpartRow{}, fmt.Errorf("store: company profile row: %w", err)
	}
	row.isSent, row.hasAttachments = isSent != 0, hasAttachments != 0
	return row, nil
}

func (r *sqlCounterpartRows) Err() error {
	if err := r.rows.Err(); err != nil {
		return fmt.Errorf("store: company profile rows: %w", err)
	}
	return nil
}

func (r *sqlCounterpartRows) Close() error { return r.rows.Close() }

// cachedCounterpartRows streams the profile window [since, until) from the
// cached population with the same lexical instant comparison as
// appendTimeAndMessageFilters. Empty bounds are omitted exactly as there.
type cachedCounterpartRows struct {
	counterparts *CompanyCounterparts
	since        string
	until        string
	next         int
	row          counterpartRow
}

func newCachedCounterpartRows(counterparts *CompanyCounterparts, since, until string) *cachedCounterpartRows {
	return &cachedCounterpartRows{counterparts: counterparts, since: since, until: until}
}

func (r *cachedCounterpartRows) Next() bool {
	for r.next < len(r.counterparts.Pairs) {
		pair := &r.counterparts.Pairs[r.next]
		r.next++
		message := &r.counterparts.Messages[pair.messageIndex]
		if r.since != "" && message.instant < r.since {
			continue
		}
		if r.until != "" && message.instant >= r.until {
			continue
		}
		contact := r.counterparts.Contacts[pair.addressIndex]
		r.row = counterpartRow{
			sourceID: message.sourceID, storeID: message.storeID, msgID: message.msgID,
			messageID: message.dbID, conversationID: message.conversationID, instant: message.instant,
			isSent: pair.isSent, addressNorm: contact.norm, address: contact.address,
			name: r.counterparts.Names[pair.nameIndex], subject: message.subject,
			senderName: message.senderName, senderAddress: message.senderAddress,
			hasAttachments: message.hasAttachments, isRead: message.isRead,
		}
		return true
	}
	return false
}

func (r *cachedCounterpartRows) Row() (counterpartRow, error) { return r.row, nil }
func (r *cachedCounterpartRows) Err() error                   { return nil }
func (r *cachedCounterpartRows) Close() error                 { return nil }

// companyCounterpartsBuildCount reports how many builds this store started.
// It exists for the cache tests, which must prove a generation shares one
// build rather than one per consumer.
func (s *Store) companyCounterpartsBuildCount() uint64 {
	return s.counterparts.buildCount()
}
