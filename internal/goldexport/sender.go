package goldexport

import "github.com/LLM-4-People/mailsense/internal/mail"

// Sender kinds (structural: headers and the owner's identity, never words).
const (
	SenderInternal  = "internal"  // same organisation as the mailbox owner (registrable domain, not a shared one)
	SenderAutomated = "automated" // machine-sent (mail.Automated)
	SenderExternal  = "external"
)

// SenderKind classifies one message's sender.
func SenderKind(m *mail.Message, ownerOrgs map[string]bool, domains mail.Domains) string {
	if mail.Automated(m) {
		return SenderAutomated
	}
	if d := domains.Of(m.SenderAddress); d != "" && ownerOrgs[d] {
		return SenderInternal
	}
	return SenderExternal
}
