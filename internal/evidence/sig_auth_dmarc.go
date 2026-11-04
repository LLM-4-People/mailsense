package evidence

func init() {
	register(authSignal("dmarc", "The DMARC result (RFC 7489) the receiving domain recorded in its Authentication-Results header (RFC 8601): pass, fail, none, temperror, permerror, other, or absent."))
}
