package deliverability

import "strings"

// ClassifyISP maps an MX host / domain to a throttle bucket.
func ClassifyISP(domain, mxHost string) string {
	d := strings.ToLower(domain)
	mx := strings.ToLower(mxHost)
	switch {
	case strings.Contains(d, "gmail") || strings.Contains(mx, "google") || strings.Contains(mx, "gmail"):
		return "gmail"
	case strings.Contains(d, "outlook") || strings.Contains(d, "hotmail") || strings.Contains(d, "live.com") ||
		strings.Contains(mx, "outlook") || strings.Contains(mx, "protection.outlook"):
		return "microsoft"
	case strings.Contains(d, "yahoo") || strings.Contains(mx, "yahoodns") || strings.Contains(mx, "yahoo"):
		return "yahoo"
	case strings.Contains(d, "icloud") || strings.Contains(d, "me.com") || strings.Contains(mx, "icloud") || strings.Contains(mx, "apple"):
		return "apple"
	case strings.Contains(mx, "ppaspmx") || strings.Contains(mx, "proton"):
		return "proton"
	default:
		return "other"
	}
}

// ISPBurstLimit is max sends per rolling window per ISP (layer 10).
func ISPBurstLimit(isp string) int {
	switch isp {
	case "gmail":
		return 40
	case "microsoft":
		return 35
	case "yahoo":
		return 25
	case "apple":
		return 20
	default:
		return 50
	}
}

// ISPWindow is the throttle window duration hint in minutes.
func ISPWindowMinutes(isp string) int {
	switch isp {
	case "gmail", "microsoft":
		return 10
	case "yahoo":
		return 15
	default:
		return 10
	}
}
