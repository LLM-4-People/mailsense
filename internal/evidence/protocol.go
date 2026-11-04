package evidence

// Protocol constants the header signals read: header names and the
// values their standards define (never words of a language).
const (
	hdrListID          = "List-Id"                        // RFC 2919
	hdrListUnsubscribe = "List-Unsubscribe"               // RFC 2369
	hdrListUnsubPost   = "List-Unsubscribe-Post"          // RFC 8058
	oneClick           = "List-Unsubscribe=One-Click"     // RFC 8058 §3.1: the only value it defines
	hdrPrecedence      = "Precedence"                     // de facto (RFC 3834 §3.1.8 notes it)
	hdrAutoSubmitted   = "Auto-Submitted"                 // RFC 3834 §5, RFC 5436 (auto-notified)
	hdrAutoSuppress    = "X-Auto-Response-Suppress"       // MS-OXCMAIL §2.2.3.2.14
	hdrMailer          = "X-Mailer"                       // de facto
	hdrUserAgent       = "User-Agent"                     // RFC 5536 §3.2.13 (news), used by mail clients
	hdrAuthResults     = "Authentication-Results"         // RFC 8601
	hdrSCL             = "X-MS-Exchange-Organization-SCL" // Exchange spam confidence level
	hdrSpamFlag        = "X-Spam-Flag"                    // SpamAssassin
	hdrReceived        = "Received"                       // RFC 5321 §4.4
	hdrReturnPath      = "Return-Path"                    // RFC 5321 §4.4
	hdrReplyTo         = "Reply-To"                       // RFC 5322 §3.6.2
	hdrImportance      = "Importance"                     // RFC 2156 §5.3
	hdrPriority        = "X-Priority"                     // de facto: 1 (highest) .. 5 (lowest)
	hdrNotificationTo  = "Disposition-Notification-To"    // RFC 8098 §2.1
)

// Vocabularies of the header signals.
var (
	precedenceValues    = []string{"none", "bulk", "list", "junk", "other"}
	autoSubmittedValues = []string{"none", "no", "auto-generated", "auto-replied", "auto-notified", "other"}
	// RFC 8601 §2.7 (and RFC 7208 §2.6 for SPF's softfail), lower case;
	// "absent" when the method is not in the header.
	authValues = []string{"absent", "pass", "fail", "softfail", "neutral", "none", "temperror", "permerror", "policy", "other"}
	// importanceValues: RFC 2156 low/normal/high (MAPI PidTagImportance
	// 0/1/2, in that order), then other (a header value outside them).
	importanceValues = []string{"low", "normal", "high", "other"}
	// MS-OXOMSG PidTagLastVerbExecuted: 0 open (EXCHIVERB_OPEN), 102 reply
	// to sender, 103 reply to all, 104 forward; any other verb (print,
	// meeting answers…) is other.
	lastVerbs = map[int64]string{0: "open", 102: "reply", 103: "reply_all", 104: "forward"}
	// MS-OXOFLAG PidTagFlagStatus: 0 none, 1 complete, 2 flagged.
	flagStates = map[int64]string{0: "none", 1: "complete", 2: "marked"}
)
