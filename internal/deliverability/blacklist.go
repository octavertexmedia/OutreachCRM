package deliverability

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// Common DNSBLs (layer 12). Lookups are best-effort and time-bounded.
var dnsblZones = []string{
	"zen.spamhaus.org",
	"bl.spamcop.net",
	"b.barracudacentral.org",
	"dnsbl.sorbs.net",
	"psbl.surriel.com",
}

// CheckBlacklists reverses an IP and queries DNSBLs. Returns listed zones.
func CheckBlacklists(ctx context.Context, ip string) (listed bool, zones []string) {
	ip = strings.TrimSpace(ip)
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return false, nil
	}
	rev := reverseIPv4(parsed.To4())
	for _, zone := range dnsblZones {
		select {
		case <-ctx.Done():
			return listed, zones
		default:
		}
		q := rev + "." + zone
		resolver := &net.Resolver{}
		cctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		_, err := resolver.LookupHost(cctx, q)
		cancel()
		if err == nil {
			listed = true
			zones = append(zones, zone)
		}
	}
	return listed, zones
}

// ResolveSendingIPs resolves A records for a hostname (HELO / SMTP host).
func ResolveSendingIPs(host string) []string {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	var out []string
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out
}

func reverseIPv4(ip net.IP) string {
	if len(ip) != 4 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", ip[3], ip[2], ip[1], ip[0])
}
