package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
)

// ExecuteMailQuery runs one validated, resolved plan over visible owner
// mail. It is the only place a model's plan becomes numbers: every value is
// a bound parameter, every ordering is chosen from the plan's fixed set, and
// no plan text reaches SQL as syntax.
func (s *Store) ExecuteMailQuery(ctx context.Context, plan mailquery.Plan, loc *time.Location) (mailquery.Result, error) {
	plan = plan.Normalize()
	if err := plan.Validate(); err != nil {
		return mailquery.Result{}, err
	}
	if plan.TimeRange != mailquery.TimeAll && plan.Since == "" && plan.Until == "" {
		return mailquery.Result{}, fmt.Errorf("store: mail query time range %q is unresolved", plan.TimeRange)
	}
	if loc == nil {
		loc = time.UTC
	}
	switch plan.Kind {
	case mailquery.KindContacts:
		return s.queryContacts(ctx, plan)
	case mailquery.KindCompanies:
		return s.queryCompanies(ctx, plan)
	case mailquery.KindMessages:
		return s.queryMessages(ctx, plan)
	case mailquery.KindTime:
		return s.queryTime(ctx, plan, loc)
	default:
		return mailquery.Result{}, fmt.Errorf("store: unsupported mail query kind %q", plan.Kind)
	}
}

// counterpartCTE is the common start of both counterpart rankings: one row
// per received sender or sent To/Cc recipient of visible owner mail, with
// the owner's own addresses removed. Callers aggregate clean by contact or
// by organization; neither re-implements the scope, direction, time or
// message filters.
func (s *Store) counterpartCTE(ctx context.Context, plan mailquery.Plan) (string, []any, error) {
	return s.counterpartCTEForMessages(ctx, plan, nil)
}

// counterpartCTEForMessages narrows the same counterpart scope to the given
// message row ids. The incremental counterpart refresh uses it to read only
// the messages whose revision markers moved, instead of streaming the whole
// all-time pair population again. An empty id list is the full CTE.
func (s *Store) counterpartCTEForMessages(ctx context.Context, plan mailquery.Plan, messageIDs []int64) (string, []any, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return "", nil, err
	}
	// Counterpart analysis is owner-facing content: a picked shared/delegate
	// store may mark its own mailbox owner as verdict=owner, but its subjects,
	// senders and recipients must never enter a contact, company or profile
	// read. Shared replies still reach ThreadStates for the state only.
	where := []string{ownerFacingWhere(vis)}
	var args []any
	switch plan.Direction {
	case mailquery.DirectionSent:
		where = append(where, sentByOwnerWhere, "r.address_norm IS NOT NULL")
	case mailquery.DirectionReceived:
		where = append(where, notSentByOwnerWhere)
	default:
		where = append(where, "("+notSentByOwnerWhere+" OR r.address_norm IS NOT NULL)")
	}
	where, args = appendTimeAndMessageFilters(where, args, plan)
	if len(messageIDs) > 0 {
		where = append(where, "m.id IN "+idSetSQL)
		args = append(args, idSet(messageIDs))
	}
	cleanWhere := []string{"p.address_norm <> ''"}
	var cleanArgs []any
	if len(plan.ContactAddresses) > 0 {
		clause, addressArgs := addressInSQL("p.address_norm", plan.ContactAddresses)
		cleanWhere = append(cleanWhere, clause)
		cleanArgs = append(cleanArgs, addressArgs...)
	}
	return `WITH pairs AS (
		SELECT m.id AS message_id, m.source_id, m.store_id, m.msg_id, m.conversation_id,
			` + instantSQL + ` AS instant,
			` + sentByOwnerFlag + ` AS is_sent,
			CASE WHEN ` + sentByOwnerWhere + `
				THEN ` + recipientNormSQL + `
				ELSE ` + senderNormSQL + ` END AS address_norm,
			CASE WHEN ` + sentByOwnerWhere + ` THEN r.address ELSE m.sender_address END AS address,
			CASE WHEN ` + sentByOwnerWhere + ` THEN r.name ELSE m.sender_name END AS name
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		LEFT JOIN recipients r ON r.message_id = m.id
			AND ` + sentByOwnerWhere + ` AND r.kind IN ('to', 'cc')
		WHERE ` + strings.Join(where, " AND ") + `
	), clean AS (
		SELECT p.* FROM pairs p
		WHERE ` + strings.Join(cleanWhere, " AND ") + `
			AND NOT EXISTS (
				SELECT 1 FROM owner_identities oi
				WHERE oi.source_id = p.source_id AND oi.store_id = p.store_id
					AND ` + identityNormSQL + ` = p.address_norm
			)
	)`, append(args, cleanArgs...), nil
}

// queryContacts aggregates one row per counterpart. Received mail counts the
// sender; sent mail counts each To/Cc recipient. The owner's own addresses
// are removed. Two-way "exchanges" keep only contacts with at least one
// message in each direction.
func (s *Store) queryContacts(ctx context.Context, plan mailquery.Plan) (mailquery.Result, error) {
	cte, args, err := s.counterpartCTE(ctx, plan)
	if err != nil {
		return mailquery.Result{}, err
	}

	order := "a.messages DESC, a.threads DESC, a.last_at DESC, a.address_norm ASC"
	switch plan.Metric {
	case mailquery.MetricThreads:
		order = "a.threads DESC, a.messages DESC, a.last_at DESC, a.address_norm ASC"
	case mailquery.MetricExchanges:
		order = "a.messages DESC, a.threads DESC, a.last_at DESC, a.address_norm ASC"
	}
	if plan.Order == mailquery.OrderAsc {
		order = strings.ReplaceAll(order, " DESC", " ASC")
	}

	finalWhere := []string{}
	var finalArgs []any
	if len(plan.ContactAddresses) == 0 && plan.Contact != "" {
		// The contact filter is a folded substring, never a LIKE pattern:
		// accents match their plain form and % or _ in a name stays literal.
		folded := mail.Fold(plan.Contact)
		finalWhere = append(finalWhere, "(instr("+foldSQL+"(a.address_norm), ?) > 0 OR instr("+foldSQL+"(COALESCE(n.name, '')), ?) > 0)")
		finalArgs = append(finalArgs, folded, folded)
	}
	if plan.Metric == mailquery.MetricExchanges {
		finalWhere = append(finalWhere, "a.sent > 0 AND a.received > 0")
	}

	query := cte + `, names AS (
		SELECT address_norm, name, COUNT(*) AS n,
			ROW_NUMBER() OVER (PARTITION BY address_norm ORDER BY COUNT(*) DESC, lower(name) ASC) AS rn
		FROM clean
		WHERE trim(name) <> ''
		GROUP BY address_norm, name
	), agg AS (
		SELECT address_norm, MAX(address) AS address, COUNT(*) AS messages,
			SUM(is_sent) AS sent, SUM(1 - is_sent) AS received,
			COUNT(DISTINCT CASE WHEN conversation_id <> '' THEN conversation_id
				ELSE 'm:' || message_id END) AS threads,
			MAX(instant) AS last_at
		FROM clean
		GROUP BY address_norm
	)
	SELECT a.address_norm, COALESCE(n.name, a.address), a.address, a.messages, a.sent, a.received,
		a.threads, a.last_at, SUM(a.messages) OVER () AS total_messages
	FROM agg a
	LEFT JOIN names n ON n.address_norm = a.address_norm AND n.rn = 1`
	if len(finalWhere) > 0 {
		query += " WHERE " + strings.Join(finalWhere, " AND ")
	}
	query += " ORDER BY " + order + " LIMIT ?"
	finalArgs = append(finalArgs, plan.Limit)

	rows, err := s.db.QueryContext(ctx, query, append(args, finalArgs...)...)
	if err != nil {
		return mailquery.Result{}, fmt.Errorf("store: contact query: %w", err)
	}
	defer rows.Close()
	out := mailquery.Result{Kind: plan.Kind, Action: plan.Action, Metric: plan.Metric,
		Rows: []mailquery.Row{}, Since: plan.Since, Until: plan.Until}
	for rows.Next() {
		var row mailquery.Row
		var total int
		if err := rows.Scan(&row.Key, &row.Label, &row.Address, &row.Messages, &row.Sent, &row.Received,
			&row.Threads, &row.LastAt, &total); err != nil {
			return mailquery.Result{}, fmt.Errorf("store: contact row: %w", err)
		}
		out.Rows = append(out.Rows, row)
		out.Total = total
	}
	if err := rows.Err(); err != nil {
		return mailquery.Result{}, fmt.Errorf("store: contact rows: %w", err)
	}
	return out, nil
}

// queryCompanies ranks external organizations, never people. An address
// belongs to the company its visible address-book entry declares; otherwise
// to the dominant declared company of its non-shared registrable domain;
// otherwise to that domain. A shared domain with no declared company (free
// mail, an ISP) is not an organization and is skipped: the ranking never
// invents a company for a personal address. Owner organizations are
// internal and never ranked.
//
// Messages are deduplicated per organization, so one sent message addressed
// to several people at the same company counts once.
func (s *Store) queryCompanies(ctx context.Context, plan mailquery.Plan) (mailquery.Result, error) {
	book, err := s.CompanyBook(ctx)
	if err != nil {
		return mailquery.Result{}, err
	}
	// Body text is the one filter the pair cache cannot serve: it is not a
	// stored message fact the ranking reads. Keep that plan on SQL.
	if plan.Text != "" {
		return s.queryCompaniesSQL(ctx, plan, book)
	}
	counterparts, err := s.CompanyCounterparts(ctx)
	if err != nil {
		return mailquery.Result{}, err
	}
	return companyQueryResult(foldCompanyCounterparts(counterparts, book, plan), plan), nil
}

// foldCompanyCounterparts applies one ranking plan to a cached population.
// The cached ranking and its parity tests share this fold, so a refresh path
// cannot drift from the production ranking.
func foldCompanyCounterparts(counterparts *CompanyCounterparts, book *CompanyBook,
	plan mailquery.Plan) map[string]*companyAgg {
	orgs := map[string]*companyAgg{}
	for i := range counterparts.Pairs {
		pair := &counterparts.Pairs[i]
		message := &counterparts.Messages[pair.messageIndex]
		contact := counterparts.Contacts[pair.addressIndex]
		if !companyPairMatches(*pair, *message, contact.norm, plan) {
			continue
		}
		foldCompanyAgg(orgs, book, counterpartRow{
			sourceID: message.sourceID, storeID: message.storeID, msgID: message.msgID,
			messageID: message.dbID, conversationID: message.conversationID, instant: message.instant,
			isSent: pair.isSent, addressNorm: contact.norm, address: contact.address,
			name: counterparts.Names[pair.nameIndex], subject: message.subject,
			senderName: message.senderName, senderAddress: message.senderAddress,
			hasAttachments: message.hasAttachments, isRead: message.isRead,
		})
	}
	return orgs
}

// queryCompaniesSQL is the text-filtered fallback: body text is not cached,
// so this one plan still streams its filtered counterpart pairs from SQL.
func (s *Store) queryCompaniesSQL(ctx context.Context, plan mailquery.Plan, book *CompanyBook) (mailquery.Result, error) {
	cte, args, err := s.counterpartCTE(ctx, plan)
	if err != nil {
		return mailquery.Result{}, err
	}
	rows, err := s.db.QueryContext(ctx, cte+` SELECT p.source_id, p.store_id, p.msg_id, p.message_id,
		p.conversation_id, p.instant, p.is_sent, p.address_norm, p.address, p.name FROM clean p`, args...)
	if err != nil {
		return mailquery.Result{}, fmt.Errorf("store: company query: %w", err)
	}
	defer rows.Close()

	orgs := map[string]*companyAgg{}
	for rows.Next() {
		var row counterpartRow
		var isSent int
		if err := rows.Scan(&row.sourceID, &row.storeID, &row.msgID, &row.messageID, &row.conversationID,
			&row.instant, &isSent, &row.addressNorm, &row.address, &row.name); err != nil {
			return mailquery.Result{}, fmt.Errorf("store: company row: %w", err)
		}
		row.isSent = isSent != 0
		foldCompanyAgg(orgs, book, row)
	}
	if err := rows.Err(); err != nil {
		return mailquery.Result{}, fmt.Errorf("store: company rows: %w", err)
	}
	return companyQueryResult(orgs, plan), nil
}

// companyPairMatches applies a ranking plan's filters to one cached pair with
// the same semantics as appendTimeAndMessageFilters: lexical instants, the
// exact message direction, read state and attachment flag, and an exact
// normalized address scope. The instant comes from the pair's message and the
// scoped address from the pair's interned contact.
func companyPairMatches(pair counterpartPair, message counterpartMessage, addressNorm string, plan mailquery.Plan) bool {
	if plan.Since != "" && message.instant < plan.Since {
		return false
	}
	if plan.Until != "" && message.instant >= plan.Until {
		return false
	}
	switch plan.Direction {
	case mailquery.DirectionSent:
		if !pair.isSent {
			return false
		}
	case mailquery.DirectionReceived:
		if pair.isSent {
			return false
		}
	}
	if plan.Unread != nil && message.isRead == *plan.Unread {
		return false
	}
	if plan.HasAttachments != nil && message.hasAttachments != *plan.HasAttachments {
		return false
	}
	if len(plan.ContactAddresses) > 0 {
		found := false
		for _, address := range plan.ContactAddresses {
			if addressNorm == address {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// foldCompanyAgg adds one counterpart pair to its organization's distinct-
// message aggregation. The cached ranking and its text-filtered SQL fallback
// share this fold.
func foldCompanyAgg(orgs map[string]*companyAgg, book *CompanyBook, row counterpartRow) {
	resolved, ok := book.resolve(row.addressNorm)
	if !ok || book.isInternalResolution(resolved) {
		return
	}
	org := resolved.org
	message := messageIdentity(row.sourceID, row.storeID, row.msgID, row.messageID)
	agg := orgs[org.Key]
	if agg == nil {
		agg = &companyAgg{org: org, domain: resolved.domain, messages: map[string]bool{}, sent: map[string]bool{},
			received: map[string]bool{}, threads: map[string]bool{}, contacts: map[string]bool{},
			domains: map[string]map[string]bool{}, spellings: map[string]int{}, names: map[string]int{}}
		orgs[org.Key] = agg
	}
	agg.messages[message] = true
	if !row.isSent {
		if observed := strings.TrimSpace(row.name); observed != "" {
			agg.names[observed]++
		}
	}
	if row.isSent {
		agg.sent[message] = true
	} else {
		agg.received[message] = true
	}
	if row.conversationID != "" {
		agg.threads["c:"+row.conversationID] = true
	} else {
		agg.threads[fmt.Sprintf("m:%d", row.messageID)] = true
	}
	if org.Declared {
		if !agg.contacts[row.addressNorm] {
			agg.spellings[resolved.spelling]++
		}
		if resolved.domain != "" {
			if agg.domains[resolved.domain] == nil {
				agg.domains[resolved.domain] = map[string]bool{}
			}
			agg.domains[resolved.domain][message] = true
		}
	}
	agg.contacts[row.addressNorm] = true
	if agg.lastAt == "" || row.instant > agg.lastAt {
		agg.lastAt = row.instant
	}
}

// companyQueryResult applies the exchange metric, the folded plan filter,
// totals, order and limit to one set of organization aggregations.
func companyQueryResult(orgs map[string]*companyAgg, plan mailquery.Plan) mailquery.Result {
	filter := mail.Fold(plan.Contact)
	if len(plan.ContactAddresses) > 0 {
		filter = ""
	}
	out := mailquery.Result{Kind: plan.Kind, Action: plan.Action, Metric: plan.Metric,
		Rows: make([]mailquery.Row, 0, len(orgs)), Since: plan.Since, Until: plan.Until}
	for _, agg := range orgs {
		if plan.Metric == mailquery.MetricExchanges && (len(agg.sent) == 0 || len(agg.received) == 0) {
			continue
		}
		row := agg.row()
		if filter != "" && !agg.matches(filter, row.Label) {
			continue
		}
		out.Total += row.Messages
		out.Rows = append(out.Rows, row)
	}
	out.Matched = len(out.Rows)
	sort.Slice(out.Rows, func(i, j int) bool {
		return companyRowLess(out.Rows[i], out.Rows[j], plan.Metric, plan.Order)
	})
	if len(out.Rows) > plan.Limit {
		out.Rows = out.Rows[:plan.Limit]
	}
	return out
}

// companyAgg is one organization's distinct-message aggregation. Sets, not
// counters, make a sent message addressed to several people at the same
// company count once.
type companyAgg struct {
	org       CompanyOrg
	domain    string // the resolution domain of the first observed pair
	messages  map[string]bool
	sent      map[string]bool
	received  map[string]bool
	threads   map[string]bool
	contacts  map[string]bool
	domains   map[string]map[string]bool // domain -> distinct messages
	spellings map[string]int             // declared spelling -> distinct contacts
	names     map[string]int             // observed received display name -> messages
	lastAt    string
}

// row is the public row of one organization: a declared company uses its
// most common spelling and most-used non-shared domain; a domain
// organization uses the domain for both.
func (a *companyAgg) row() mailquery.Row {
	label, address := a.org.Label, a.domain
	if a.org.Declared {
		label = dominantCompanySpelling(a.spellings)
		address = mostUsedCompanyDomain(a.domains)
	} else if a.domain != "" {
		// A domain organization has no declared name; derive the one the
		// local people and business vocabulary support instead of showing
		// the raw domain.
		label = domainDisplayName(a.domain, a.names)
	}
	return mailquery.Row{
		Key: a.org.Key, Label: label, Address: address,
		Messages: len(a.messages), Sent: len(a.sent), Received: len(a.received),
		Threads: len(a.threads), Contacts: len(a.contacts), LastAt: a.lastAt,
	}
}

// matches reports whether the folded plan filter names the row's label, one
// of the organization's domains or one of its merged addresses.
func (a *companyAgg) matches(folded, label string) bool {
	if strings.Contains(mail.Fold(label), folded) {
		return true
	}
	if a.domain != "" && strings.Contains(mail.Fold(a.domain), folded) {
		return true
	}
	for domain := range a.domains {
		if strings.Contains(mail.Fold(domain), folded) {
			return true
		}
	}
	for address := range a.contacts {
		if strings.Contains(mail.Fold(address), folded) {
			return true
		}
	}
	return false
}

// mostUsedCompanyDomain is the organization's domain with the most distinct
// messages, alphabetically on a tie. It is empty for a declared company
// known only through a shared domain.
func mostUsedCompanyDomain(byMessage map[string]map[string]bool) string {
	best, bestN := "", -1
	for domain, messages := range byMessage {
		n := len(messages)
		if n > bestN || (n == bestN && domain < best) {
			best, bestN = domain, n
		}
	}
	return best
}

// companyRowLess orders company rows by the requested metric. An ascending
// order reverses the whole comparison, including the tie-breaks.
func companyRowLess(a, b mailquery.Row, metric mailquery.Metric, order mailquery.Order) bool {
	var cmp int
	switch metric {
	case mailquery.MetricThreads:
		cmp = compareInt(b.Threads, a.Threads)
		if cmp == 0 {
			cmp = strings.Compare(b.LastAt, a.LastAt)
		}
	default:
		cmp = compareInt(b.Messages, a.Messages)
		if cmp == 0 {
			cmp = compareInt(b.Threads, a.Threads)
		}
		if cmp == 0 {
			cmp = strings.Compare(b.LastAt, a.LastAt)
		}
	}
	if cmp == 0 {
		cmp = strings.Compare(mail.Fold(a.Label), mail.Fold(b.Label))
	}
	if cmp == 0 {
		cmp = strings.Compare(a.Key, b.Key)
	}
	if order == mailquery.OrderAsc {
		return cmp > 0
	}
	return cmp < 0
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// ownerMailWhere is the one message population of a mail query: the
// owner-facing boundary, the plan's direction, its exact counterparts or
// contact filter, and the shared time, flag and label filters. queryMessages
// and queryTime both match this population, so a count, a page and a time
// bucket cannot describe different mail.
func (s *Store) ownerMailWhere(ctx context.Context, plan mailquery.Plan) (string, []any, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return "", nil, err
	}
	where := []string{ownerFacingWhere(vis)}
	var args []any
	switch plan.Direction {
	case mailquery.DirectionSent:
		where = append(where, sentByOwnerWhere)
	case mailquery.DirectionReceived:
		where = append(where, notSentByOwnerWhere)
	}
	if len(plan.ContactAddresses) > 0 {
		exact, exactArgs := exactCounterpartWhere(plan.ContactAddresses)
		where = append(where, exact)
		args = append(args, exactArgs...)
	} else {
		where, args = appendContactFilter(where, args, plan.Contact)
	}
	where, args = appendTimeAndMessageFilters(where, args, plan)
	return strings.Join(where, " AND "), args, nil
}

// queryMessages counts or lists messages matching the plan's filters. It
// never explodes a message per recipient, so counts are message counts.
func (s *Store) queryMessages(ctx context.Context, plan mailquery.Plan) (mailquery.Result, error) {
	condition, args, err := s.ownerMailWhere(ctx, plan)
	if err != nil {
		return mailquery.Result{}, err
	}

	cte := `WITH msgs AS (
		SELECT m.id AS message_id, m.subject, m.sender_name, m.sender_address,
			` + instantSQL + ` AS instant,
			` + sentByOwnerFlag + ` AS is_sent
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + condition + `
	)`
	out := mailquery.Result{Kind: plan.Kind, Action: plan.Action, Rows: []mailquery.Row{},
		Since: plan.Since, Until: plan.Until}
	if plan.Action == mailquery.ActionCount {
		if err := s.db.QueryRowContext(ctx, cte+" SELECT COUNT(*) FROM msgs", args...).Scan(&out.Total); err != nil {
			return mailquery.Result{}, fmt.Errorf("store: message count: %w", err)
		}
		return out, nil
	}
	order := "instant DESC, message_id DESC"
	if plan.Order == mailquery.OrderAsc {
		order = "instant ASC, message_id ASC"
	}
	rows, err := s.db.QueryContext(ctx, cte+` SELECT message_id, subject, sender_name, instant, is_sent,
		COUNT(*) OVER () AS total FROM msgs ORDER BY `+order+` LIMIT ?`,
		append(args, plan.Limit)...)
	if err != nil {
		return mailquery.Result{}, fmt.Errorf("store: message list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row mailquery.Row
		var sent int
		if err := rows.Scan(&row.MessageID, &row.Subject, &row.SenderName, &row.Instant, &sent, &out.Total); err != nil {
			return mailquery.Result{}, fmt.Errorf("store: message row: %w", err)
		}
		row.Key = fmt.Sprintf("%d", row.MessageID)
		row.SentFlag = sent != 0
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return mailquery.Result{}, fmt.Errorf("store: message rows: %w", err)
	}
	return out, nil
}

// queryTime buckets matching messages in the owner's zone. It groups in Go
// so a DST transition never turns two local midnights into one UTC hour.
func (s *Store) queryTime(ctx context.Context, plan mailquery.Plan, loc *time.Location) (mailquery.Result, error) {
	condition, args, err := s.ownerMailWhere(ctx, plan)
	if err != nil {
		return mailquery.Result{}, err
	}

	query := `WITH msgs AS (
		SELECT ` + instantSQL + ` AS instant
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + condition + `
	) SELECT instant FROM msgs`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return mailquery.Result{}, fmt.Errorf("store: time query: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var instant string
		if err := rows.Scan(&instant); err != nil {
			return mailquery.Result{}, fmt.Errorf("store: time row: %w", err)
		}
		t, ok := mail.Parse(instant)
		if !ok {
			continue
		}
		counts[timeBucket(plan.GroupBy, t.In(loc))]++
	}
	if err := rows.Err(); err != nil {
		return mailquery.Result{}, fmt.Errorf("store: time rows: %w", err)
	}
	keys := make([]string, 0, len(counts))
	total := 0
	for k, n := range counts {
		keys = append(keys, k)
		total += n
	}
	sort.Slice(keys, func(i, j int) bool {
		if plan.Order == mailquery.OrderAsc {
			return keys[i] < keys[j]
		}
		return keys[i] > keys[j]
	})
	if len(keys) > plan.Limit {
		keys = keys[:plan.Limit]
	}
	out := mailquery.Result{Kind: plan.Kind, Action: plan.Action, Total: total,
		Rows: make([]mailquery.Row, 0, len(keys)), Since: plan.Since, Until: plan.Until}
	for _, k := range keys {
		out.Rows = append(out.Rows, mailquery.Row{Key: k, Messages: counts[k]})
	}
	return out, nil
}

// appendContactFilter restricts message and time queries to one sender or
// recipient. It is the same counterpart rule as the contact aggregation:
// sent mail matches a To/Cc recipient, received mail matches the sender.
func appendContactFilter(where []string, args []any, contact string) ([]string, []any) {
	if contact == "" {
		return where, args
	}
	folded := mail.Fold(contact)
	where = append(where, `(
		(`+sentByOwnerWhere+` AND EXISTS (
			SELECT 1 FROM recipients r WHERE r.message_id = m.id AND r.kind IN ('to', 'cc')
				AND (instr(`+foldSQL+`(r.address_norm), ?) > 0 OR instr(`+foldSQL+`(COALESCE(r.name, '')), ?) > 0)
		)) OR
		(`+notSentByOwnerWhere+` AND (instr(`+foldSQL+`(m.sender_norm), ?) > 0 OR instr(`+foldSQL+`(COALESCE(m.sender_name, '')), ?) > 0))
	)`)
	args = append(args, folded, folded, folded, folded)
	return where, args
}

// instantRange appends the one time-window shape of the store: a lower and an
// upper bound, either optional. undatedInside says whether a message with no
// instant belongs to the window. Search excludes it (an undated message is not
// "before" a date), while a ranked mail query includes it, because the company
// profile documents that such a message still counts in Messages and Recent
// even though it cannot enter a month bucket. The two callers declare which
// side of that contract they speak instead of spelling their own SQL.
func instantRange(where []string, args []any, since, until string, undatedInside bool) ([]string, []any) {
	guard := ""
	if !undatedInside {
		guard = instantSQL + " != '' AND "
	}
	if since != "" {
		where = append(where, guard+instantSQL+" >= ?")
		args = append(args, since)
	}
	if until != "" {
		where = append(where, guard+instantSQL+" < ?")
		args = append(args, until)
	}
	return where, args
}

// appendTimeAndMessageFilters adds the shared filters to a query. The
// instant expression references the f (folders) alias, as ownerWhere does.
func appendTimeAndMessageFilters(where []string, args []any, plan mailquery.Plan) ([]string, []any) {
	where, args = instantRange(where, args, plan.Since, plan.Until, true)
	if plan.Unread != nil {
		if *plan.Unread {
			where = append(where, "m.is_read = 0")
		} else {
			where = append(where, "m.is_read = 1")
		}
	}
	if plan.HasAttachments != nil {
		if *plan.HasAttachments {
			where = append(where, "m.has_attachments = 1")
		} else {
			where = append(where, "m.has_attachments = 0")
		}
	}
	if plan.Text != "" {
		pattern := "%" + plan.Text + "%"
		where = append(where, "(m.subject LIKE ? OR m.body LIKE ?)")
		args = append(args, pattern, pattern)
	}
	return where, args
}

// timeBucket is the stable key of one local instant for a time plan.
func timeBucket(group mailquery.GroupBy, t time.Time) string {
	switch group {
	case mailquery.GroupDay:
		return t.Format("2006-01-02")
	case mailquery.GroupWeek:
		year, week := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	case mailquery.GroupMonth:
		return mail.MonthKey(t)
	case mailquery.GroupWeekday:
		return strings.ToLower(t.Weekday().String())
	default:
		return mail.MonthKey(t)
	}
}
