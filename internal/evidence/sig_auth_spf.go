package evidence

func init() {
	register(authSignal("spf", "The SPF result (RFC 7208) the receiving domain recorded in its Authentication-Results header (RFC 8601): pass, fail, softfail, neutral, none, temperror, permerror, policy, other, or absent."))
}
