package outlook

import (
	"errors"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Owner identity of a store, as Outlook reports it (no inference from
// Sent mail). Sources (Microsoft Learn, Outlook VBA reference; MS-OXOABK):
//   - the default store's owner is Namespace.CurrentUser.AddressEntry;
//     any other mailbox store names its owner in PR_MAILBOX_OWNER_ENTRYID
//     (0x661B0102) → Namespace.GetAddressEntryFromID;
//   - AddressEntry.GetExchangeUser → ExchangeUser.PrimarySmtpAddress (the
//     primary SMTP address) and ExchangeUser.Address (the legacy
//     Exchange DN that Exchange-internal mail carries as the sender);
//   - ExchangeUser.PropertyAccessor.GetProperty(PR_EMS_AB_PROXY_ADDRESSES,
//     0x800F101F, PtypMultipleString): every proxy address with its type
//     prefix ("SMTP:" primary, "smtp:" aliases, "X500:" former DNs; other
//     types such as SIP or SPO are not mail addresses);
//   - a non-Exchange entry's SMTP address is PR_SMTP_ADDRESS (0x39FE001F);
//   - Namespace.Accounts: an account whose DeliveryStore is the store adds
//     Account.SmtpAddress (POP/IMAP accounts, PST delivery stores).
// Verified against a live Outlook profile during development.

// proxyTypes maps the proxy address types that are mail addresses.
var proxyTypes = map[string]bool{"smtp": true, "x500": true}

// ownerRead collects a store owner's addresses as Outlook reports them
// and the reads that failed: one failed read (an offline address book,
// no rights) makes the identity incomplete, and an incomplete identity
// never replaces the recorded one (source.Mailbox.IdentityComplete).
type ownerRead struct {
	addrs  []string
	failed []string // "<read>:<HRESULT>"
}

func (r *ownerRead) add(list ...string) {
	for _, a := range list {
		if strings.TrimSpace(a) != "" {
			r.addrs = append(r.addrs, a)
		}
	}
}

// fail records a failed read; only a lost session is returned (it stops
// the discovery). A property the entry does not have (MAPI_E_NOT_FOUND)
// is no failure: there is nothing to read.
func (r *ownerRead) fail(what string, err error) error {
	if hr, _ := HResult(err); err == nil || hr == hrNotFound {
		return nil
	}
	if unavailable(err) {
		return err
	}
	r.failed = append(r.failed, what+":"+Code(err))
	return nil
}

// ownerEntry opens the address entry of a store's owner (nil when the
// store names none: a store without PR_MAILBOX_OWNER_ENTRYID, such as a
// PST, is complete without one).
func (c *Connector) ownerEntry(ns, store Object, isDefault bool, r *ownerRead) (Object, error) {
	if isDefault {
		v, err := ns.Get("CurrentUser")
		if err != nil || v == nil {
			return nil, r.fail("current_user", orMissing(err))
		}
		u := v.(Object)
		defer u.Release()
		ae, err := u.Get("AddressEntry")
		if err != nil || ae == nil {
			return nil, r.fail("current_user_entry", orMissing(err))
		}
		return ae.(Object), nil
	}
	v, err := store.Get("PropertyAccessor")
	if err != nil {
		return nil, r.fail("store_properties", err)
	}
	pa := v.(Object)
	eid, err := pa.Call("GetProperty", propMailboxOwnerEID)
	pa.Release()
	if hr, _ := HResult(err); hr == hrNotFound || (err == nil && unset(eid)) {
		return nil, nil
	}
	if err != nil {
		return nil, r.fail("owner_entry_id", err)
	}
	ae, err := ns.Call("GetAddressEntryFromID", asHex(eid))
	if err != nil || ae == nil {
		return nil, r.fail("owner_entry", orMissing(err))
	}
	return ae.(Object), nil
}

// errMissing marks an object Outlook answered with Nothing.
var errMissing = errors.New("outlook: Nothing")

func orMissing(err error) error {
	if err != nil {
		return err
	}
	return errMissing
}

// ownerAddresses reads the store owner's addresses, primary first: the
// owner entry's (Exchange user: primary SMTP, proxies, DN; else its SMTP
// address), then the SMTP addresses of the accounts delivering into the
// store. Only a lost session is an error; every other failed read is
// recorded in the result (incomplete), never dropped silently.
func (c *Connector) ownerAddresses(ns, store Object, storeID string, isDefault bool) (*ownerRead, error) {
	r := &ownerRead{}
	ae, err := c.ownerEntry(ns, store, isDefault, r)
	if err != nil {
		return nil, err
	}
	if ae != nil {
		err := entryAddresses(ae, r)
		ae.Release()
		if err != nil {
			return nil, err
		}
	}
	if err := accountAddresses(ns, storeID, r); err != nil {
		return nil, err
	}
	return r, nil
}

// entryAddresses reads one address entry's addresses.
func entryAddresses(ae Object, r *ownerRead) error {
	v, err := ae.Call("GetExchangeUser")
	if err != nil {
		return r.fail("exchange_user", err)
	}
	if v == nil { // not an Exchange user: its SMTP address
		smtp, err := entryProperty(ae, propSMTPAddress)
		r.add(smtp)
		return r.fail("smtp_address", err)
	}
	u := v.(Object)
	defer u.Release()
	primary, err := u.Get("PrimarySmtpAddress")
	if err := r.fail("primary_smtp", err); err != nil {
		return err
	}
	r.add(asString(primary))
	if err := proxyAddresses(u, r); err != nil {
		return err
	}
	dn, err := u.Get("Address")
	if err := r.fail("legacy_dn", err); err != nil {
		return err
	}
	if mail.IsLegacyDN(asString(dn)) {
		r.add(asString(dn))
	}
	return nil
}

// proxyAddresses reads PR_EMS_AB_PROXY_ADDRESSES of an Exchange user and
// keeps the mail addresses (SMTP and X500), primary first as Exchange
// lists it ("SMTP:" upper-case).
func proxyAddresses(u Object, r *ownerRead) error {
	v, err := u.Get("PropertyAccessor")
	if err != nil {
		return r.fail("user_properties", err)
	}
	pa := v.(Object)
	defer pa.Release()
	raw, err := pa.Call("GetProperty", propProxyAddresses)
	if err != nil {
		return r.fail("proxy_addresses", err)
	}
	var primary, rest []string
	for _, p := range asStrings(raw) {
		typ, addr, ok := strings.Cut(p, ":")
		if !ok || !proxyTypes[strings.ToLower(typ)] || strings.TrimSpace(addr) == "" {
			continue
		}
		if typ == "SMTP" {
			primary = append(primary, addr)
		} else {
			rest = append(rest, addr)
		}
	}
	r.add(append(primary, rest...)...)
	return nil
}

// entryProperty reads one string property of an address entry.
func entryProperty(ae Object, prop string) (string, error) {
	v, err := ae.Get("PropertyAccessor")
	if err != nil {
		return "", err
	}
	pa := v.(Object)
	defer pa.Release()
	s, err := pa.Call("GetProperty", prop)
	if err != nil {
		return "", err
	}
	return asString(s), nil
}

// accountAddresses adds the SMTP addresses of the profile's accounts
// that deliver into the store.
func accountAddresses(ns Object, storeID string, r *ownerRead) error {
	v, err := ns.Get("Accounts")
	if err != nil {
		return r.fail("accounts", err)
	}
	accounts := v.(Object)
	defer accounts.Release()
	n, err := accounts.Get("Count")
	if err != nil {
		return r.fail("accounts", err)
	}
	for i := int64(1); i <= asInt(n).N; i++ {
		a, err := accountAddress(accounts, i, storeID)
		if err := r.fail("account", err); err != nil {
			return err
		}
		r.add(a)
	}
	return nil
}

func accountAddress(accounts Object, i int64, storeID string) (string, error) {
	v, err := accounts.Call("Item", i)
	if err != nil {
		return "", err
	}
	acct := v.(Object)
	defer acct.Release()
	ds, err := acct.Get("DeliveryStore")
	if err != nil || ds == nil {
		return "", err // an account without a delivery store (none: not an error)
	}
	st := ds.(Object)
	id, err := st.Get("StoreID")
	st.Release()
	if err != nil || asString(id) != storeID {
		return "", err
	}
	smtp, err := acct.Get("SmtpAddress")
	return asString(smtp), err
}
