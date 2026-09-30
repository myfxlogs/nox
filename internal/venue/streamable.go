package venue

// HasPrivateStream 账户行私有 WS 事件写口覆盖判定：该 (venue, venue_type) 账户
// 快照是否由私有 WS 直写 fin_venue_account_snapshot（src=*_event）。
//
// 覆盖语义 ≠ 起流语义：okx 统一账户一条私有流覆盖 spot/perp 两键
// （写侧归一 unified，见 cexscan.normalizeVenueType），故 okx:spot 也返回 true；
// dashboard.cex_stream_manager 决定"哪行 config 起流"仍保留 perp-only 收窄。
//
// binance:spot 仅 real 有流（demo spot ws-api 不在代理路由内，D-459）。
func HasPrivateStream(venue, venueType string, simulated bool) bool {
	switch venue {
	case "gate":
		return venueType == "perp"
	case "binance":
		if venueType == "spot" {
			return !simulated
		}
		return venueType == "perp"
	case "okx":
		return venueType == "perp" || venueType == "spot"
	}
	return false
}
