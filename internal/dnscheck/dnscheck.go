package dnscheck

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// Check looks up SPF / rough DKIM selector / DMARC TXT for a domain.
func Check(domain string) models.DomainCheck {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "@")
	dc := models.DomainCheck{Domain: domain, CheckedAt: time.Now().UTC()}
	if domain == "" {
		dc.Detail = "empty domain"
		return dc
	}
	var parts []string
	txts, err := net.LookupTXT(domain)
	if err != nil {
		parts = append(parts, "apex TXT: "+err.Error())
	}
	for _, t := range txts {
		lt := strings.ToLower(t)
		if strings.HasPrefix(lt, "v=spf1") {
			dc.SPF = true
			parts = append(parts, "SPF ok")
		}
	}
	dmarcTXT, err := net.LookupTXT("_dmarc." + domain)
	if err != nil {
		parts = append(parts, "DMARC: "+err.Error())
	} else {
		for _, t := range dmarcTXT {
			if strings.Contains(strings.ToLower(t), "v=dmarc1") {
				dc.DMARC = true
				parts = append(parts, "DMARC ok")
			}
		}
	}
	// Heuristic DKIM: check common selectors
	for _, sel := range []string{"default", "google", "selector1", "selector2", "k1"} {
		recs, err := net.LookupTXT(fmt.Sprintf("%s._domainkey.%s", sel, domain))
		if err != nil {
			continue
		}
		for _, t := range recs {
			if strings.Contains(strings.ToLower(t), "v=dkim1") || strings.Contains(t, "p=") {
				dc.DKIM = true
				parts = append(parts, "DKIM via "+sel)
				break
			}
		}
		if dc.DKIM {
			break
		}
	}
	if !dc.DKIM {
		parts = append(parts, "DKIM not found on common selectors")
	}
	dc.Detail = strings.Join(parts, "; ")
	return dc
}
