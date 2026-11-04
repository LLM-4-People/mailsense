package outlook

import (
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// resolver turns Exchange legacy DNs into SMTP addresses (PORTING §1):
// ExchangeUser.PrimarySmtpAddress, else the address entry's
// PR_SMTP_ADDRESS, else the DN itself. Each DN is resolved once per
// connector and cached (a failure is cached as "", meaning keep the DN).
type resolver struct {
	cache map[string]string
	stats *Stats
}

func newResolver(stats *Stats) *resolver { return &resolver{cache: map[string]string{}, stats: stats} }

// address resolves one sender-style reference. open returns the address
// entry to resolve through (nil when there is none). Only a lost session
// is an error; any other failure keeps the DN.
func (r *resolver) address(ref addressRef, open func() (Object, error)) (string, error) {
	if ref.smtp != "" {
		return ref.smtp, nil
	}
	if !mail.IsLegacyDN(ref.email) {
		return ref.email, nil
	}
	key := mail.NormalizeAddress(ref.email)
	if smtp, ok := r.cache[key]; ok {
		r.stats.add(func(s *Stats) { s.Resolve.CacheHits++ })
		return orDefault(smtp, ref.email), nil
	}
	smtp, err := smtpOfEntry(open)
	if unavailable(err) {
		return "", err
	}
	r.cache[key] = smtp
	r.stats.add(func(s *Stats) {
		if smtp != "" {
			s.Resolve.Resolved++
		} else {
			s.Resolve.Unresolved++
		}
	})
	return orDefault(smtp, ref.email), nil
}

// smtpOfEntry runs the address-entry part of the SMTP chain.
func smtpOfEntry(open func() (Object, error)) (string, error) {
	if open == nil {
		return "", nil
	}
	ae, err := open()
	if err != nil || ae == nil {
		return "", err
	}
	defer ae.Release()
	return smtpOfAddressEntry(ae)
}

// smtpOfAddressEntry reads the SMTP address of an address entry. The
// caller keeps ownership of ae.
func smtpOfAddressEntry(ae Object) (string, error) {
	if v, err := ae.Call("GetExchangeUser"); err == nil && v != nil {
		u := v.(Object)
		smtp, uerr := u.Get("PrimarySmtpAddress")
		u.Release()
		if s := asString(smtp); uerr == nil && s != "" {
			return s, nil
		}
	} else if unavailable(err) {
		return "", err
	}
	v, err := ae.Get("PropertyAccessor")
	if err != nil {
		return "", err
	}
	pa := v.(Object)
	defer pa.Release()
	smtp, err := pa.Call("GetProperty", propSMTPAddress)
	if unavailable(err) {
		return "", err
	}
	return asString(smtp), nil
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
