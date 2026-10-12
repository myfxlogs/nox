package bus

import "time"

// FundingNeedNotice 资金注入提醒事件（D-072；D-508 S7b 自 dashboard 迁入——
// autotrade 窄接口解逆向依赖后事件类型须居中立叶子包）：swap 套利余额不足时
// 发布 → StreamUpdates case 10 → 前端弹窗。不入 alerts 表、不走 alertPaused；
// 断线错过不回放（实时弹窗语义）。
type FundingNeedNotice struct {
	Canonical  string
	VenueLong  string
	VenueShort string
	Reason     string  // SwapArbNotional 返回的 reason（含具体数值）
	NeedUSD    float64 // minNotional − min(equity)；非余额原因（venue 离线/币种不符）= 0
	Ts         time.Time
	AliasLong  string // 买腿别名（aliasOrKey 填充，空=无别名）
	AliasShort string // 卖腿别名（aliasOrKey 填充，空=无别名）
}
