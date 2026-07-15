package deliverability

// Curated disposable / throwaway domains (extend via settings if needed).
// Not a full 100k dump — keeps the binary lean while blocking common traps.
var disposableDomains = map[string]struct{}{
	"mailinator.com": {}, "mailinator.net": {}, "guerrillamail.com": {}, "guerrillamail.org": {},
	"10minutemail.com": {}, "10minutemail.net": {}, "tempmail.com": {}, "temp-mail.org": {},
	"throwaway.email": {}, "yopmail.com": {}, "sharklasers.com": {}, "guerrillamailblock.com": {},
	"grr.la": {}, "discard.email": {}, "discardmail.com": {}, "trashmail.com": {},
	"trashmail.net": {}, "maildrop.cc": {}, "getnada.com": {}, "tempail.com": {},
	"fakeinbox.com": {}, "mailnesia.com": {}, "mintemail.com": {}, "moakt.com": {},
	"emailondeck.com": {}, "tempinbox.com": {}, "mailcatch.com": {}, "mytemp.email": {},
	"tmpmail.org": {}, "tmpmail.net": {}, "burnermail.io": {}, "guerrillamail.de": {},
	"spamgourmet.com": {}, "mailnull.com": {}, "spam4.me": {}, "trash-mail.com": {},
	"wegwerfmail.de": {}, "trashmail.me": {}, "tempmailo.com": {}, "dispostable.com": {},
	"mailforspam.com": {}, "spamfree24.org": {}, "jetable.org": {}, "kasmail.com": {},
	"spambox.us": {}, "mailimate.com": {}, "tempmails.net": {}, "emailtmp.com": {},
	"1secmail.com": {}, "1secmail.org": {}, "1secmail.net": {}, "guerrillamail.info": {},
	"pokemail.net": {}, "spam.la": {}, "binkmail.com": {}, "bobmail.info": {},
	"chammy.info": {}, "devnullmail.com": {}, "letthemeatspam.com": {}, "mailin8r.com": {},
	"mailinater.com": {}, "sogetthis.com": {}, "spamherelots.com": {}, "thisisnotmyrealemail.com": {},
	"anonymbox.com": {}, "trashymail.com": {}, "mt2009.com": {}, "thankyou2010.com": {},
	"trash2009.com": {}, "courriel.fr.nf": {}, "moncourrier.fr.nf": {},
	"nomail.xl.cx": {}, "mega.zik.dj": {}, "yopmail.fr": {}, "cool.fr.nf": {},
	"jetable.fr.nf": {}, "nospam.ze.tc": {}, "nomail2me.com": {}, "teleworm.us": {},
	"mailscrap.com": {}, "fakemailgenerator.com": {}, "emailfake.com": {}, "generator.email": {},
	"emltmp.com": {}, "tmpeml.com": {}, "tmpbox.net": {}, "dropmail.me": {},
	"inboxkitten.com": {}, "crazymailing.com": {}, "tempmailer.com": {}, "tempmailer.de": {},
	"filzmail.com": {}, "spamobox.com": {}, "tmpz.net": {}, "emailna.co": {},
	"mvrht.com": {}, "33mail.com": {}, "mailtemp.net": {}, "tempr.email": {},
	"discard.ml": {}, "trashmailbox.com": {}, "tmpnator.live": {}, "luxusmail.org": {},
	"mailbox.in.ua": {}, "mailpoof.com": {}, "gettempmail.com": {}, "tempmail.us": {},
	"spamdecoy.net": {}, "mailhazard.com": {}, "mailhazard.us": {}, "mailexpire.com": {},
	"tempomail.fr": {}, "tmail.ws": {}, "tmpmail.club": {}, "anonaddy.me": {},
}

// IsDisposable returns true for known throwaway domains (and common subdomains).
func IsDisposable(domain string) bool {
	domain = stringsToLower(domain)
	if _, ok := disposableDomains[domain]; ok {
		return true
	}
	// check parent (e.g. foo.mailinator.com)
	parts := splitDots(domain)
	for i := 1; i < len(parts)-1; i++ {
		parent := joinDots(parts[i:])
		if _, ok := disposableDomains[parent]; ok {
			return true
		}
	}
	return false
}

func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func splitDots(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func joinDots(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	n := len(parts) - 1
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			b = append(b, '.')
		}
		b = append(b, p...)
	}
	return string(b)
}
