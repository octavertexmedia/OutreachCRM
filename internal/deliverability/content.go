package deliverability

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	urlRe   = regexp.MustCompile(`(?i)https?://[^\s<>"']+|www\.[^\s<>"']+`)
	shortRe = regexp.MustCompile(`(?i)\b(bit\.ly|t\.co|goo\.gl|tinyurl\.com|ow\.ly|is\.gd|buff\.ly|rebrand\.ly)\b`)
)

var spamPhrases = []string{
	"act now", "limited time", "click here", "buy now", "free money", "winner",
	"congratulations you won", "viagra", "casino", "crypto giveaway", "double your",
	"make money fast", "work from home", "risk free", "guaranteed", "no obligation",
	"urgent response", "final notice", "account suspended", "verify your account",
	"nigerian prince", "wire transfer", "100% free", "cash bonus",
}

// AnalyzeContent scores spam-likeliness of subject+body (0–100).
func AnalyzeContent(subject, body string) (score float64, reasons []string) {
	text := strings.ToLower(subject + "\n" + body)
	score = 0
	for _, p := range spamPhrases {
		if strings.Contains(text, p) {
			score += 12
			reasons = append(reasons, "spam phrase: "+p)
		}
	}
	urls := urlRe.FindAllString(text, -1)
	if len(urls) > 5 {
		score += 15
		reasons = append(reasons, "too many links")
	} else if len(urls) > 2 {
		score += 6
	}
	if shortRe.MatchString(text) {
		score += 18
		reasons = append(reasons, "shortened URL")
	}
	if strings.Count(subject, "!") > 2 {
		score += 8
		reasons = append(reasons, "excessive subject punctuation")
	}
	upper := 0
	letters := 0
	for _, r := range subject {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters > 8 && float64(upper)/float64(letters) > 0.6 {
		score += 14
		reasons = append(reasons, "all-caps subject")
	}
	if strings.Contains(strings.ToLower(body), "<html") && !strings.Contains(body, "\n") {
		// HTML-only with no plaintext alternative signal
		score += 5
		reasons = append(reasons, "html-heavy body")
	}
	imgHint := strings.Count(strings.ToLower(body), "<img")
	if imgHint >= 3 && len(strings.TrimSpace(stripTags(body))) < 80 {
		score += 12
		reasons = append(reasons, "image-heavy / little text")
	}
	if score > 100 {
		score = 100
	}
	return score, reasons
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
