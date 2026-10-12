// Package deribit 实现 Deribit 期权平价偏离采集监控（D-137）。
// 只采集+监控+告警，不执行/不接账户/免 API key（public 行情端点）。
// 产出 Fact（opt_parity_dev，UnitBps）+ 告警（|dev_bps|>阈值 持续≥3 tick）。
package deribit

import (
	"math"
	"strconv"
	"strings"
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
// 格式：BTC-DDMMMYY-STRIKE-C/P（Deribit 原生）→ 归一 symbol=BTC:20260927:65000（无 C/P，平价是 call-put 对属性）。
type optInstrument struct {
	Base      string  // BTC/ETH
	Expiry    string  // 原生 DDMMMYY（27SEP26）
	ExpiryYMD string  // YYYYMMDD（20260927，排序/分组键，字典序=日历序）
	Strike    float64 // 65000
	IsCall    bool    // true=call, false=put
	Canonical string  // BTC:20260927:65000（归一 symbol，落库用，无 C/P 后缀）
}

// parseExpiry 将 Deribit D[D]MMMYY（如 27SEP26 / 4SEP26）解析为 YYYYMMDD（20260927 / 20260904）。
// Deribit 日份无前导零（4SEP26=6 字符，27SEP26=7 字符）；月份固定 3 字母，年份固定 2 位。
// 输出 YYYYMMDD 日份补前导零（字典序=日历序的前提）。
// Deribit 到期日均为 UTC 08:00；此处只取日期部分用于归一 symbol 与排序键。
// 解析失败返回空串（调用方跳过该 instrument）。
func parseExpiry(s string) string {
	var day, monStr, yr string
	switch len(s) {
	case 7: // DDMMMYY
		day, monStr, yr = s[:2], s[2:5], s[5:]
	case 6: // DMMMYY（单日份无前导零）
		day, monStr, yr = s[:1], s[1:4], s[4:]
	default:
		return ""
	}
	months := map[string]string{
		"JAN": "01", "FEB": "02", "MAR": "03", "APR": "04",
		"MAY": "05", "JUN": "06", "JUL": "07", "AUG": "08",
		"SEP": "09", "OCT": "10", "NOV": "11", "DEC": "12",
	}
	mon, ok := months[monStr]
	if !ok {
		return ""
	}
	// 日份补前导零（4 → 04）确保字典序=日历序
	if len(day) == 1 {
		day = "0" + day
	}
	// Deribit 年份 2 位 → 20XX（Deribit 上市合约均在 2000 年代）
	return "20" + yr + mon + day
}

// parseInstrument 解析期权 instrument 名。非 option 格式返回 ok=false。
// 格式：BTC-27SEP26-65000-C。strike 可能为非整数（如 65000.5），用 strconv 自由解析。
func parseInstrument(name string) (optInstrument, bool) {
	// 分割：base-expiry-strike-type
	parts := strings.Split(name, "-")
	if len(parts) != 4 {
		return optInstrument{}, false
	}
	expiryYMD := parseExpiry(parts[1])
	if expiryYMD == "" {
		return optInstrument{}, false
	}
	strike, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return optInstrument{}, false
	}
	isCall := parts[3] == "C"
	if parts[3] != "C" && parts[3] != "P" {
		return optInstrument{}, false
	}
	return optInstrument{
		Base:      parts[0],
		Expiry:    parts[1],
		ExpiryYMD: expiryYMD,
		Strike:    strike,
		IsCall:    isCall,
		// 归一 symbol 无 C/P 后缀——平价偏离是 call-put 对的属性，非单腿。
		Canonical: parts[0] + ":" + expiryYMD + ":" + parts[2],
	}, true
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
