package reconcile

import "strconv"

// OKXBalanceDetail D-434 R1：OKX /api/v5/account/balance data[0].details[] 行
// （REST 与 WS account 推送同构 schema，字段集一致）。REST 侧读全字段（holdings/
// equity），WS 侧只取 Ccy/EqUsd/Mmr/MgnRatio——同一类型禁两份拷贝（本洞即拷贝漂移产物）。
type OKXBalanceDetail struct {
	Ccy      string `json:"ccy"`
	Eq       string `json:"eq"`       // 本币权益（实采实证：币种计价，非 USD）
	CashBal  string `json:"cashBal"`  // 本币数量（总持有）
	AvailBal string `json:"availBal"` // 可用余额
	EqUsd    string `json:"eqUsd"`    // USD 等值
	Mmr      string `json:"mmr"`      // D-434：本币 MMR（USD 口径，顶层空时 details 供值）
	MgnRatio string `json:"mgnRatio"` // D-434：本币层保证金率回落
}

// OKXBufferMultiple D-434 R1 共享提取：buffer_multiple=equity/MMR 三层回落（REST
// accountOKX 与 WS okxAccountPushRow 同一函数）。pos502 实采实证本账户口径下顶层
// mgnRatio 空而 details[USDT] 供 mmr/mgnRatio。
// ①Σdetails[USDT].mmr>0 且 eq>0 → eq/Σmmr（与 binance 自算 marginBalance/maintMargin
// 同口径，首选）②details[USDT].mgnRatio>0 → /100（venue 权威回落）③顶层 mgnRatio>0
// → /100（原行为）④全空 → 0=未采 → guard 判 data_stale（诚实）。
func OKXBufferMultiple(eq float64, details []OKXBalanceDetail, topMgnRatio string) float64 {
	sumMMR, detailMgn := 0.0, 0.0
	for _, d := range details {
		if d.Ccy != "USDT" {
			continue
		}
		if v, perr := strconv.ParseFloat(d.Mmr, 64); perr == nil && v > 0 {
			sumMMR += v
		}
		if v, perr := strconv.ParseFloat(d.MgnRatio, 64); perr == nil && v > 0 {
			detailMgn = v
		}
	}
	switch {
	case sumMMR > 0 && eq > 0:
		return eq / sumMMR
	case detailMgn > 0:
		return detailMgn / 100
	default:
		if mr, perr := strconv.ParseFloat(topMgnRatio, 64); perr == nil && mr > 0 {
			return mr / 100
		}
	}
	return 0
}
