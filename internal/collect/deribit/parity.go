// Package deribit 实现 Deribit 期权平价偏离采集监控（D-137）。
// 只采集+监控+告警，不执行/不接账户/免 API key（public 行情端点）。
// 产出 Fact（opt_parity_dev，UnitBps）+ 告警（|dev_bps|>阈值 持续≥3 tick）。
package deribit

import (
	"math"
)

// Venue 常量。
const Venue = "deribit:option"

// optBookSummary Deribit get_book_summary_by_currency 单条记录（option/future 通用）。
// 字段名对齐 API JSON（mark_price/underlying_price/instrument_name/bid_price/ask_price）。
type optBookSummary struct {
	InstrumentName  string  // BTC-27SEP26-65000-C（option）/ BTC-27SEP26（future）
	MarkPrice       float64 // option: BTC 计价；future: USD 计价
	UnderlyingPrice float64 // option: per-contract index；future: 0（不用）
	BidPrice        float64 // 可能为 0（API null → 0）
	AskPrice        float64 // 可能为 0（API null → 0）
	BaseCurrency    string  // BTC/ETH
}

// optInstrument 解析后的期权 instrument 名。
// 格式：BTC-DDMMMYY-STRIKE-C/P（Deribit 原生）→ 归一 symbol=BTC:20260927:65000:C。
type optInstrument struct {
	Base      string  // BTC/ETH
	Expiry    string  // 原生 DDMMMYY（27SEP26）
	Strike    float64 // 65000
	IsCall    bool    // true=call, false=put
	Canonical string  // BTC:20260927:65000:C（归一 symbol，落库用）
}

// parseExpiry 将 Deribit DDMMMYY（如 27SEP26）解析为 YYYYMMDD（20260927）。
// Deribit 到期日均为 UTC 08:00；此处只取日期部分用于归一 symbol。
// 解析失败返回空串（调用方跳过该 instrument）。
func parseExpiry(s string) string {
	if len(s) != 7 {
		return ""
	}
	day := s[:2]
	monStr := s[2:5]
	yr := s[5:]
	months := map[string]string{
		"JAN": "01", "FEB": "02", "MAR": "03", "APR": "04",
		"MAY": "05", "JUN": "06", "JUL": "07", "AUG": "08",
		"SEP": "09", "OCT": "10", "NOV": "11", "DEC": "12",
	}
	mon, ok := months[monStr]
	if !ok {
		return ""
	}
	// Deribit 年份 2 位 → 20XX（Deribit 上市合约均在 2000 年代）
	return "20" + yr + mon + day
}

// parseInstrument 解析期权 instrument 名。非 option 格式返回 ok=false。
// 格式：BTC-27SEP26-65000-C。strike 可能为非整数（如 65000.5），用 strconv 自由解析。
func parseInstrument(name string) (optInstrument, bool) {
	// 分割：base-expiry-strike-type
	parts := splitInstrument(name)
	if len(parts) != 4 {
		return optInstrument{}, false
	}
	expiry := parseExpiry(parts[1])
	if expiry == "" {
		return optInstrument{}, false
	}
	strike, ok := parseFloat(parts[2])
	if !ok {
		return optInstrument{}, false
	}
	isCall := parts[3] == "C"
	if parts[3] != "C" && parts[3] != "P" {
		return optInstrument{}, false
	}
	return optInstrument{
		Base:      parts[0],
		Expiry:    parts[1],
		Strike:    strike,
		IsCall:    isCall,
		Canonical: parts[0] + ":" + expiry + ":" + parts[2] + ":" + parts[3],
	}, true
}

// splitInstrument 按 '-' 分割 instrument 名（不引入 strings 依赖，纯索引）。
func splitInstrument(name string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(name); i++ {
		if name[i] == '-' {
			parts = append(parts, name[start:i])
			start = i + 1
		}
	}
	parts = append(parts, name[start:])
	return parts
}

// parseFloat 简易 float 解析（避免引入 strconv 到纯逻辑文件；测试覆盖）。
func parseFloat(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	} else if s[0] == '+' {
		i = 1
	}
	intPart := 0.0
	hasInt := false
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		intPart = intPart*10 + float64(s[i]-'0')
		hasInt = true
	}
	frac := 0.0
	if i < len(s) && s[i] == '.' {
		i++
		div := 10.0
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			frac += float64(s[i]-'0') / div
			div *= 10
		}
	}
	if !hasInt && frac == 0 {
		return 0, false
	}
	v := intPart + frac
	if neg {
		v = -v
	}
	return v, true
}

// ParityResult 单个 (标的,到期,行权价) 的平价偏离计算结果。
type ParityResult struct {
	Instrument optInstrument
	C, P, F, K float64 // call/put mark(USD)、期货价、行权价
	Dev        float64 // (C−P)−(F−K)，USD
	DevBps     float64 // dev / underlying × 10000
	Underlying float64 // underlying_price（用于 bps 换算）
}

// computeParity 计算单对 call/put 的平价偏离（D-137 §1）。
// C/P=mark_price（API 返回 BTC 计价 → 乘 underlying_price 转 USD）；
// F=同到期期货 mark_price（USD）；K=strike。
// dev = (C−P)−(F−K)；dev_bps = dev/underlying×10000。
// 第一版 r=0（K·e^(−rT)≈K）：偏离量级≥费率容忍度时 r 细节不影响判定。
func computeParity(call, put optBookSummary, futurePrice, strike, underlying float64) ParityResult {
	cUSD := call.MarkPrice * underlying // option mark 以 BTC 计价 → USD
	pUSD := put.MarkPrice * underlying
	dev := (cUSD - pUSD) - (futurePrice - strike)
	devBps := 0.0
	if underlying > 0 {
		devBps = dev / underlying * 10000
	}
	return ParityResult{
		C:          cUSD,
		P:          pUSD,
		F:          futurePrice,
		K:          strike,
		Dev:        dev,
		DevBps:     devBps,
		Underlying: underlying,
	}
}

// absBps 返回 |dev_bps|。
func absBps(v float64) float64 { return math.Abs(v) }
