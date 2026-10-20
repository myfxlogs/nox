package exec

import (
	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// mtLegVenueKeyOf D-633：USDPerNow preferVenueKey=本仓首 MT 腿 venueKey（腿键域优先——跨 broker 脏锚修正）。
func mtLegVenueKeyOf(legs []LegSpec) string {
	for _, l := range legs {
		if isMTVenueKey(l.VenueKey) {
			return l.VenueKey
		}
	}
	return ""
}

// mtLegVenueKeyOfPos pgstore.PositionLeg 版（pipeline_resume 实型——l.Venue=键列）。
func mtLegVenueKeyOfPos(legs []pgstore.PositionLeg) string {
	for _, l := range legs {
		if keydomain.IsMTKey(l.Venue) {
			return l.Venue
		}
	}
	return ""
}
