// Package identity decides when two prospect records describe the same person.
//
// The asymmetry that shapes everything here: a duplicate is an inconvenience,
// a false merge is data loss. Merging two people mixes their conversation
// history irreversibly in the reader's mind even though the rows can be
// unpicked, and the evidence needed to tell them apart is exactly what got
// blended. So the bar for merging automatically is evidence that cannot
// reasonably coincide, and everything short of that goes to a human.
package identity

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Signal weights. These are starting values, meant to be tuned after watching a
// few hundred real decisions rather than argued about in advance.
const (
	WeightEmail       = 100 // definitive
	WeightLinkedIn    = 100 // definitive
	WeightPhone       = 60  // shared lines exist, but are rare
	WeightLocalPart   = 35  // j.smith@ is weaker than johnathan.smith@
	WeightDomainCited = 30  // they told you where they went
	WeightName        = 25  // scaled down for common names
	WeightBounceMove  = 20  // old address died as a new one appeared
	WeightNoOverlap   = 15  // employment windows are consistent with a move
	VetoDifferentName = -100
)

// Decision thresholds.
const (
	AutoMergeAt = 100
	ReviewAt    = 50
)

// Decision is what to do with a scored pair.
type Decision string

const (
	DecisionMerge    Decision = "merge"    // conclusive; merge without asking
	DecisionReview   Decision = "review"   // probable; queue for a human
	DecisionSeparate Decision = "separate" // leave alone
)

// Record is one side of a comparison — the facts known about a person.
type Record struct {
	PersonID int64
	Name     string
	Email    string
	Phone    string
	LinkedIn string

	Company string
	Title   string

	// Employment window. Nil ValidTo means the role is current.
	ValidFrom *time.Time
	ValidTo   *time.Time

	// Bounced marks an address that stopped delivering — weak on its own,
	// because Smartlead does not expose whether a bounce was hard or soft.
	Bounced bool

	// DomainsSeenInThreads holds domains mentioned in this person's replies.
	// If the other record's domain appears here, they told us where they went.
	DomainsSeenInThreads []string
}

// Match is the outcome of comparing two records.
type Match struct {
	Score    int
	Signals  []string
	Decision Decision
}

var (
	// Apostrophes are removed rather than split on, so O'Smith and OSmith
	// compare equal. Every other separator becomes a space, so Mary-Jane and
	// "Mary Jane" do too.
	apostrophes = regexp.MustCompile(`['\x60\x{2018}\x{2019}]`)
	nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)
)

// NormalizeName reduces a display name to a comparable form: lowercase, no
// punctuation, common honorifics and suffixes removed, words sorted so
// "Smith, John" and "John Smith" agree.
func NormalizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = apostrophes.ReplaceAllString(n, "")
	n = nonAlphaNum.ReplaceAllString(n, " ")
	fields := strings.Fields(n)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if isNameNoise(f) {
			continue
		}
		kept = append(kept, f)
	}
	// Smartlead exports occasionally duplicate the surname ("Easterday
	// Easterday"), so collapse repeats before comparing.
	sort.Strings(kept)
	out := make([]string, 0, len(kept))
	for i, f := range kept {
		if i > 0 && f == kept[i-1] {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

func isNameNoise(w string) bool {
	switch w {
	case "mr", "mrs", "ms", "miss", "dr", "prof", "sir",
		"jr", "sr", "ii", "iii", "iv", "phd", "mba", "cpa":
		return true
	}
	return len(w) == 0
}

// NormalizePhone reduces a number to its digits, keeping the last 10 so that
// +91 98765 43210 and 09876543210 compare equal. Numbers too short to identify
// anyone return "" and never match.
func NormalizePhone(phone string) string {
	var digits strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) < 10 {
		return ""
	}
	return d[len(d)-10:]
}

// localPart returns the part of an address before the @, lowercased.
func localPart(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if at := strings.LastIndex(e, "@"); at > 0 {
		return e[:at]
	}
	return ""
}

func domainPart(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if at := strings.LastIndex(e, "@"); at >= 0 && at+1 < len(e) {
		return e[at+1:]
	}
	return ""
}

// distinctiveLocalPart reports whether a local part is specific enough to carry
// weight. "info", "contact" and "j" appear at thousands of unrelated companies
// and would otherwise merge strangers wholesale.
func distinctiveLocalPart(lp string) bool {
	if len(lp) < 4 {
		return false
	}
	switch lp {
	case "info", "hello", "contact", "sales", "admin", "support", "team",
		"office", "mail", "email", "enquiries", "inquiries", "hi", "help",
		"marketing", "press", "billing", "accounts", "careers", "jobs",
		"noreply", "no-reply", "donotreply":
		return false
	}
	return true
}

// Score compares two records and returns the evidence for them being the same
// person, along with what to do about it.
func Score(a, b Record) Match {
	var score int
	var signals []string

	add := func(points int, name string) {
		score += points
		signals = append(signals, name)
	}

	nameA, nameB := NormalizeName(a.Name), NormalizeName(b.Name)
	namesKnown := nameA != "" && nameB != ""
	namesAgree := namesKnown && nameA == nameB

	// Definitive signals first.
	emailA, emailB := strings.ToLower(strings.TrimSpace(a.Email)), strings.ToLower(strings.TrimSpace(b.Email))
	if emailA != "" && emailA == emailB {
		add(WeightEmail, "email")
	}
	if a.LinkedIn != "" && strings.EqualFold(strings.TrimSpace(a.LinkedIn), strings.TrimSpace(b.LinkedIn)) {
		add(WeightLinkedIn, "linkedin")
	}

	// A different name vetoes everything else. Two people at one company can
	// share a phone number or a mailbox naming convention; they do not share
	// an identity.
	if namesKnown && !namesAgree {
		signals = append(signals, "name_conflict")
		return Match{Score: VetoDifferentName, Signals: signals, Decision: DecisionSeparate}
	}

	if namesAgree {
		add(WeightName, "name")
	}

	phoneA, phoneB := NormalizePhone(a.Phone), NormalizePhone(b.Phone)
	if phoneA != "" && phoneA == phoneB {
		add(WeightPhone, "phone")
	}

	lpA, lpB := localPart(a.Email), localPart(b.Email)
	domA, domB := domainPart(a.Email), domainPart(b.Email)
	if lpA != "" && lpA == lpB && domA != domB && distinctiveLocalPart(lpA) {
		add(WeightLocalPart, "local_part")
	}

	if domainCited(a.DomainsSeenInThreads, domB) || domainCited(b.DomainsSeenInThreads, domA) {
		add(WeightDomainCited, "domain_cited")
	}

	if (a.Bounced || b.Bounced) && domA != domB && domA != "" && domB != "" {
		add(WeightBounceMove, "bounce_then_move")
	}

	if employmentDisjoint(a, b) {
		add(WeightNoOverlap, "employment_disjoint")
	}

	return Match{Score: score, Signals: signals, Decision: decide(score)}
}

func decide(score int) Decision {
	switch {
	case score >= AutoMergeAt:
		return DecisionMerge
	case score >= ReviewAt:
		return DecisionReview
	default:
		return DecisionSeparate
	}
}

func domainCited(seen []string, domain string) bool {
	if domain == "" {
		return false
	}
	for _, d := range seen {
		if strings.EqualFold(strings.TrimSpace(d), domain) {
			return true
		}
	}
	return false
}

// employmentDisjoint reports whether two roles could not have been held at the
// same time — consistent with one person moving, rather than two people.
// Unknown dates are not evidence either way.
func employmentDisjoint(a, b Record) bool {
	if a.Company == "" || b.Company == "" || strings.EqualFold(a.Company, b.Company) {
		return false
	}
	if a.ValidFrom == nil || b.ValidFrom == nil {
		return false
	}
	// Order them so x starts first.
	x, y := a, b
	if y.ValidFrom.Before(*x.ValidFrom) {
		x, y = y, x
	}
	// x must have ended before y began.
	return x.ValidTo != nil && !x.ValidTo.After(*y.ValidFrom)
}

// BlockKeys returns the keys under which a record should be indexed for
// candidate generation. Comparing all pairs across 65,000 people is two billion
// comparisons; blocking limits comparison to records that share at least one
// strong-ish key, which is where every real match lives anyway.
func BlockKeys(r Record) []string {
	var keys []string
	if n := NormalizeName(r.Name); n != "" {
		keys = append(keys, "name:"+n)
	}
	if lp := localPart(r.Email); distinctiveLocalPart(lp) {
		keys = append(keys, "local:"+lp)
	}
	if p := NormalizePhone(r.Phone); p != "" {
		keys = append(keys, "phone:"+p)
	}
	if r.LinkedIn != "" {
		keys = append(keys, "li:"+strings.ToLower(strings.TrimSpace(r.LinkedIn)))
	}
	return keys
}
