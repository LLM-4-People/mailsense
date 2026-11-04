package outlook

import (
	"math"
	"time"
)

// Date is a VT_DATE: an OLE Automation date (days since 1899-12-30, the
// fraction is the time of day; for negative values the fraction counts
// forward from midnight of the integer day).
//
// Which clock it is on depends on how the value was asked for: Outlook's
// built-in date members return LOCAL wall clock, while values read by schema
// name (http://schemas.microsoft.com/mapi/…, in a Table or through a
// PropertyAccessor) are already UTC. A schema-name value uses UTC; a built-in
// member that denotes an instant uses LocalInstant. The legacy exporter
// shifted PropertyAccessor dates by the UTC offset a second time (PORTING §1).
type Date float64

var oleEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// UTC returns a schema-name date as the UTC instant it already is, rounded
// to the millisecond (VT_DATE doubles carry float noise: 12:00 can arrive
// as 11:59:59.9999). A built-in date member must not use this method for an
// instant: its value is local wall clock, not UTC.
func (d Date) UTC() time.Time {
	f := float64(d)
	days := math.Trunc(f)
	ms := math.Round(math.Abs(f-days) * 86400e3)
	return oleEpoch.AddDate(0, 0, int(days)).Add(time.Duration(ms) * time.Millisecond)
}

// LocalInstant returns a built-in date member (AppointmentItem.Start/End, …)
// as the UTC instant of the local wall clock it carries. The OADate fields are
// interpreted in time.Local, so a DST offset is applied for that date instead
// of the host's current fixed offset. A wall clock that does not exist or is
// ambiguous at a DST change resolves the way time.Date does.
func (d Date) LocalInstant() time.Time {
	civil := d.UTC()
	y, month, day := civil.Date()
	hour, minute, second := civil.Clock()
	return time.Date(y, month, day, hour, minute, second, civil.Nanosecond(), time.Local).UTC()
}

// DateOf is the inverse of UTC (for fakes and tests).
func DateOf(t time.Time) Date {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	days := math.Round(day.Sub(oleEpoch).Hours() / 24)
	frac := float64(t.Sub(day)) / float64(24*time.Hour)
	if days < 0 {
		return Date(days - frac)
	}
	return Date(days + frac)
}
