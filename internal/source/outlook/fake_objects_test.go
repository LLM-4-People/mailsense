package outlook

import (
	"encoding/hex"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

type fakeFolderRef struct {
	s *fakeStore
	f *fakeFolder
}

func (f *fakeObj) store(s *fakeStore, name string, args []any) (any, error) {
	switch name {
	case "DisplayName":
		return s.name, nil
	case "StoreID":
		return s.id, nil
	case "ExchangeStoreType":
		return s.typ, nil
	case "IsCachedExchange":
		return s.cached, nil
	case "FilePath":
		return s.filePath, nil
	case "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{store: s}), nil
	case "GetRootFolder":
		if s.offline {
			return nil, fail(hrNetworkError, name)
		}
		return f.o.obj("Folder", fakeFolderRef{s, s.root}), nil
	case "GetDefaultFolder":
		d := s.defaults[argInt(args[0])]
		if d == nil || s.offline {
			return nil, fail(hrNotFound, name)
		}
		return f.o.obj("Folder", fakeFolderRef{s, d}), nil
	}
	return nil, errNoMember
}

type fakeFolders struct {
	ref  fakeFolderRef
	next int
}

func (f *fakeObj) folder(r fakeFolderRef, name string, args []any) (any, error) {
	switch name {
	case "Folders":
		return f.o.obj("Folders", &fakeFolders{ref: r}), nil
	case "EntryID":
		if r.f.failEID != 0 {
			return nil, fail(r.f.failEID, name)
		}
		return r.f.eid, nil
	case "Name":
		return r.f.name, nil
	case "DefaultItemType":
		if r.f.itemType != 0 {
			return r.f.itemType, nil
		}
		switch r.f.class {
		case "IPF.Contact":
			return int64(2), nil
		case "IPF.Appointment":
			return int64(1), nil
		}
		return int64(0), nil
	case "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{folder: r.f}), nil
	case "GetTable":
		rows := slices.Clone(r.f.items)
		return f.o.obj("Table", &fakeTable{items: rows}), nil
	case "Items":
		return f.o.obj("Items", r.f), nil
	}
	return nil, errNoMember
}

// items is the Items collection view of a folder.
func (f *fakeObj) items(folder *fakeFolder, name string, args []any) (any, error) {
	next := func() (any, error) {
		if folder.itemIter >= len(folder.items) {
			return nil, nil
		}
		it := folder.items[folder.itemIter]
		folder.itemIter++
		return f.o.obj("Item", it), nil
	}
	switch name {
	case "Count":
		return int64(len(folder.items)), nil
	case "IncludeRecurrences":
		return folder.includeRecurrences, nil
	case "GetFirst":
		folder.itemIter = 0
		return next()
	case "GetNext":
		return next()
	case "Sort":
		sort.SliceStable(folder.items, func(i, j int) bool {
			a, _ := folder.items[i].props["Start"].(Date)
			b, _ := folder.items[j].props["Start"].(Date)
			if a != b {
				return a < b
			}
			return folder.items[i].props["EntryID"].(string) < folder.items[j].props["EntryID"].(string)
		})
		return nil, nil
	case "Restrict":
		filter, _ := args[0].(string)
		if strings.Contains(filter, "[IsRecurring] = True") {
			var recurring []*fakeItem
			for _, it := range folder.items {
				if b, _ := it.props["IsRecurring"].(bool); b {
					recurring = append(recurring, it)
				}
			}
			return f.o.obj("Items", &fakeFolder{items: recurring}), nil
		}
		// The connector applies the window itself, so the fake returns the
		// same view for a date filter.
		folder.itemIter = 0
		return f.o.obj("Items", folder), nil
	}
	return nil, errNoMember
}

func (f *fakeObj) folders(fs *fakeFolders, name string) (any, error) {
	switch name {
	case "GetFirst":
		fs.next = 0
	case "GetNext":
	default:
		return nil, errNoMember
	}
	if fs.next >= len(fs.ref.f.children) {
		return nil, nil
	}
	c := fs.ref.f.children[fs.next]
	fs.next++
	return f.o.obj("Folder", fakeFolderRef{fs.ref.s, c}), nil
}

type fakeRecurrence struct{ master *fakeItem }

// OlRecurrenceType / OlDaysOfWeek values, restated here so the fake is an
// independent oracle of the object model rather than a mirror of the
// connector's constants.
const (
	fakeDaily    = 0
	fakeWeekly   = 1
	fakeMonthly  = 2
	fakeMonthNth = 3
	fakeYearly   = 5
	fakeYearNth  = 6
)

// recurrence models the RecurrencePattern object of the real Outlook object
// model: it serves GetOccurrence and the pattern properties, and nothing
// else. GetOccurrences is not a member (calling it is recorded as misuse).
func (f *fakeObj) recurrence(r *fakeRecurrence, name string, args []any) (any, error) {
	switch name {
	case "GetOccurrence":
		return f.getOccurrence(r, args)
	case "RecurrenceType", "Interval", "PatternStartDate", "PatternEndDate", "NoEndDate",
		"Occurrences", "StartTime", "DayOfWeekMask", "DayOfMonth", "Instance", "MonthOfYear":
		if v, ok := r.master.props[name]; ok {
			return v, nil
		}
		return nil, fail(hrNotFound, name)
	}
	return nil, errNoMember
}

// getOccurrence answers one instance. A moved or canceled exception is
// returned as the item Outlook keeps for it; a deleted one answers E_FAIL
// (or Nothing when the test asks for that build's behaviour), and a date on
// which the series has no appointment answers MAPI_E_NOT_FOUND.
func (f *fakeObj) getOccurrence(r *fakeRecurrence, args []any) (any, error) {
	if len(args) != 1 {
		return nil, fail(hrInvalidArg, "GetOccurrence")
	}
	d, ok := args[0].(Date)
	if !ok {
		return nil, fail(hrInvalidArg, "GetOccurrence")
	}
	// GetOccurrence takes the owner's local wall clock; the connector sends
	// it as a UTC-tagged civil value (see wallClock), not as an instant.
	when := d.UTC()
	if r.master.failOccurrence != 0 {
		return nil, fail(r.master.failOccurrence, "GetOccurrence")
	}
	for _, ex := range r.master.exceptions {
		if !fakeSameDay(ex.original, when) {
			continue
		}
		switch {
		case ex.deleted && ex.nothing:
			return nil, nil
		case ex.deleted:
			return nil, fail(hrFail, "GetOccurrence")
		case ex.item != nil:
			return f.o.obj("Item", ex.item), nil
		}
	}
	if !r.matches(when) {
		return nil, fail(hrNotFound, "GetOccurrence")
	}
	return f.o.obj("Item", r.instance(when)), nil
}

// matches scans the series day by day from PatternStartDate, independently
// of the connector's recurrence engine, and honours the date and occurrence
// bounds.
func (r *fakeRecurrence) matches(when time.Time) bool {
	m := r.master
	start, ok := fakeCivil(m.props["PatternStartDate"])
	if !ok {
		return false
	}
	startDay, target := fakeDay(start), fakeDay(when)
	if target.Before(startDay) {
		return false
	}
	noEnd, _ := m.props["NoEndDate"].(bool)
	if !noEnd {
		if end, ok := fakeCivil(m.props["PatternEndDate"]); ok && target.After(fakeDay(end)) {
			return false
		}
	}
	limit := int64(0)
	if !noEnd {
		limit, _ = m.props["Occurrences"].(int64)
	}
	var count int64
	for d := startDay; !d.After(target); d = d.AddDate(0, 0, 1) {
		if !r.ruleMatches(d, startDay) {
			continue
		}
		count++
		if d.Equal(target) {
			return limit == 0 || count <= limit
		}
		if count > 100000 { // a corrupt pattern must not spin
			return false
		}
	}
	return false
}

func (r *fakeRecurrence) ruleMatches(d, startDay time.Time) bool {
	m := r.master
	kind, ok := m.props["RecurrenceType"].(int64)
	if !ok {
		return false
	}
	interval := fakeIntOr(m.props["Interval"], 1)
	if interval < 1 {
		return false
	}
	switch kind {
	case fakeDaily:
		if fakeDays(startDay, d)%interval != 0 {
			return false
		}
		mask := fakeIntOr(m.props["DayOfWeekMask"], 0)
		return mask == 0 || mask&fakeBit(d) != 0
	case fakeWeekly:
		weeks := fakeDays(fakeWeekStart(startDay), fakeWeekStart(d)) / 7
		return weeks%interval == 0 && fakeIntOr(m.props["DayOfWeekMask"], 0)&fakeBit(d) != 0
	case fakeMonthly:
		if fakeMonths(startDay, d)%interval != 0 {
			return false
		}
		day := fakeIntOr(m.props["DayOfMonth"], 0)
		return d.Day() == min(day, fakeDaysIn(d.Year(), d.Month()))
	case fakeMonthNth:
		return fakeMonths(startDay, d)%interval == 0 &&
			fakeNth(d, fakeIntOr(m.props["Instance"], 0), fakeIntOr(m.props["DayOfWeekMask"], 0))
	case fakeYearly:
		if (d.Year()-startDay.Year())%interval != 0 {
			return false
		}
		month, day := fakeIntOr(m.props["MonthOfYear"], 0), fakeIntOr(m.props["DayOfMonth"], 0)
		return int(d.Month()) == month && d.Day() == min(day, fakeDaysIn(d.Year(), d.Month()))
	case fakeYearNth:
		return (d.Year()-startDay.Year())%interval == 0 &&
			int(d.Month()) == fakeIntOr(m.props["MonthOfYear"], 0) &&
			fakeNth(d, fakeIntOr(m.props["Instance"], 0), fakeIntOr(m.props["DayOfWeekMask"], 0))
	}
	return false
}

// instance is the AppointmentItem Outlook returns for a pattern slot: the
// master's data at the requested date and StartTime, with its own EntryID
// and the pattern's duration.
func (r *fakeRecurrence) instance(when time.Time) *fakeItem {
	m := r.master
	props := maps.Clone(m.props)
	start := when
	if clock, ok := fakeCivil(m.props["StartTime"]); ok {
		start = time.Date(when.Year(), when.Month(), when.Day(), clock.Hour(), clock.Minute(), clock.Second(), 0, time.UTC)
	}
	dur := fakeIntOr(m.props["Duration"], 0)
	if dur <= 0 {
		s, sok := m.props["Start"].(Date)
		e, eok := m.props["End"].(Date)
		if sok && eok {
			dur = int(e.UTC().Sub(s.UTC()) / time.Minute)
		}
	}
	if dur <= 0 {
		dur = 30
	}
	props["Start"] = DateOf(start) // built-in members are local wall clock
	props["End"] = DateOf(start.Add(time.Duration(dur) * time.Minute))
	props["IsRecurring"] = true
	if id, ok := m.props["EntryID"].(string); ok {
		props["EntryID"] = id + "/" + start.Format("20060102T150405")
	}
	return &fakeItem{props: props, body: m.body, htmlBody: m.htmlBody,
		recips: slices.Clone(m.recips), atts: slices.Clone(m.atts)}
}

func fakeCivil(v any) (time.Time, bool) {
	d, ok := v.(Date)
	if !ok {
		return time.Time{}, false
	}
	return d.UTC(), true
}

func fakeIntOr(v any, def int) int {
	if n, ok := v.(int64); ok {
		return int(n)
	}
	return def
}

func fakeDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func fakeWeekStart(t time.Time) time.Time {
	return fakeDay(t).AddDate(0, 0, -int(t.Weekday()))
}

func fakeDays(a, b time.Time) int {
	return int((fakeDay(b).Unix() - fakeDay(a).Unix()) / 86400)
}

func fakeMonths(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()-a.Month())
}

func fakeDaysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func fakeBit(t time.Time) int { return 1 << uint(t.Weekday()) }

func fakeNth(d time.Time, instance, mask int) bool {
	if mask == 0 || instance < 1 || instance > 5 || fakeBit(d) != mask {
		return false
	}
	if instance == 5 { // last
		return d.AddDate(0, 0, 7).Month() != d.Month()
	}
	return (d.Day()-1)/7+1 == instance
}

func fakeSameDay(a, b time.Time) bool { return fakeDay(a).Equal(fakeDay(b)) }

type fakeTable struct {
	items []*fakeItem
	cols  []string
	pos   int
}

type fakeColumns struct{ t *fakeTable }

func (f *fakeObj) table(t *fakeTable, name string, args []any) (any, error) {
	switch name {
	case "Columns":
		return f.o.obj("Columns", &fakeColumns{t}), nil
	case "Sort":
		if f.o.sortErr != 0 {
			return nil, fail(f.o.sortErr, name)
		}
		desc := args[1].(bool)
		sort.SliceStable(t.items, func(i, j int) bool {
			a, b := t.items[i].props[propLastModified].(Date), t.items[j].props[propLastModified].(Date)
			if desc {
				return a > b
			}
			return a < b
		})
		return nil, nil
	case "GetArray":
		for _, c := range t.cols {
			if f.o.refuseCols[c] {
				return nil, &ComError{Code: 0x80004005, Member: name}
			}
		}
		n := argInt(args[0])
		var rows [][]any
		for ; n > 0 && t.pos < len(t.items); n-- {
			it := t.items[t.pos]
			t.pos++
			row := make([]any, len(t.cols))
			for i, c := range t.cols {
				row[i] = it.props[c]
				if s, ok := row[i].(string); ok && it.tableCap > 0 && len([]rune(s)) > it.tableCap {
					row[i] = string([]rune(s)[:it.tableCap])
				}
			}
			rows = append(rows, row)
		}
		if f.o.transpose && len(rows) > 0 {
			tr := make([][]any, len(t.cols))
			for c := range tr {
				for r := range rows {
					tr[c] = append(tr[c], rows[r][c])
				}
			}
			return tr, nil
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return rows, nil
	}
	return nil, errNoMember
}

func (f *fakeObj) columns(c *fakeColumns, name string, args []any) (any, error) {
	switch name {
	case "RemoveAll":
		c.t.cols = nil
		return nil, nil
	case "Add":
		c.t.cols = append(c.t.cols, args[0].(string))
		return f.o.obj("Column", nil), nil
	}
	return nil, errNoMember
}

type fakeAccessor struct {
	store  *fakeStore
	folder *fakeFolder
	item   *fakeItem
	recip  *fakeRecip
	att    *fakeAttachment
	entry  string
	user   string // an ExchangeUser's accessor
}

func (f *fakeObj) accessor(a *fakeAccessor, name string, args []any) (any, error) {
	get := func(prop string) any {
		switch {
		case a.store != nil:
			if prop == propMailboxOwnerEID && a.store.ownerEID != "" {
				b, _ := hex.DecodeString(a.store.ownerEID)
				return b
			}
		case a.folder != nil:
			return a.folder.prop(prop)
		case a.item != nil:
			if v, ok := a.item.props[prop]; ok {
				return v
			}
		case a.att != nil:
			if v, ok := a.att.props[prop]; ok {
				return v
			}
		case a.recip != nil:
			return a.recip.prop(prop)
		case a.user != "":
			if prop == propProxyAddresses && f.o.book[a.user].proxies != nil {
				out := []any{}
				for _, p := range f.o.book[a.user].proxies {
					out = append(out, p)
				}
				return out
			}
		default:
			if prop == propSMTPAddress && f.o.book[a.entry].propSMTP != "" {
				return f.o.book[a.entry].propSMTP
			}
		}
		return PropError(hrNotFound)
	}
	if a.folder != nil && a.folder.failProps != 0 {
		return nil, fail(a.folder.failProps, name)
	}
	switch name {
	case "GetProperty":
		v := get(args[0].(string))
		if pe, ok := v.(PropError); ok {
			return nil, fail(uint32(pe), name)
		}
		return v, nil
	case "GetProperties":
		names := args[0].([]string)
		if f.o.shortProps {
			names = names[1:]
		}
		out := make([]any, len(names))
		for i, n := range names {
			out[i] = get(n)
		}
		return out, nil
	}
	return nil, errNoMember
}

func (fl *fakeFolder) prop(p string) any {
	switch p {
	case propEntryID:
		b, _ := hex.DecodeString(fl.eid)
		return b
	case propDisplayName:
		return fl.name
	case propContainerClass:
		if fl.class == "" {
			return PropError(hrNotFound)
		}
		return fl.class
	case propSubfolders:
		return len(fl.children) > 0
	case propFolderType:
		if fl.search {
			return int64(folderTypeSearch)
		}
		return int64(1)
	case propAttrHidden:
		return fl.hidden
	case propContentCount:
		return int64(len(fl.items))
	case propContentUnread:
		return fl.unread
	case propCommitTimeMax:
		if fl.commitMax.IsZero() {
			return PropError(hrNotFound)
		}
		return DateOf(fl.commitMax)
	}
	return PropError(hrNotFound)
}

func (r *fakeRecip) prop(p string) any {
	switch p {
	case propRecipientType:
		return r.typ
	case propDisplayName:
		return r.name
	case propEmailAddress:
		return r.email
	case propAddrType:
		return r.addrType
	case propSMTPAddress:
		if r.smtp == "" {
			return PropError(hrNotFound)
		}
		return r.smtp
	}
	return PropError(hrNotFound)
}

func (f *fakeObj) item(it *fakeItem, name string) (any, error) {
	switch name {
	case "Body":
		if it.failBody != 0 {
			return nil, fail(it.failBody, name)
		}
		return it.body, nil
	case "HTMLBody":
		if it.failHTMLBody != 0 {
			return nil, fail(it.failHTMLBody, name)
		}
		if it.class == "post" {
			return nil, fail(hrUnknownName, name) // an item class without the member
		}
		return it.htmlBody, nil
	case "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{item: it}), nil
	case "Recipients":
		if it.class == "post" {
			return nil, fail(hrUnknownName, name) // GetIDsOfNames: the class has no such member
		}
		return f.o.obj("Recipients", &fakeRecipients{it.recips}), nil
	case "Attachments":
		return f.o.obj("Attachments", &fakeAttachments{it.atts}), nil
	}
	if name == "GetRecurrencePattern" {
		return f.o.obj("RecurrencePattern", &fakeRecurrence{master: it}), nil
	}
	if v, ok := it.props[name]; ok {
		return v, nil
	}
	// The guard already refused members the model does not list; an absent
	// property is a normal Outlook answer, not a model violation.
	return nil, fail(hrNotFound, name)
}

type fakeAttachments struct{ list []fakeAttachment }

func (f *fakeObj) attachments(as *fakeAttachments, name string, args []any) (any, error) {
	switch name {
	case "Count":
		return int64(len(as.list)), nil
	case "Item":
		i := argInt(args[0])
		if i < 1 || i > len(as.list) {
			return nil, fail(hrNotFound, name)
		}
		a := &as.list[i-1]
		if a.nothing {
			return nil, nil
		}
		if a.failRead != 0 {
			return nil, fail(a.failRead, name)
		}
		return f.o.obj("Attachment", a), nil
	}
	return nil, errNoMember
}

func (f *fakeObj) attachment(a *fakeAttachment, name string) (any, error) {
	switch name {
	case "FileName":
		return a.name, nil
	case "Size":
		return a.size, nil
	case "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{att: a}), nil
	}
	return nil, errNoMember
}

type fakeRecipients struct{ list []fakeRecip }

func (f *fakeObj) recipients(rs *fakeRecipients, name string, args []any) (any, error) {
	switch name {
	case "Count":
		return int64(len(rs.list)), nil
	case "Item":
		i := argInt(args[0])
		if i < 1 || i > len(rs.list) {
			return nil, fail(hrNotFound, name)
		}
		return f.o.obj("Recipient", rs.list[i-1]), nil
	}
	return nil, errNoMember
}

func (f *fakeObj) recipient(r fakeRecip, name string) (any, error) {
	switch name {
	case "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{recip: &r}), nil
	case "AddressEntry":
		if r.entry == "" {
			return nil, nil
		}
		return f.o.obj("AddressEntry", r.entry), nil
	}
	return nil, errNoMember
}

func (f *fakeObj) entry(key, name string, args []any) (any, error) {
	switch {
	case f.kind == "AddressEntry" && name == "GetExchangeUser":
		if code := f.o.book[key].failUser; code != 0 {
			return nil, fail(code, name)
		}
		if f.o.book[key].smtp == "" {
			return nil, nil
		}
		return f.o.obj("ExchangeUser", key), nil
	case f.kind == "AddressEntry" && name == "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{entry: key}), nil
	case f.kind == "ExchangeUser" && name == "PrimarySmtpAddress":
		return f.o.book[key].smtp, nil
	case f.kind == "ExchangeUser" && name == "Address":
		return f.o.book[key].dn, nil
	case f.kind == "ExchangeUser" && name == "PropertyAccessor":
		return f.o.obj("PropertyAccessor", &fakeAccessor{user: key}), nil
	}
	return nil, errNoMember
}
