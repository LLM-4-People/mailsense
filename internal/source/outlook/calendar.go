package outlook

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// CalendarCollections lists the default calendar of every picked store.
// Additional calendar folders are not walked yet; the default folder holds
// the meetings the owner is invited to in that mailbox.
func (c *Connector) CalendarCollections(ctx context.Context) ([]source.CalendarCollection, error) {
	var out []source.CalendarCollection
	err := c.op(ctx, func(ns Object, _ settings) error {
		return c.eachPickedDefaultFolder(ctx, ns, calendarCollections, func(st StoreInfo, folder Object) error {
			entry, err := folder.Get("EntryID")
			if err != nil {
				return err
			}
			name, _ := folder.Get("Name") // a nameless calendar keeps its id
			if asString(entry) == "" {
				return nil
			}
			out = append(out, source.CalendarCollection{SourceID: c.id, StoreID: st.StoreID, StoreKey: st.Key,
				EntryID: asString(entry), Name: asString(name), Path: asString(name), Default: st.Default})
			return nil
		})
	})
	return out, err
}

// IncompleteCalendarError reports a calendar read that delivered every event
// it could read but skipped an appointment or a recurring series. It is not a
// transport or availability failure: the events already passed to the sink
// are valid, and the caller must merge them into the stored window instead of
// replacing it, so occurrences the partial read did not see survive.
//
// SkippedEvents counts appointments whose conversion failed (unreadable
// Start/End, invalid data, or no usable identity); SkippedSeries counts
// recurring masters whose rule, occurrence dates, or GetOccurrence lookup
// failed. A deleted occurrence that GetOccurrence reports as absent is not a
// skip, and neither is a moved or canceled exception.
type IncompleteCalendarError struct {
	SkippedEvents int
	SkippedSeries int
}

func (e *IncompleteCalendarError) Error() string {
	return fmt.Sprintf("outlook: calendar read incomplete: %d event(s), %d series skipped",
		e.SkippedEvents, e.SkippedSeries)
}

// CalendarSkipped is the completeness signal consumed by the syncer without
// depending on this package's type.
func (e *IncompleteCalendarError) CalendarSkipped() (events, series int) {
	return e.SkippedEvents, e.SkippedSeries
}

// SyncCalendar streams the occurrences of one calendar whose start falls in
// [since, until), the same bound restrictCalendar applies to ordinary
// appointments. It never asks Outlook to expand a whole series with
// IncludeRecurrences (that call can block for minutes): it restricts the
// window for ordinary appointments, then computes the occurrence dates of
// each recurring master and asks Outlook for each one with GetOccurrence.
// GetOccurrence is the only expansion member in the Outlook object model
// (there is no GetOccurrences) and it returns the exception that applies to
// that instance, so moved and canceled occurrences keep their real data.
//
// A read that skips an appointment or a series returns *IncompleteCalendarError
// after streaming every event it could read. The sink has received a valid
// partial result at that point; its caller must not treat the stored window as
// complete.
func (c *Connector) SyncCalendar(ctx context.Context, col source.CalendarCollection, since, until time.Time,
	sink source.CalendarSink) error {
	return c.op(ctx, func(ns Object, s settings) error {
		v, err := ns.Call("GetFolderFromID", col.EntryID, col.StoreID)
		if err != nil {
			return err
		}
		folder := v.(Object)
		defer folder.Release()
		iv, err := folder.Get("Items")
		if err != nil {
			return err
		}
		all := iv.(Object)
		defer all.Release()
		read, skippedEvents, skippedSeries := 0, 0, 0
		emit := func(it Object) (bool, error) {
			ev, err := c.appointment(it, col)
			it.Release()
			if err != nil {
				if unavailable(err) {
					return false, err
				}
				skippedEvents++
				return true, nil
			}
			if ev == nil {
				skippedEvents++
				return true, nil
			}
			if !ev.End.After(since) || !ev.Start.Before(until) {
				return true, nil
			}
			if err := sink.Event(ctx, *ev); err != nil {
				return false, err
			}
			read++
			if read%50 == 0 {
				sink.Progress(source.Progress{Done: int64(read), Unit: "items"})
			}
			return true, ctx.Err()
		}

		// Ordinary appointments whose start falls inside the window.
		inWindow, err := restrictCalendar(all, since, until)
		if err != nil {
			return err
		}
		cur, err := inWindow.Call("GetFirst")
		if err != nil {
			inWindow.Release()
			return err
		}
		for cur != nil {
			it := cur.(Object)
			recurring, rerr := isRecurring(it)
			if rerr != nil && unavailable(rerr) {
				it.Release()
				inWindow.Release()
				return rerr
			}
			if !recurring {
				ok, err := emit(it)
				if !ok || err != nil {
					if err == nil {
						err = ctx.Err()
					}
					inWindow.Release()
					return err
				}
			} else {
				// The master is expanded below, not emitted at its series start.
				it.Release()
			}
			if err := ctx.Err(); err != nil {
				inWindow.Release()
				return err
			}
			cur, err = inWindow.Call("GetNext")
			if err != nil {
				inWindow.Release()
				return err
			}
		}
		inWindow.Release()

		// Recurring series: expand exactly the requested range per master.
		masters, err := restrictRecurring(all)
		if err != nil {
			return err
		}
		defer masters.Release()
		cur, err = masters.Call("GetFirst")
		if err != nil {
			return err
		}
		for cur != nil {
			master := cur.(Object)
			skipped, err := c.occurrences(ctx, master, since, until, emit)
			if err != nil {
				master.Release()
				return err
			}
			if skipped {
				skippedSeries++
			}
			master.Release()
			if err := ctx.Err(); err != nil {
				return err
			}
			cur, err = masters.Call("GetNext")
			if err != nil {
				return err
			}
		}
		sink.Progress(source.Progress{Done: int64(read), Unit: "items"})
		if err := ctx.Err(); err != nil {
			return err
		}
		if skippedEvents > 0 || skippedSeries > 0 {
			return &IncompleteCalendarError{SkippedEvents: skippedEvents, SkippedSeries: skippedSeries}
		}
		return nil
	})
}

// occurrences expands one recurring master into the window and emits each
// occurrence. An unreadable series is skipped with one bounded warning and
// reports true, never turned into a fake occurrence and never logged per
// instance. The only errors returned end the whole operation (Outlook
// unavailable, the context canceled, or the sink failing).
func (c *Connector) occurrences(ctx context.Context, master Object, since, until time.Time,
	emit func(Object) (bool, error)) (bool, error) {
	ref := seriesRef(master)
	pv, err := master.Call("GetRecurrencePattern")
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return false, cerr
		}
		if unavailable(err) {
			return false, err
		}
		c.log.Warn("calendar: series skipped", "series_id", ref, "pattern", "unknown", "code", Code(err))
		return true, nil
	}
	pat := pv.(Object)
	defer pat.Release()
	p, err := readPattern(pat)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return false, cerr
		}
		if unavailable(err) {
			return false, err
		}
		c.log.Warn("calendar: series skipped", "series_id", ref, "pattern", p.name(), "reason", err.Error())
		return true, nil
	}
	dates, err := p.dates(wallClock(since), wallClock(until))
	if err != nil {
		c.log.Warn("calendar: series skipped", "series_id", ref, "pattern", p.name(), "reason", err.Error())
		return true, nil
	}
	for _, at := range dates {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ov, err := pat.Call("GetOccurrence", DateOf(at))
		if err != nil {
			if unavailable(err) {
				return false, err
			}
			// A deleted instance (or a date the series no longer holds) is
			// the documented answer, not a series failure: skip it.
			if occurrenceAbsent(err) {
				continue
			}
			c.log.Warn("calendar: series skipped", "series_id", ref, "pattern", p.name(), "code", Code(err))
			return true, nil
		}
		if ov == nil { // Nothing: the instance no longer exists
			continue
		}
		ok, err := emit(ov.(Object))
		if !ok || err != nil {
			if err == nil {
				err = fmt.Errorf("outlook: event emission stopped")
			}
			return false, err
		}
	}
	return false, nil
}

// occurrenceAbsent reports whether GetOccurrence answered "no appointment of
// that series exists on that date" (RecurrencePattern.GetOccurrence remarks).
// A deleted instance is reported as E_FAIL through the object model; some
// builds answer MAPI_E_NOT_FOUND or Nothing instead. Every other failure is a
// real series failure and is logged once, with its HRESULT.
func occurrenceAbsent(err error) bool {
	if err == nil {
		return true
	}
	hr, ok := HResult(err)
	return ok && (hr == hrNotFound || hr == hrFail || hr == hrException)
}

// readPattern reads one RecurrencePattern's rule, once per master, through
// the members the Outlook object model actually has. A property that is not
// valid for the pattern type (Outlook answers an error) is simply unset.
func readPattern(pat Object) (recurrencePattern, error) {
	var p recurrencePattern
	p.kind = recurUnknown
	kind, ok, err := patternInt(pat, "RecurrenceType")
	if err != nil {
		return p, err
	}
	if !ok {
		return p, unsupportedPattern(p.name(), "no recurrence type")
	}
	p.kind = kind
	switch p.kind {
	case recurDaily, recurWeekly, recurMonthly, recurMonthNth, recurYearly, recurYearNth:
	default:
		return p, unsupportedPattern(p.name(), "unsupported recurrence type %d", p.kind)
	}

	if err := patternIntField(pat, "Interval", &p.interval); err != nil {
		return p, err
	}
	if p.interval == 0 {
		p.interval = 1
	}
	startDate, ok, err := patternTime(pat, "PatternStartDate")
	if err != nil {
		return p, err
	}
	if !ok || startDate.Year() < 1601 || startDate.Year() >= noEndDateYear {
		return p, unsupportedPattern(p.name(), "no pattern start date")
	}
	hh, mm, ss := 0, 0, 0
	if startTime, ok, err := patternTime(pat, "StartTime"); err != nil {
		return p, err
	} else if ok {
		hh, mm, ss = startTime.Hour(), startTime.Minute(), startTime.Second()
	}
	p.start = time.Date(startDate.Year(), startDate.Month(), startDate.Day(), hh, mm, ss, 0, time.UTC)

	noEnd, _, err := patternBool(pat, "NoEndDate")
	if err != nil {
		return p, err
	}
	if !noEnd {
		if end, ok, err := patternTime(pat, "PatternEndDate"); err != nil {
			return p, err
		} else if ok && end.Year() >= 1601 && end.Year() < noEndDateYear {
			p.end = dateOnly(end)
		}
		if n, ok, err := patternInt(pat, "Occurrences"); err != nil {
			return p, err
		} else if ok && n > 0 {
			p.count = int(n)
		}
	}

	var fields []patternIntMember
	switch p.kind {
	case recurDaily, recurWeekly:
		fields = []patternIntMember{{"DayOfWeekMask", &p.mask}}
	case recurMonthly:
		fields = []patternIntMember{{"DayOfMonth", &p.day}}
	case recurMonthNth:
		fields = []patternIntMember{{"Instance", &p.instance}, {"DayOfWeekMask", &p.mask}}
	case recurYearly:
		fields = []patternIntMember{{"MonthOfYear", &p.month}, {"DayOfMonth", &p.day}}
	case recurYearNth:
		fields = []patternIntMember{{"MonthOfYear", &p.month}, {"Instance", &p.instance}, {"DayOfWeekMask", &p.mask}}
	}
	for _, f := range fields {
		if err := patternIntField(pat, f.name, f.dst); err != nil {
			return p, err
		}
	}
	return p, nil
}

// patternIntField reads an integer member that is valid for the pattern type.
func patternIntField(pat Object, name string, dst *int) error {
	n, ok, err := patternInt(pat, name)
	if err != nil {
		return err
	}
	if ok {
		*dst = int(n)
	}
	return nil
}

// patternIntMember is one integer property to read into a pattern field.
type patternIntMember struct {
	name string
	dst  *int
}

// patternInt reads an integer member. A property the pattern type does not
// expose is unset, not an error; an unavailable Outlook still is one.
func patternInt(pat Object, name string) (int64, bool, error) {
	v, err := pat.Get(name)
	if err != nil {
		if unavailable(err) {
			return 0, false, err
		}
		return 0, false, nil
	}
	n := asInt(v)
	if !n.Valid {
		return 0, false, nil
	}
	return n.N, true, nil
}

// patternTime reads a built-in date member of a RecurrencePattern. Its value
// is local wall clock, which recurrence enumeration needs as civil time: the
// result carries those fields in a UTC time.Time and is deliberately not
// converted to an instant (Date.LocalInstant is for emitted event instants).
func patternTime(pat Object, name string) (time.Time, bool, error) {
	v, err := pat.Get(name)
	if err != nil {
		if unavailable(err) {
			return time.Time{}, false, err
		}
		return time.Time{}, false, nil
	}
	d, ok := v.(Date)
	if !ok {
		return time.Time{}, false, nil
	}
	return d.UTC(), true, nil
}

func patternBool(pat Object, name string) (bool, bool, error) {
	v, err := pat.Get(name)
	if err != nil {
		if unavailable(err) {
			return false, false, err
		}
		return false, false, nil
	}
	b, ok := asBool(v)
	return b, ok, nil
}

// isRecurring reads the master flag without failing the whole calendar on
// a single unreadable item.
func isRecurring(it Object) (bool, error) {
	v, err := it.Get("IsRecurring")
	if err != nil {
		return false, err
	}
	b, _ := asBool(v)
	return b, nil
}

// restrictRecurring lists the recurring masters of the folder.
func restrictRecurring(items Object) (Object, error) {
	v, err := items.Call("Restrict", "[IsRecurring] = True")
	if err != nil {
		return nil, fmt.Errorf("outlook: list recurring appointments: %w", err)
	}
	return v.(Object), nil
}

// mustGet is a best-effort log field, never a correctness dependency.
func mustGet(it Object, name string) any {
	v, _ := it.Get(name)
	return v
}

// seriesRef is the log identity of a recurring series: a short digest of the
// master's stable EntryID. Its subject is owner content and never reaches a
// log; the id lets repeated failures of the same series be correlated.
func seriesRef(master Object) string {
	id := asString(mustGet(master, "EntryID"))
	if id == "" {
		return ""
	}
	return digest.Hex([]byte(id), 12)
}

// restrictCalendar returns the folder's Items restricted to [since, until)
// in the owner's local time. The original Items is not released here.
func restrictCalendar(items Object, since, until time.Time) (Object, error) {
	filter := fmt.Sprintf("[Start] >= '%s' AND [Start] < '%s'",
		since.In(time.Local).Format("01/02/2006 03:04 PM"),
		until.In(time.Local).Format("01/02/2006 03:04 PM"))
	v, err := items.Call("Restrict", filter)
	if err != nil {
		return nil, fmt.Errorf("outlook: restrict calendar to the sync window: %w", err)
	}
	return v.(Object), nil
}

// appointment converts one AppointmentItem occurrence. A canceled meeting
// is kept (the store knows MeetingCanceled) so the agenda can exclude it
// without losing the fact that it was canceled.
func (c *Connector) appointment(it Object, col source.CalendarCollection) (*calendar.Event, error) {
	var ev calendar.Event
	ev.SourceID, ev.StoreKey = col.SourceID, col.StoreKey
	fields := []struct {
		name string
		dst  *string
	}{
		{"EntryID", &ev.EntryID}, {"Subject", &ev.Subject}, {"Location", &ev.Location},
		{"Organizer", &ev.OrganizerName}, {"GlobalAppointmentID", &ev.GlobalID},
		{"CleanGlobalObjectId", &ev.CleanGlobalID}, {"LastModificationTime", &ev.LastModified},
	}
	for _, f := range fields {
		v, err := it.Get(f.name)
		if err != nil {
			if unavailable(err) {
				return nil, err
			}
			continue
		}
		*f.dst = asString(v)
	}
	start, err := calTime(it, "Start")
	if err != nil {
		return nil, err
	}
	end, err := calTime(it, "End")
	if err != nil {
		return nil, err
	}
	ev.Start, ev.End = start, end
	if ev.EntryID == "" || ev.Start.IsZero() || ev.End.IsZero() {
		return nil, nil
	}
	if v, err := it.Get("AllDayEvent"); err == nil {
		ev.AllDay, _ = asBool(v)
	}
	if v, err := it.Get("BusyStatus"); err == nil {
		ev.Busy = calendar.BusyStatus(asInt(v).N)
	}
	if v, err := it.Get("MeetingStatus"); err == nil {
		ev.Meeting = calendar.MeetingStatus(asInt(v).N)
	}
	if v, err := it.Get("ResponseStatus"); err == nil {
		ev.Response = calendar.ResponseStatus(asInt(v).N)
	}
	if v, err := it.Get("IsRecurring"); err == nil {
		ev.Recurring, _ = asBool(v)
	}
	if ev.CleanGlobalID == "" && ev.GlobalID != "" {
		ev.CleanGlobalID = ev.GlobalID
	}
	if ev.Recurring {
		ev.SeriesID = ev.CleanGlobalID
	}
	if v, err := it.Get("Body"); err == nil {
		ev.BodyPreview = previewText(asString(v), 400)
	}
	atts, err := c.attendees(it)
	if err != nil {
		if unavailable(err) {
			return nil, err
		}
		c.log.Debug("calendar: attendees unreadable", "event", ev.EntryID, "code", Code(err))
	} else if len(atts) == 0 {
		atts = fallbackAttendees(it)
	}
	ev.Attendees = atts
	if err := ev.Validate(); err != nil {
		return nil, err
	}
	return &ev, nil
}

// attendees reads the structured recipient list of a meeting.
func (c *Connector) attendees(it Object) ([]calendar.Attendee, error) {
	v, err := it.Get("Recipients")
	if err != nil {
		return nil, err
	}
	rs := v.(Object)
	defer rs.Release()
	n, err := rs.Get("Count")
	if err != nil {
		return nil, err
	}
	var out []calendar.Attendee
	for i := int64(1); i <= asInt(n).N; i++ {
		rv, err := rs.Call("Item", i)
		if err != nil {
			if unavailable(err) {
				return nil, err
			}
			continue
		}
		r := rv.(Object)
		att := calendar.Attendee{Kind: calendar.Required}
		if v, err := r.Get("Name"); err == nil {
			att.Name = asString(v)
		}
		if v, err := r.Get("Type"); err == nil {
			switch asInt(v).N {
			case 2:
				att.Kind = calendar.Optional
			case 3:
				att.Kind = calendar.Resource
			}
		}
		if a, err := recipientSMTP(r); err == nil {
			att.Address = a
		} else if unavailable(err) {
			r.Release()
			return nil, err
		}
		r.Release()
		if att.Address == "" && att.Name == "" {
			continue
		}
		out = append(out, att)
	}
	return out, nil
}

// recipientSMTP resolves one recipient to an SMTP address: the address
// entry first, then the raw Address property.
func recipientSMTP(r Object) (string, error) {
	v, err := r.Get("AddressEntry")
	if err == nil && v != nil {
		ae := v.(Object)
		smtp, serr := smtpOfAddressEntry(ae)
		ae.Release()
		if unavailable(serr) {
			return "", serr
		}
		if smtp != "" {
			return smtp, nil
		}
	}
	if v, err := r.Get("Address"); err == nil {
		a := asString(v)
		if contacts.Valid(a) {
			return a, nil
		}
	}
	return "", nil
}

// fallbackAttendees parses the delimited name lists when the structured
// recipient list is empty. Names are kept; a name that is already an
// address is used as one.
func fallbackAttendees(it Object) []calendar.Attendee {
	var out []calendar.Attendee
	for _, spec := range []struct {
		prop string
		kind calendar.AttendeeKind
	}{{"RequiredAttendees", calendar.Required}, {"OptionalAttendees", calendar.Optional}, {"Resources", calendar.Resource}} {
		v, err := it.Get(spec.prop)
		if err != nil {
			continue
		}
		for _, name := range strings.Split(asString(v), ";") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			att := calendar.Attendee{Kind: spec.kind, Name: name}
			if contacts.Valid(name) {
				att.Address = contacts.Normalize(name)
			}
			out = append(out, att)
		}
	}
	return out
}

// calTime reads a built-in date property (AppointmentItem.Start/End) as a UTC
// instant. Outlook returns those members as the owner's local wall clock, so
// the OADate is interpreted in the host zone (Date.LocalInstant), never as a
// UTC value.
func calTime(it Object, name string) (time.Time, error) {
	v, err := it.Get(name)
	if err != nil {
		return time.Time{}, err
	}
	d, ok := v.(Date)
	if !ok {
		return time.Time{}, nil
	}
	return d.LocalInstant(), nil
}

// previewText bounds a preview to n runes on a word boundary.
func previewText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \t"); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
