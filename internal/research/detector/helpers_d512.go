// helpers_d512.go —— D-512 T6：CrossVenueDetector 退役，共享 helper（stale/sortStrings）迁此保留。
package detector

import (
	"time"

	"arb/internal/data/bus"
)

func stale(q bus.Quote, now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return now.Sub(q.Time) > ttl
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
