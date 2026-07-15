package deliverability

import (
	"net"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
)

var emailLocalRe = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+$`)

// ValidateSyntax returns false for obviously broken addresses.
func ValidateSyntax(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > 254 {
		return false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return false
	}
	email = addr.Address
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if strings.Contains(local, "..") || strings.Contains(domain, "..") {
		return false
	}
	if !emailLocalRe.MatchString(local) {
		return false
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
				return false
			}
		}
	}
	return true
}

// SplitEmail returns local and domain parts (lowercased domain).
func SplitEmail(email string) (local, domain string, ok bool) {
	email = strings.TrimSpace(strings.ToLower(email))
	if addr, err := mail.ParseAddress(email); err == nil {
		email = strings.ToLower(addr.Address)
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return "", "", false
	}
	return email[:at], email[at+1:], true
}

// DomainHasMailExchanger is true if MX, A, or AAAA exist.
func DomainHasMailExchanger(domain string) (bool, string) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" {
		return false, "empty domain"
	}
	if mx, err := net.LookupMX(domain); err == nil && len(mx) > 0 {
		return true, "mx"
	}
	if ips, err := net.LookupIP(domain); err == nil && len(ips) > 0 {
		return true, "a/aaaa"
	}
	return false, "no mx/a/aaaa"
}

var roleLocals = map[string]struct{}{
	"info": {}, "support": {}, "sales": {}, "office": {}, "hello": {},
	"admin": {}, "contact": {}, "help": {}, "billing": {}, "noreply": {},
	"no-reply": {}, "webmaster": {}, "postmaster": {}, "abuse": {},
	"marketing": {}, "team": {}, "hr": {}, "jobs": {}, "press": {},
}

// IsRoleBased flags high-risk shared mailboxes.
func IsRoleBased(local string) bool {
	local = strings.ToLower(strings.TrimSpace(local))
	_, ok := roleLocals[local]
	return ok
}

// Common typo → correction for major providers.
var typoMap = map[string]string{
	"gmial.com": "gmail.com", "gmai.com": "gmail.com", "gamil.com": "gmail.com",
	"gnail.com": "gmail.com", "gmail.co": "gmail.com", "gmail.cm": "gmail.com",
	"hotnail.com": "hotmail.com", "hotmai.com": "hotmail.com", "hotmial.com": "hotmail.com",
	"yahho.com": "yahoo.com", "yaho.com": "yahoo.com", "yahooo.com": "yahoo.com",
	"outlok.com": "outlook.com", "outloo.com": "outlook.com", "outlook.con": "outlook.com",
	"iclod.com": "icloud.com", "icoud.com": "icloud.com",
	"protonmai.com": "protonmail.com",
}

// SuggestTypo returns a corrected domain if a known typo is detected.
func SuggestTypo(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if c, ok := typoMap[domain]; ok {
		return c
	}
	return ""
}

// MXHost returns the primary MX host if any.
func MXHost(domain string) string {
	mx, err := net.LookupMX(domain)
	if err != nil || len(mx) == 0 {
		return ""
	}
	best := mx[0]
	for _, m := range mx[1:] {
		if m.Pref < best.Pref {
			best = m
		}
	}
	return strings.TrimSuffix(strings.ToLower(best.Host), ".")
}
