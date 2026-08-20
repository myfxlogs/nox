package dashboard

// canonicalize 归一 broker 原生名 → 统一 canonical（D-056）。
// 加密对 CFD 用 USD 报价（BTCUSD）、CEX 用 USDT（BTCUSDT）→ 统一到 USDT 后缀，
// 使 CFD-CEX cross 共居一桶。非加密对（外汇/金属）原样。
var cryptoUsdToUsdt = map[string]string{"BTCUSD": "BTCUSDT", "ETHUSD": "ETHUSDT"}

// Canonicalize 归一 broker 原生名 → 统一 canonical（单一真相源，P3）。
// 唯一实现：cmd/core 与本包内调用方一律走它，不两处各写一份。
func Canonicalize(name string) string {
	if c, ok := cryptoUsdToUsdt[name]; ok {
		return c
	}
	return name
}
