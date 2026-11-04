package evidence

func init() {
	register(authSignal("dkim", "The DKIM result (RFC 6376) the receiving domain recorded in its Authentication-Results header (RFC 8601): pass, fail, neutral, none, temperror, permerror, policy, other, or absent."))
}
