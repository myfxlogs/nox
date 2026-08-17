package bus

import "time"

// Quote 是 CFD 和加密行情的公共抽象（§1.1）。
// 热路径用 float64，无 decimal 分配。
type Quote struct {
	Symbol      string    // canonical symbol（EURUSD / BTCUSDT）
	Venue       string    // 场所标识（icmarkets / binance / okx）
	Bid         float64
	Ask         float64
	Time        time.Time // 报价时刻
	VenueSymbol string    // 原始符号（broker 特有命名）
	VenueType   string    // mt4 / mt5 / spot / perp（D-028）
}

// VenueKey 返回复合键 "{venue}:{venue_type}"（D-028）。
func (q Quote) VenueKey() string {
	return q.Venue + ":" + q.VenueType
}

// Mid 返回中间价 (bid+ask)/2。
func (q Quote) Mid() float64 {
	return (q.Bid + q.Ask) / 2
}
