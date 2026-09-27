// Package macro D-528：宏观采集层（COT/财历/FRED 三源移植自 ark-intelligent 三 fetcher，
// stdlib 重写零 vendor；fetcher 纯函数不碰 DB，落库=pgstore.Macro* 方法，调度=独立 goroutine）。
// 网络断言：mql5/cftc/fred 全公网直连路径，http.Client 不配 proxy（出口劫持由宿主层负责，
// 与 cex-proxy token 通道零耦合）；http.Client 30s 超时自建，不复用 cfg.Client（那是 cex-proxy 通道）。
package macro

import "strings"

// CotContract CFTC 合约注册行（code=CFTC 合约码；report=TFF|DISAGGREGATED；sector=渲染分区）。
type CotContract struct {
	Code     string
	Symbol   string // 渲染/存储用品种键（cot_position.instrument）
	Name     string // CFTC 市场名（英文）
	Currency string
	Sector   string // FX/EQ/CMDTY/BOND/CRYPTO
	Report   string // TFF/DISAGGREGATED
}

// cotContracts 品种全集（照 ark DefaultCOTContracts 移植；渲染分区=sector）。
var cotContracts = []CotContract{
	// 外汇（TFF）
	{Code: "099741", Symbol: "6E", Name: "Euro FX", Currency: "EUR", Sector: "FX", Report: "TFF"},
	{Code: "096742", Symbol: "6B", Name: "British Pound", Currency: "GBP", Sector: "FX", Report: "TFF"},
	{Code: "097741", Symbol: "6J", Name: "Japanese Yen", Currency: "JPY", Sector: "FX", Report: "TFF"},
	{Code: "092741", Symbol: "6S", Name: "Swiss Franc", Currency: "CHF", Sector: "FX", Report: "TFF"},
	{Code: "232741", Symbol: "6A", Name: "Australian Dollar", Currency: "AUD", Sector: "FX", Report: "TFF"},
	{Code: "090741", Symbol: "6C", Name: "Canadian Dollar", Currency: "CAD", Sector: "FX", Report: "TFF"},
	{Code: "112741", Symbol: "6N", Name: "NZ Dollar", Currency: "NZD", Sector: "FX", Report: "TFF"},
	{Code: "098662", Symbol: "DX", Name: "US Dollar Index", Currency: "USD", Sector: "FX", Report: "TFF"},
	// 股指（TFF）
	{Code: "13874A", Symbol: "ES", Name: "S&P 500 E-mini", Currency: "SPX500", Sector: "EQ", Report: "TFF"},
	{Code: "209742", Symbol: "NQ", Name: "Nasdaq 100 E-mini", Currency: "NDX", Sector: "EQ", Report: "TFF"},
	{Code: "124601", Symbol: "YM", Name: "Dow Jones E-mini", Currency: "DJI", Sector: "EQ", Report: "TFF"},
	{Code: "239742", Symbol: "RTY", Name: "Russell 2000 E-mini", Currency: "RUT", Sector: "EQ", Report: "TFF"},
	// 大宗=金属+能源（DISAGGREGATED）
	{Code: "088691", Symbol: "GC", Name: "Gold", Currency: "XAU", Sector: "CMDTY", Report: "DISAGGREGATED"},
	{Code: "084691", Symbol: "SI", Name: "Silver", Currency: "XAG", Sector: "CMDTY", Report: "DISAGGREGATED"},
	{Code: "085692", Symbol: "HG", Name: "Copper", Currency: "COPPER", Sector: "CMDTY", Report: "DISAGGREGATED"},
	{Code: "067651", Symbol: "CL", Name: "Crude Oil WTI", Currency: "OIL", Sector: "CMDTY", Report: "DISAGGREGATED"},
	{Code: "022651", Symbol: "HO", Name: "NY Harbor ULSD", Currency: "ULSD", Sector: "CMDTY", Report: "DISAGGREGATED"},
	{Code: "111659", Symbol: "RB", Name: "RBOB Gasoline", Currency: "RBOB", Sector: "CMDTY", Report: "DISAGGREGATED"},
	// 债券（TFF）
	{Code: "043602", Symbol: "ZN", Name: "10-Year T-Note", Currency: "BOND", Sector: "BOND", Report: "TFF"},
	{Code: "020601", Symbol: "ZB", Name: "30-Year T-Bond", Currency: "BOND30", Sector: "BOND", Report: "TFF"},
	{Code: "044601", Symbol: "ZF", Name: "5-Year T-Note", Currency: "BOND5", Sector: "BOND", Report: "TFF"},
	{Code: "042601", Symbol: "TU", Name: "2-Year T-Note", Currency: "BOND2", Sector: "BOND", Report: "TFF"},
	// 加密（TFF）
	{Code: "133741", Symbol: "BTC", Name: "Bitcoin", Currency: "BTC", Sector: "CRYPTO", Report: "TFF"},
	{Code: "146021", Symbol: "ETH", Name: "Ether", Currency: "ETH", Sector: "CRYPTO", Report: "TFF"},
}

// cotByCode/codeBySymbol 索引（fetch 解析与 ResolveInstrument 用）。
var cotByCode = func() map[string]CotContract {
	m := make(map[string]CotContract, len(cotContracts))
	for _, c := range cotContracts {
		m[c.Code] = c
	}
	return m
}()

// ResolveInstrument 品种别名解析（LLM 工具入参宽匹配：symbol/英文名/currency/中文名
// 大小写不敏感包含即命中；唯一命中返回，零或多命中返回 ""）。
func ResolveInstrument(in string) string {
	in = strings.ToUpper(strings.TrimSpace(in))
	if in == "" {
		return ""
	}
	var hits []string
	for _, c := range cotContracts {
		if strings.Contains(strings.ToUpper(c.Symbol), in) ||
			strings.Contains(strings.ToUpper(c.Name), in) ||
			strings.Contains(strings.ToUpper(c.Currency), in) ||
			strings.Contains(cnName(c.Symbol), strings.TrimSpace(in)) {
			hits = append(hits, c.Symbol)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return ""
}

// cnName 品种中文名（渲染与别名匹配共用；键=Symbol）。
func cnName(symbol string) string {
	m := map[string]string{
		"6E": "欧元", "6B": "英镑", "6J": "日元", "6S": "瑞郎", "6A": "澳元",
		"6C": "加元", "6N": "纽元", "DX": "美元指数",
		"ES": "标普500", "NQ": "纳指100", "YM": "道指", "RTY": "罗素2000",
		"GC": "黄金", "SI": "白银", "HG": "铜", "CL": "WTI原油", "HO": "柴油", "RB": "汽油",
		"ZN": "10年美债", "ZB": "30年美债", "ZF": "5年美债", "TU": "2年美债",
		"BTC": "比特币", "ETH": "以太坊",
	}
	return m[symbol]
}

// MQL5URL 生产端点只读暴露（A2 容器探针用；测试走自建 server 不经此）。
func MQL5URL() string { return mql5URL }
