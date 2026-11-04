package api

import (
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// SenderView separates the identities of a message for the viewer:
//
//   - from_address is the address the message claims to come from: the
//     Exchange SentRepresenting address when the source reports one, else
//     the actual sender address.
//   - sender_address is the address that actually sent it.
//   - on_behalf_of says both identities exist, differ after normalization
//     and this is a received message. A sent item keeps the two addresses
//     visible but is never labelled "on behalf of".
//   - reply_to and return_path are the first values of those headers; an
//     empty string means the header is absent, never "same as From".
//   - authentication repeats the receiving domain's Authentication-Results
//     through the evidence auth parser. reported=false makes no pass/fail
//     claim, and no field here ever turns a display name into a verification.
type SenderView struct {
	FromAddress    string                   `json:"from_address"`
	SenderAddress  string                   `json:"sender_address"`
	OnBehalfOf     bool                     `json:"on_behalf_of"`
	ReplyTo        string                   `json:"reply_to"`
	ReturnPath     string                   `json:"return_path"`
	Authentication SenderAuthenticationView `json:"authentication"`
}

// SenderAuthenticationView is the reported authentication of the topmost
// Authentication-Results header. The values are the receiving domain's
// words (pass, fail, softfail, …), not this app's verdict.
type SenderAuthenticationView struct {
	Reported bool   `json:"reported"`
	SPF      string `json:"spf"`
	DKIM     string `json:"dkim"`
	DMARC    string `json:"dmarc"`
	Source   string `json:"source"`
}

// SenderOf derives the view from the stored message. Recipients stay on
// mail.Message.Recipients: the single-message view already carries the one
// stored list, so no second recipient copy is added here.
func SenderOf(m *mail.Message) SenderView {
	from := strings.TrimSpace(m.SentRepresentingAddress)
	if from == "" {
		from = strings.TrimSpace(m.SenderAddress)
	}
	sender := strings.TrimSpace(m.SenderAddress)
	onBehalf := from != "" && sender != "" &&
		mail.NormalizeAddress(from) != mail.NormalizeAddress(sender) &&
		// A draft is not a received message either: the viewer shows the
		// two identities, but the "on behalf of" label belongs to mail
		// that arrived.
		m.FolderRole != mail.RoleSent && m.FolderRole != mail.RoleDrafts
	cut := slices.ContainsFunc(m.Alterations, func(a mail.Alteration) bool { return a.Code == mail.AltHeadersTruncated })
	var headers mail.Headers
	if cut {
		headers = mail.ParseCutHeaders(m.Headers)
	} else {
		headers = mail.ParseHeaders(m.Headers)
	}
	report := evidence.ReportedAuthCut(m.Headers, cut)
	return SenderView{
		FromAddress:   from,
		SenderAddress: sender,
		OnBehalfOf:    onBehalf,
		ReplyTo:       headers.Value("Reply-To"),
		ReturnPath:    headers.Value("Return-Path"),
		Authentication: SenderAuthenticationView{
			Reported: report.Reported, SPF: report.SPF, DKIM: report.DKIM, DMARC: report.DMARC, Source: report.Source,
		},
	}
}
