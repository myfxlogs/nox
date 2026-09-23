// pairs.go D-423：监控对清单（自 main.go 拆出保行预算）。
package main

// monitorPair D-335R1 S1 + D-423 venue 化：{cexVenue, sym, pair, exSym}——
// cexVenue 路由 CEX 报价源（binance:perp=经 cex-proxy fapi；okx:perp=OKX 公开行情），
// 行级 venue 三元组落库（pair,cex_venue,mt_venue），消费端按仓 venue 直查禁借用。
type monitorPair struct{ cexVenue, sym, pair, exSym string }

// monitorPairs 全监控对清单（S0 缺陷修复：旧版共享单一 XAUUSD 报价喂全部对 →
// BTCUSDT-BTC 实为 binance BTC×XAU 顶替，esp=175348bps 垃圾行 14:20 实拍；
// 事实核查=3001157 流内 BTC 符号=BTCUSDT）。
// D-423：加 okx:perp×XAUT-USDT-SWAP 行——真钱组合 pos 501（okx:perp×3001157:mt5,
// XAU-USDT-SWAP×XAUUSDm）的 venue 簿；pair 键=消费端口径 CanonicalSell-TrimSuffix
// （"XAUUSDT-XAU"），与 binance 行同 pair 不同 cex_venue=三元组隔离本意。
func monitorPairs() []monitorPair {
	return []monitorPair{
		{"binance:perp", "PAXGUSDT", "PAXG-XAU", "XAUUSD"},
		{"binance:perp", "XAUTUSDT", "XAUT-XAU", "XAUUSD"},
		{"binance:perp", "XAUUSDT", "XAUUSDT-XAU", "XAUUSD"},
		{"binance:perp", "BTCUSDT", "BTCUSDT-BTC", "BTCUSDT"},   // D-335 S0：业主批第四对（BTCUSDT×BTCUSDT，swap 免息，纳入观察）
		{"okx:perp", "XAUT-USDT-SWAP", "XAUUSDT-XAU", "XAUUSD"}, // D-423：OKX 黄金永续×Exness XAU（pos 501 组合）
	}
}
