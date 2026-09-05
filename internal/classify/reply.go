// Package classify turns a reply into a category and an intent score.
//
// Most of the work is already done upstream: Smartlead attaches its own
// lead_category to every statistics row, and its vocabulary separates a meeting
// request from an information request — exactly the distinction scoring needs.
// So the primary classifier is a mapping, not a model. Local rules exist only
// where Smartlead is unreliable, which is auto-reply detection.
package classify

import (
	"strings"
	"time"
)

// Version is the classifier revision. It is stored on every result so a
// prompt or rule change becomes "reclassify where version < current" instead of
// a full re-import. Bump it whenever behaviour here changes.
const Version = 1

// Category is the canonical reply classification.
type Category string

const (
	Interested    Category = "interested"      // positive, wants to proceed
	Warm          Category = "warm"            // engaged, non-committal
	FollowUpLater Category = "follow_up_later" // timing objection
	NotInterested Category = "not_interested"  // explicit no
	Remove        Category = "remove"          // opt-out request
	WrongContact  Category = "wrong_contact"   // not their remit
	OutOfOffice   Category = "out_of_office"   // temporary absence
	Automated     Category = "automated"       // system-generated
	Uncategorized Category = "uncategorized"   // needs the AI pass
)

// Ask is the specific request inside a positive reply. It is kept separate from
// Category because "send me pricing" and "let us meet" are both interested but
// are not worth the same.
type Ask string

const (
	AskNone    Ask = ""
	AskInfo    Ask = "information"
	AskMeeting Ask = "meeting"
)

// Result is one classification.
type Result struct {
	Category  Category
	Ask       Ask
	AutoReply bool
	Reason    string
	Version   int
}

// Scoreable reports whether this reply is evidence about a human's intent.
// Out-of-office and machine replies are not: counting them is how a prospect
// whose mailbox answered automatically ends up looking engaged.
func (r Result) Scoreable() bool {
	return !r.AutoReply && r.Category != OutOfOffice && r.Category != Automated
}

// MapSmartleadCategory converts Smartlead's own label into ours. Unknown or
// absent labels fall through to Uncategorized so the AI pass can pick them up
// rather than being silently scored as neutral.
//
// The live vocabulary, observed on a real account: Interested, Not Interested,
// Not Interested Now, Information Request, Meeting Request, Out Of Office,
// Wrong Person, Do Not Contact, Sender Originated Bounce, Uncategorizable by Ai.
func MapSmartleadCategory(raw string) (Category, Ask) {
	switch normalize(raw) {
	case "interested":
		return Interested, AskNone
	case "meetingrequest":
		return Interested, AskMeeting
	case "informationrequest":
		return Interested, AskInfo
	case "notinterestednow", "followuplater", "notnow":
		return FollowUpLater, AskNone
	case "notinterested":
		return NotInterested, AskNone
	case "donotcontact", "unsubscribed", "optout":
		return Remove, AskNone
	case "wrongperson", "wrongcontact":
		return WrongContact, AskNone
	case "outofoffice", "ooo":
		return OutOfOffice, AskNone
	case "senderoriginatedbounce", "bounce", "bounced":
		return Automated, AskNone
	case "":
		return Uncategorized, AskNone
	default:
		// Includes "Uncategorizable by Ai" and anything new Smartlead adds.
		return Uncategorized, AskNone
	}
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// autoReplyWindow is how soon after a send a reply is assumed to be machine
// generated. On a live account, 31 of 57 replies arrived inside two minutes
// while Smartlead's own ignore_reply flag caught only 17 — its absence proves
// nothing, so latency does the work.
const autoReplyWindow = 2 * time.Minute

var autoReplySubjects = []string{
	"out of office", "out-of-office", "automatic reply", "auto reply",
	"autoreply", "auto-reply", "away from", "on leave", "on vacation",
	"annual leave", "maternity leave", "paternity leave", "abwesenheit",
	"réponse automatique", "delivery status notification", "undeliverable",
	"mail delivery", "returned mail", "no longer with", "has left",
}

var autoReplyBodyHints = []string{
	"i am currently out of the office", "i'm currently out of the office",
	"currently away", "will be out of the office", "limited access to email",
	"this is an automated", "do not reply to this", "auto-generated",
	"thank you for your email. i am", "i will respond when i return",
}

// Input is everything known about one reply at classification time.
type Input struct {
	SmartleadCategory string
	IgnoreReply       bool // Smartlead's own auto-reply hint
	Subject           string
	Body              string
	SentAt            time.Time // when the outbound that prompted it went out
	RepliedAt         time.Time
}

// Classify decides what a reply is. Auto-reply detection runs first and wins:
// an out-of-office that says "sounds interesting, I'm away until Monday" is
// still an out-of-office, and scoring it as interest would be wrong.
func Classify(in Input) Result {
	res := Result{Version: Version}

	if auto, why := detectAutoReply(in); auto {
		res.AutoReply = true
		res.Reason = why
		// Trust Smartlead's label when it already agrees this is machine noise.
		if cat, _ := MapSmartleadCategory(in.SmartleadCategory); cat == OutOfOffice {
			res.Category = OutOfOffice
		} else {
			res.Category = Automated
		}
		return res
	}

	cat, ask := MapSmartleadCategory(in.SmartleadCategory)
	res.Category, res.Ask = cat, ask
	if cat == Uncategorized {
		res.Reason = "no usable smartlead category; needs the AI pass"
	} else {
		res.Reason = "smartlead category: " + strings.TrimSpace(in.SmartleadCategory)
	}
	return res
}

func detectAutoReply(in Input) (bool, string) {
	if in.IgnoreReply {
		return true, "smartlead ignore_reply"
	}
	// A reply that lands within seconds of the send was not typed by a person.
	if !in.SentAt.IsZero() && !in.RepliedAt.IsZero() {
		gap := in.RepliedAt.Sub(in.SentAt)
		if gap >= 0 && gap <= autoReplyWindow {
			return true, "replied within " + autoReplyWindow.String() + " of send"
		}
	}
	subject := strings.ToLower(in.Subject)
	for _, m := range autoReplySubjects {
		if strings.Contains(subject, m) {
			return true, "subject matches " + m
		}
	}
	body := strings.ToLower(in.Body)
	if len(body) > 2000 {
		body = body[:2000] // auto-reply boilerplate is always near the top
	}
	for _, m := range autoReplyBodyHints {
		if strings.Contains(body, m) {
			return true, "body matches auto-reply phrasing"
		}
	}
	return false, ""
}

// IntentPoints converts a classification into the high-water intent score used
// by prospect ranking. A reply that is not scoreable contributes nothing at
// all — not zero-as-a-signal, but genuinely no evidence.
func IntentPoints(r Result) (points int, reason string) {
	if !r.Scoreable() {
		return 0, ""
	}
	switch r.Category {
	case Interested:
		switch r.Ask {
		case AskMeeting:
			return 55, "asked for a meeting"
		case AskInfo:
			return 40, "asked for information"
		default:
			return 30, "replied positively"
		}
	case Warm:
		return 30, "engaged reply"
	case FollowUpLater:
		return 25, "timing objection — invited a later approach"
	case NotInterested:
		return -30, "said not interested"
	case WrongContact:
		return 0, "not their remit"
	case Remove:
		return 0, "opted out"
	default:
		return 0, ""
	}
}

// Suppresses reports whether a classification must stop all future contact.
// This is a legal and courtesy boundary, never a score to be outweighed.
func Suppresses(r Result) bool {
	return r.Category == Remove
}
