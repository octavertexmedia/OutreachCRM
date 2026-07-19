package deliverability

import (
	"strings"
	"time"
)

// Known major mailbox providers get high base domain scores.
var providerScores = map[string]float64{
	"gmail.com": 100, "googlemail.com": 100, "outlook.com": 96, "hotmail.com": 94,
	"live.com": 94, "msn.com": 90, "yahoo.com": 94, "ymail.com": 90,
	"icloud.com": 95, "me.com": 93, "mac.com": 93, "aol.com": 88,
	"protonmail.com": 92, "proton.me": 92, "zoho.com": 88, "fastmail.com": 91,
	"gmx.com": 85, "gmx.net": 85, "mail.com": 80, "comcast.net": 86,
	"att.net": 84, "verizon.net": 84, "sbcglobal.net": 82,
}

// ScoreDomain assigns layer-3 *recipient* domain reputation 0–100.
// Do not pass sending-domain SPF/DKIM/DMARC here — that polluted bounce heuristics.
// auth.Blacklisted is only used when the recipient domain itself was listed.
func ScoreDomain(domain string, auth AuthStatus, mxHost string) float64 {
	domain = strings.ToLower(domain)
	if s, ok := providerScores[domain]; ok {
		if auth.Blacklisted {
			return s - 25
		}
		return s
	}
	score := 70.0
	if auth.Blacklisted {
		score -= 40
	}
	tld := domain
	if i := strings.LastIndex(domain, "."); i >= 0 {
		tld = domain[i+1:]
	}
	switch tld {
	case "xyz", "top", "icu", "buzz", "click", "loan", "work":
		score -= 25
	case "info", "biz":
		score -= 8
	case "edu", "gov":
		score += 15
	}
	mxHost = strings.ToLower(mxHost)
	if strings.Contains(mxHost, "protection.outlook") || strings.Contains(mxHost, "google") ||
		strings.Contains(mxHost, "mimecast") || strings.Contains(mxHost, "ppops.net") {
		score += 5
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

// ScoreSendingAuth rates the sending domain's DNS auth posture 0–100 (dashboard / RequireAuth).
func ScoreSendingAuth(auth AuthStatus) float64 {
	score := 40.0
	if auth.SPF {
		score += 20
	}
	if auth.DKIM {
		score += 20
	}
	if auth.DMARC {
		score += 20
	}
	if auth.Blacklisted {
		score -= 50
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

// ScoreRecipient computes layer-4 recipient reputation 0–100.
func ScoreRecipient(h RecipientHistory) float64 {
	if h.HardBounces > 0 || h.Complaints > 0 {
		return 0
	}
	if h.Unsubscribes > 0 {
		return 5
	}
	score := 70.0
	if h.Sent == 0 {
		return 65 // unknown — neutral-cautious
	}
	eng := h.Opened + h.Clicked*2 + h.Replied*3
	score = 40 + float64(eng)*5 - float64(h.SoftBounces)*15
	if h.NeverEngaged && h.Sent >= 3 {
		score -= 25
	}
	if h.PurchasedList {
		score -= 20
	}
	if !h.LastEventAt.IsZero() && time.Since(h.LastEventAt) > 180*24*time.Hour {
		score -= 15
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

// PredictBounce is a lightweight heuristic stand-in for XGBoost (layer 5).
func PredictBounce(syntaxOK, hasMX, disposable, role bool, typo string, recip RecipientHistory, domainScore float64, smtpReject bool, smtpUnknown bool) float64 {
	if !syntaxOK {
		return 100
	}
	if disposable {
		return 98
	}
	if !hasMX {
		return 100
	}
	if smtpReject {
		return 99
	}
	if typo != "" {
		return 85
	}
	if recip.HardBounces > 0 {
		return 100
	}
	if recip.Complaints > 0 {
		return 95
	}
	p := 8.0
	if role {
		p += 18
	}
	if smtpUnknown {
		p += 5 // verification disabled / inconclusive
	}
	p += (100 - domainScore) * 0.25
	p += float64(recip.SoftBounces) * 12
	if recip.Sent >= 3 && recip.Opened+recip.Clicked+recip.Replied == 0 {
		p += 15
	}
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p
}

// SpamTrapRisk estimates layer-6 risk 0–100.
func SpamTrapRisk(h RecipientHistory, source string, domainScore float64) float64 {
	risk := 10.0
	src := strings.ToLower(source)
	if strings.Contains(src, "purchas") || strings.Contains(src, "scrape") || strings.Contains(src, "bought") {
		risk += 35
	}
	if h.PurchasedList {
		risk += 30
	}
	if h.Sent >= 2 && h.Opened+h.Clicked+h.Replied == 0 {
		risk += 25
	}
	if h.FirstSeenAt.IsZero() && h.Sent == 0 {
		risk += 5 // brand new, mild
	}
	if domainScore < 30 {
		risk += 20
	}
	if risk > 100 {
		risk = 100
	}
	return risk
}

// PredictEngagement estimates layer-7 open/click likelihood 0–100.
func PredictEngagement(h RecipientHistory, role bool, contentRisk float64) float64 {
	base := 45.0
	if h.Sent > 0 {
		eng := float64(h.Opened+h.Clicked+h.Replied) / float64(h.Sent)
		base = 20 + eng*70
	}
	if role {
		base -= 20
	}
	base -= contentRisk * 0.3
	if h.NeverEngaged && h.Sent >= 2 {
		base = 15
	}
	if base < 0 {
		base = 0
	}
	if base > 100 {
		base = 100
	}
	return base
}
