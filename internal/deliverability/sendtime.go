package deliverability

import (
	"hash/fnv"
	"time"
)

// PreferredHour returns a stable preferred local hour (8–18) for the recipient (layer 8).
func PreferredHour(email string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(stringsToLower(email)))
	return 8 + int(h.Sum32()%11) // 8..18
}

// NextSendSlot delays until the preferred hour in UTC (simple; campaigns may have their own TZ).
func NextSendSlot(email string, now time.Time) time.Time {
	hour := PreferredHour(email)
	cand := time.Date(now.Year(), now.Month(), now.Day(), hour, int(hashMod(email, 60)), int(hashMod(email+"s", 60)), 0, time.UTC)
	if !cand.After(now) {
		cand = cand.Add(24 * time.Hour)
	}
	// If within ±2h of preferred, send now
	diff := cand.Sub(now)
	if diff > 22*time.Hour {
		// wrapped — preferred already passed today recently
		prev := cand.Add(-24 * time.Hour)
		if now.Sub(prev) < 2*time.Hour {
			return now
		}
	}
	if cand.Sub(now) < 2*time.Hour {
		return now
	}
	// Only delay if more than 3 hours away; otherwise send
	if cand.Sub(now) > 3*time.Hour && cand.Sub(now) < 14*time.Hour {
		return cand
	}
	return now
}

func hashMod(s string, mod uint32) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32() % mod
}

// WarmupDailyLimit implements layer-9 ramp: 20,40,80,150,250,400,600,800,1000…
func WarmupDailyLimit(warmupDay, accountQuota int) int {
	if accountQuota <= 0 {
		accountQuota = 40
	}
	ramp := []int{20, 40, 80, 150, 250, 400, 600, 800, 1000, 1500, 2000}
	day := warmupDay
	if day < 0 {
		day = 0
	}
	var limit int
	if day < len(ramp) {
		limit = ramp[day]
	} else {
		limit = ramp[len(ramp)-1] + (day-len(ramp)+1)*500
	}
	if limit > accountQuota {
		return accountQuota
	}
	return limit
}
