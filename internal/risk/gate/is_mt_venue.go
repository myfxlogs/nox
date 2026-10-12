// is_mt_venue.go —— venueKey MT 判定（D-135 sizing CS fail-closed 用；engine_opp_qty.go 拆出，D-212 拆搬）。
package gate

import "arb/internal/data/keydomain"

// IsMTVenue 判断 venueKey 是否为 MT5/MT4 venue。
func IsMTVenue(venue string) bool {
	return keydomain.IsMTKey(venue)
}
