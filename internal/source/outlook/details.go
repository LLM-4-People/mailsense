package outlook

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// detailProps are read in one PropertyAccessor.GetProperties per item:
// what the table cannot give (headers, References; PidTagLocalCommitTime,
// which tables refuse with E_FAIL) and, in full, what the table caps at 255
// characters (Subject, sender name, ids, conversation index, categories,
// flag text). The setters write the matching mail.Details fields.
var detailProps = []struct {
	name string
	set  func(d *mail.Details, v any, t mail.Times)
}{
	{propTransportHeaders, func(d *mail.Details, v any, _ mail.Times) { d.Headers = asString(v) }},
	{propReferences, func(d *mail.Details, v any, _ mail.Times) { d.References = asString(v) }},
	{propLocalCommitTime, func(d *mail.Details, v any, t mail.Times) { d.LocalCommitTime = asTime(v, t) }},
	{propSubject, func(d *mail.Details, v any, _ mail.Times) { d.Subject = asString(v) }},
	{propSenderName, func(d *mail.Details, v any, _ mail.Times) { d.SenderName = asString(v) }},
	{propInternetMessageID, func(d *mail.Details, v any, _ mail.Times) { d.InternetMessageID = asString(v) }},
	{propInReplyTo, func(d *mail.Details, v any, _ mail.Times) { d.InReplyTo = asString(v) }},
	{propConversationIndex, func(d *mail.Details, v any, _ mail.Times) { d.ConversationIndex = asHex(v) }},
	{propKeywords, func(d *mail.Details, v any, _ mail.Times) { d.Categories = asList(v) }},
	{propFlagRequest, func(d *mail.Details, v any, _ mail.Times) { d.FlagRequest = asString(v) }},
}

// detailNames are the schema names of detailProps, in order.
var detailNames = func() []string {
	out := make([]string, len(detailProps))
	for i, p := range detailProps {
		out[i] = p.name
	}
	return out
}()

// recipientProps are read in one GetProperties per recipient: the row
// usually carries the SMTP address, so the address book is rarely needed.
var recipientProps = []string{propRecipientType, propDisplayName, propEmailAddress, propAddrType, propSMTPAddress}

var recipientKinds = map[int64]mail.RecipientKind{mapiTo: mail.To, mapiCc: mail.Cc, mapiBcc: mail.Bcc}

// ReadDetails opens one item by its long-term EntryID and reads what the
// table cannot give: the full plain-text body (cut at mail.body.max_chars),
// the HTMLBody alternative (cut at mail.body.html_max_chars), the transport
// headers (mail.headers.max_chars), References, and the recipients with SMTP
// addresses. The returned attachment list is complete or the read fails: a
// detail write replaces the message's stored rows, so a prefix is never
// reported as success. Reading never changes the item (Body and HTMLBody are
// property gets; UnRead is never touched). A failure of this item alone is
// a *source.ItemError; an unavailable Outlook (lost session, still busy,
// breaker) or a cancelled context is not. An item that made Outlook raise
// an exception (RPC_E_SERVERFAULT) is both: a quarantine item error (the
// syncer sets it aside, so the next run does not read it first again) that
// is also the lost session the breaker counts.
func (c *Connector) ReadDetails(ctx context.Context, ref source.ItemRef) (mail.Details, error) {
	var d mail.Details
	err := c.op(ctx, func(ns Object, s settings) error {
		var err error
		d, err = c.details(ns, ref, s)
		if cerr := ctx.Err(); cerr != nil { // cancelled while reading: report it, keep nothing
			return cerr
		}
		if err == nil || unavailable(err) && !faulted(err) {
			return err
		}
		c.stats.add(func(st *Stats) { st.Details.Failed++; st.Details.Failures[Code(err)]++ })
		return &source.ItemError{Code: Code(err), Err: err, Quarantine: faulted(err)}
	})
	return d, err
}

func (c *Connector) details(ns Object, ref source.ItemRef, s settings) (mail.Details, error) {
	var d mail.Details
	t0 := c.clock.Now()
	v, err := ns.Call("GetItemFromID", ref.ID, ref.StoreID)
	if err != nil {
		return d, err
	}
	item := v.(Object)
	defer item.Release()
	t1 := c.clock.Now()
	body, err := item.Get("Body")
	if absent(err) {
		c.stats.add(func(st *Stats) { st.Details.Absent["Body"]++ })
	} else if err != nil {
		return d, err
	}
	d.Body, d.BodyTruncated = mail.TruncateUTF16(asString(body), s.bodyMax)
	// HTMLBody is read separately, never inferred from Body: a text stub
	// must not hide the real HTML alternative. A member the item class lacks
	// is an empty known HTML state, not a failure.
	htmlBody, err := item.Get("HTMLBody")
	if absent(err) {
		c.stats.add(func(st *Stats) { st.Details.Absent["HTMLBody"]++ })
	} else if err != nil {
		return d, err
	}
	d.BodyHTMLKnown = true
	d.BodyHTML, d.BodyHTMLTruncated = mail.TruncateHTML(asString(htmlBody), s.htmlMax)
	if d.BodyHTMLTruncated {
		d.Alterations = append(d.Alterations, mail.Alteration{
			Code: mail.AltBodyHTMLTruncated, Detail: strconv.Itoa(mail.UTF16Len(asString(htmlBody)))})
	}
	t2 := c.clock.Now()
	if err := c.itemProps(item, &d, s); err != nil {
		return d, err
	}
	headers := d.Headers
	var hcut bool
	d.Headers, hcut = mail.TruncateUTF16(headers, s.headersMax)
	if hcut { // disclosed per message, not only in the statistics
		d.Alterations = append(d.Alterations, mail.Alteration{Code: mail.AltHeadersTruncated, Detail: strconv.Itoa(mail.UTF16Len(headers))})
	}
	t3 := c.clock.Now()
	fromRow, err := c.recipients(ns, item, &d)
	if err != nil {
		return d, err
	}
	if err := c.attachments(item, &d, s); err != nil {
		return d, fmt.Errorf("outlook: attachments of %s: %w", ref.ID, err)
	}
	c.mu.Lock()
	kind := c.kinds[ref.StoreID]
	c.mu.Unlock()
	c.recordDetails(kind, &d, [5]time.Time{t0, t1, t2, t3, c.clock.Now()}, fromRow, mail.UTF16Len(headers), hcut)
	return d, nil
}

// recordDetails adds one item's detail read to the stats; t holds the
// times before open, body, properties, recipients and after.
func (c *Connector) recordDetails(kind mail.StoreKind, d *mail.Details, t [5]time.Time, fromRow, headerChars int, headersCut bool) {
	c.stats.add(func(st *Stats) {
		x := &st.Details
		x.Items++
		x.Open += t[1].Sub(t[0])
		x.Body += t[2].Sub(t[1])
		x.Props += t[3].Sub(t[2])
		x.Recipients += t[4].Sub(t[3])
		x.RecipientCount += len(d.Recipients)
		x.RecipientSMTPRow += fromRow
		k := x.ByKind[kind]
		if k == nil {
			k = &KindCost{}
			x.ByKind[kind] = k
		}
		k.Items++
		k.Time += t[4].Sub(t[0])
		x.BodyChars += int64(mail.UTF16Len(d.Body))
		x.MaxHeaderChars = max(x.MaxHeaderChars, headerChars)
		if d.BodyTruncated {
			x.BodyTruncated++
		}
		if headersCut {
			x.HeadersTruncated++
		}
	})
}

// itemProps reads the detail properties (headers uncut).
func (c *Connector) itemProps(item Object, d *mail.Details, s settings) error {
	v, err := item.Get("PropertyAccessor")
	if err != nil {
		return err
	}
	pa := v.(Object)
	defer pa.Release()
	raw, err := pa.Call("GetProperties", detailNames)
	if err != nil {
		return err
	}
	vals, err := valuesOf(raw, detailNames)
	if err != nil {
		return err
	}
	for i, p := range detailProps {
		if !unset(vals[i]) {
			p.set(d, vals[i], s.times)
		}
	}
	return nil
}

// recipients reads To/Cc/Bcc; it returns how many SMTP addresses came
// straight from the recipient row.
func (c *Connector) recipients(ns Object, item Object, d *mail.Details) (int, error) {
	v, err := item.Get("Recipients")
	if absent(err) {
		c.stats.add(func(st *Stats) { st.Details.Absent["Recipients"]++ })
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rs := v.(Object)
	defer rs.Release()
	n, err := rs.Get("Count")
	if err != nil {
		return 0, err
	}
	fromRow := 0
	for i := int64(1); i <= asInt(n).N; i++ {
		v, err := rs.Call("Item", i)
		if err != nil {
			return fromRow, err
		}
		r := v.(Object)
		rec, smtpOnRow, ok, err := c.recipient(r)
		r.Release()
		if err != nil {
			return fromRow, err
		}
		if smtpOnRow {
			fromRow++
		}
		if ok {
			d.Recipients = append(d.Recipients, rec)
		}
	}
	return fromRow, nil
}

// attachments reads every attachment's metadata and, for an inline raster
// image within the configured budget, its decoded bytes. Outlook stores the
// MIME identity in hidden properties rather than on the Attachment object;
// PR_ATTACH_DATA_BIN is a byte SAFEARRAY, so no file is written and the
// read-only surface stays intact. A property a particular item class omits is
// an empty value, not a failed message.
//
// The list is all or nothing: a detail read succeeds only with every
// attachment of the item, because the store replaces the message's rows with
// this list. A member that cannot be read is an error with its index, never a
// shorter list: the message stays partial and a later pass retries, keeping
// the metadata and inline bytes stored before. Only an item class without an
// attachment collection is an empty list.
func (c *Connector) attachments(item Object, d *mail.Details, s settings) error {
	v, err := item.Get("Attachments")
	if absent(err) {
		// An item class without an attachment collection is an empty list.
		c.stats.add(func(st *Stats) { st.Details.Absent["Attachments"]++ })
		return nil
	}
	if err != nil {
		// A transient collection read must not replace stored metadata with
		// an empty list: the item stays partial and a later pass retries.
		return err
	}
	if v == nil {
		return fmt.Errorf("%w: Attachments", ErrNoObject)
	}
	as, ok := v.(Object)
	if !ok {
		return fmt.Errorf("outlook: Attachments is %T, not an object", v)
	}
	defer as.Release()
	n, err := as.Get("Count")
	if err != nil {
		return err
	}
	count := asInt(n).N
	total, kept := 0, 0
	for i := int64(1); i <= count; i++ {
		av, err := as.Call("Item", i)
		if err != nil {
			return fmt.Errorf("attachment %d of %d: %w", i, count, err)
		}
		if av == nil {
			return fmt.Errorf("attachment %d of %d: %w", i, count, ErrNoObject)
		}
		a, ok := av.(Object)
		if !ok {
			return fmt.Errorf("attachment %d of %d: Outlook returned %T, not an object", i, count, av)
		}
		att, dataBytes := c.attachment(a, s, total, kept)
		a.Release()
		d.Attachments = append(d.Attachments, att)
		if len(att.Data) > 0 {
			total += dataBytes
			kept++
		}
	}
	return nil
}

func (c *Connector) attachment(a Object, s settings, total, kept int) (mail.Attachment, int) {
	var att mail.Attachment
	name := asString(mustGet(a, "FileName"))
	size := asInt(mustGet(a, "Size")).N
	paV, err := a.Get("PropertyAccessor")
	if err != nil || paV == nil {
		// A class without an accessor still has a name and size.
		att = mail.Attachment{Name: name, ContentType: "application/octet-stream", Size: size}
		return att, 0
	}
	pa, ok := paV.(Object)
	if !ok {
		att = mail.Attachment{Name: name, ContentType: "application/octet-stream", Size: size}
		return att, 0
	}
	defer pa.Release()
	props := []string{propAttachContentID, propAttachContentLocation, propAttachMimeTag, propAttachFlags, propAttachSize}
	raw, err := pa.Call("GetProperties", props)
	if err != nil {
		att = mail.Attachment{Name: name, ContentType: "application/octet-stream", Size: size}
		return att, 0
	}
	vals, err := valuesOf(raw, props)
	if err != nil {
		att = mail.Attachment{Name: name, ContentType: "application/octet-stream", Size: size}
		return att, 0
	}
	cid := mail.NormalizeContentID(asString(vals[0]))
	loc := asString(vals[1])
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(asString(vals[2]), ";")[0]))
	if n := asInt(vals[4]).N; n > 0 {
		size = n
	}
	if name == "" {
		name = firstString(pa, propAttachLongFilename, propAttachFilename)
	}
	inline := cid != "" || loc != ""
	imageType := mail.InlineImageType(mimeType, name)
	if mimeType == "" {
		if imageType != "" {
			mimeType = imageType
		} else {
			mimeType = "application/octet-stream"
		}
	}
	att = mail.Attachment{Name: name, ContentType: mimeType, Size: size, Inline: inline,
		ContentID: cid, ContentLocation: loc}
	if !inline {
		return att, 0
	}
	// A part whose label and name identify nothing may still be a picture:
	// Outlook reports many inline images as application/octet-stream with a
	// .bin name. Read those bytes inside the same budget and identify them by
	// their magic number; a declared non-image type is never read.
	if imageType == "" && !mail.InlineImageTypeNeedsBytes(mimeType, name) {
		return att, 0
	}
	if s.inlinePartMax <= 0 || s.inlineTotalMax <= 0 || s.inlineParts <= 0 ||
		(size > 0 && (size > int64(s.inlinePartMax) || total+int(size) > s.inlineTotalMax)) || kept >= s.inlineParts {
		return att, 0
	}
	data, err := pa.Call("GetProperty", propAttachDataBin)
	if err != nil {
		return att, 0
	}
	bytes, ok := data.([]byte)
	if !ok || len(bytes) == 0 {
		return att, 0
	}
	if len(bytes) > s.inlinePartMax || total+len(bytes) > s.inlineTotalMax {
		return att, 0
	}
	if imageType == "" {
		imageType = mail.InlineImageTypeFromBytes(bytes)
		if imageType == "" {
			return att, 0
		}
	}
	att.Data, att.ContentType, att.Size = bytes, imageType, int64(len(bytes))
	return att, len(bytes)
}

func firstString(pa Object, names ...string) string {
	for _, name := range names {
		v, err := pa.Call("GetProperty", name)
		if err == nil {
			if s := strings.TrimSpace(asString(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

func (c *Connector) recipient(r Object) (mail.Recipient, bool, bool, error) {
	var rec mail.Recipient
	v, err := r.Get("PropertyAccessor")
	if err != nil {
		return rec, false, false, err
	}
	pa := v.(Object)
	raw, err := pa.Call("GetProperties", recipientProps)
	pa.Release()
	if err != nil {
		return rec, false, false, err
	}
	vals, err := valuesOf(raw, recipientProps)
	if err != nil {
		return rec, false, false, err
	}
	kind, ok := recipientKinds[asInt(vals[0]).N]
	if !ok {
		return rec, false, false, nil
	}
	rec.Kind, rec.Name = kind, asString(vals[1])
	ref := addressRef{smtp: asString(vals[4]), email: asString(vals[2])}
	var open func() (Object, error)
	if asString(vals[3]) == addrTypeExchange {
		open = func() (Object, error) {
			v, err := r.Get("AddressEntry")
			if err != nil || v == nil {
				return nil, err
			}
			return v.(Object), nil
		}
	}
	rec.Address, err = c.dns.address(ref, open)
	return rec, ref.smtp != "", true, err
}

// absent reports that an item has no such member at all: Outlook item
// classes differ (measured: 2,209 of 100,684 items on the target profile
// have no Recipients), and a member the class lacks is an empty value, not
// a failure of the item.
func absent(err error) bool {
	hr, ok := HResult(err)
	return ok && (hr == hrUnknownName || hr == hrNotFound)
}
