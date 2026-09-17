// D-172 S1：CEX funding 结算历史采集器（三所）。
// 补采过去 30 天 funding 结算历史写入 facts（kind=funding_history），
// 用于基差回测（S2）+ 结算费率 vs 轮询偏差验证（S4）。
//
// 与实时 funding 采集器（exchange.go）的区别：
//   - 实时端点返回预测费率（下一次结算的预估值）；
//   - 历史端点返回结算费率（实际结算时的值）——S4 偏差验证的数据源。
//   - Ts 用 API 返回的结算时刻（非 time.Now()），S4 按结算时刻配对对比。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"arb/internal/fact"
)

// HistoryStartResolver 返回某 venue×symbol 的最近 funding_history 时间戳，
// 无历史返回零时间（首次回填）。用于增量补采避免重复。
type HistoryStartResolver func(ctx context.Context, venue, symbol string) (time.Time, error)

const fundingHistoryBackfill = 30 * 24 * time.Hour // 首次回填窗口

// fundingHistoryPeriodsPerDay = 3（8h 结算，与实时采集器回退一致；Q5 三所默认 8h 对齐）。
const fundingHistoryPeriodsPerDay = 3

// startTS 返回补采起始时刻：有历史=last+1s（增量），无历史=now-30d（首次回填）。
func startTS(ctx context.Context, resolve HistoryStartResolver, venue, symbol string) (time.Time, error) {
	last, err := resolve(ctx, venue, symbol)
	if err != nil {
		return time.Time{}, err
	}
	if last.IsZero() {
		return time.Now().Add(-fundingHistoryBackfill), nil
	}
	return last.Add(time.Second), nil
}

// BinanceFundingHistory 采集 Binance funding 结算历史。
// 端点 GET /fapi/v1/fundingRate?symbol=&startTime=&endTime=&limit=1000
// 响应：[{fundingTime(ms number), fundingRate(string)}]，limit 1000 覆盖 30 天。
// D-301a S2：intervals 注入 per-symbol 结算周期真值（fundingInfo fundingIntervalHours），
// nil → 回退 fundingHistoryPeriodsPerDay=3（8h，D-079 禁静默）。
type BinanceFundingHistory struct {
	cfg       Config
	resolve   HistoryStartResolver
	intervals *FundingIntervalCache
}

func NewBinanceFundingHistory(cfg Config, r HistoryStartResolver) *BinanceFundingHistory {
	return &BinanceFundingHistory{cfg: cfg, resolve: r}
}

// SetFundingIntervals 注入 funding 周期缓存（D-301a S2）。
func (b *BinanceFundingHistory) SetFundingIntervals(c *FundingIntervalCache) { b.intervals = c }

func (b *BinanceFundingHistory) Kind() string { return fact.KindFundingHistory }

func (b *BinanceFundingHistory) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range b.cfg.Symbols {
		pair := sym + "USDT"
		start, err := startTS(ctx, b.resolve, VenueBinance, pair)
		if err != nil {
			// D-308 S1：逐符号 fail-open——cexSymbols 全局表在所间不对称，缺符号静默跳过不炸全所。
			fmt.Printf("[exchange:binance] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		url := fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&startTime=%d&endTime=%d&limit=1000",
			b.cfg.BinanceBaseURL, pair, start.UnixMilli(), time.Now().UnixMilli())
		arr, err := getJSONArray(ctx, b.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:binance] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			ft, _ := m["fundingTime"].(float64)
			rateStr, _ := m["fundingRate"].(string)
			var f float64
			fmt.Sscanf(rateStr, "%f", &f)
			// D-301a S2：per-symbol 结算周期真值（fundingInfo fundingIntervalHours）。
			periods := fundingHistoryPeriodsPerDay
			if b.intervals != nil {
				periods = b.intervals.PeriodsPerDay(pair)
			}
			out = append(out, fact.Fact{
				Kind: fact.KindFundingHistory, Venue: VenueBinance, Symbol: pair,
				Value: annualizeFunding(f, periods),
				Unit:  fact.UnitPctAnnualized, Ts: time.UnixMilli(int64(ft)), Src: "binance_fapi_history",
			})
		}
	}
	return out, nil
}

// OKXFundingHistory 采集 OKX funding 结算历史。
// 端点 GET /api/v5/public/funding-rate-history?instId=&before=&after=&limit=100
// 响应：{data:[{fundingRate(string), fundingTime(ms string)}]}，降序（新→旧）。
// 分页语义与参数命名相反（D-321 API 实证）：before=X 取「更新于 X」、after=X 取「早于 X」。
// 增量上界=before=watermark（取比已知更新的），窗口内翻页=after=本页最旧 fundingTime。
// limit 100，30 天 8h=90 条单页够；4h/1h 结算满页翻页。
type OKXFundingHistory struct {
	cfg     Config
	resolve HistoryStartResolver
}

func NewOKXFundingHistory(cfg Config, r HistoryStartResolver) *OKXFundingHistory {
	return &OKXFundingHistory{cfg: cfg, resolve: r}
}

func (o *OKXFundingHistory) Kind() string { return fact.KindFundingHistory }

// okxFundingPeriodsPerDay D-321 S2：探测 per-symbol 结算周期——funding-rate 端点
// fundingTime/nextFundingTime 差值推导（与实时路径 D-079 同口径）。OKX 存在 4h 结算
// symbol（CL-USDT-SWAP 实测），旧 ×3 硬编码低估 2×。任一失败 printf+回退 3
// （fail-open，同构增量路径的逐符号跳过）。
func (o *OKXFundingHistory) okxFundingPeriodsPerDay(ctx context.Context, instID string) int {
	url := fmt.Sprintf("%s/api/v5/public/funding-rate?instId=%s", o.cfg.OKXBaseURL, instID)
	body, err := getJSON(ctx, o.cfg.Client, url)
	if err != nil {
		fmt.Printf("[exchange:okx] funding_history %s period probe: %v (fallback 3/day)\n", instID, err)
		return fundingHistoryPeriodsPerDay
	}
	data, ok := body["data"].([]any)
	if !ok || len(data) == 0 {
		fmt.Printf("[exchange:okx] funding_history %s period probe empty (fallback 3/day)\n", instID)
		return fundingHistoryPeriodsPerDay
	}
	item, _ := data[0].(map[string]any)
	var ftMs, nftMs float64
	if s, ok := item["fundingTime"].(string); ok {
		fmt.Sscanf(s, "%f", &ftMs)
	}
	if s, ok := item["nextFundingTime"].(string); ok {
		fmt.Sscanf(s, "%f", &nftMs)
	}
	if nftMs-ftMs <= 0 {
		fmt.Printf("[exchange:okx] funding_history %s period probe ft=%v nft=%v (fallback 3/day)\n", instID, ftMs, nftMs)
	}
	return fundingPeriodsPerDay(nftMs - ftMs)
}

func (o *OKXFundingHistory) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
symLoop:
	for _, sym := range o.cfg.Symbols {
		pair := sym + "USDT"
		start, err := startTS(ctx, o.resolve, VenueOKX, pair)
		if err != nil {
			// D-308 S1：逐符号 fail-open（同 Binance）——部分页数据弃置，出参只带成功符号。
			fmt.Printf("[exchange:okx] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		instID := sym + "-USDT-SWAP"
		periods := o.okxFundingPeriodsPerDay(ctx, instID) // D-321 S2：per-symbol 周期真值
		// D-321 S1：参数角色对调——watermark 放 before（取更新方向）。旧码塞 after
		// （OKX 语义=取更早）→ 每轮只重取已知历史，dedup 零新增零报错，watermark 冻结
		// （KB pagination-direction-silent-freeze）。
		beforeMs := strconv.FormatInt(start.UnixMilli(), 10)
		after := "" // 窗口内翻页游标（首页不带）
		var symFacts []fact.Fact
		for {
			url := fmt.Sprintf("%s/api/v5/public/funding-rate-history?instId=%s&before=%s&limit=100",
				o.cfg.OKXBaseURL, instID, beforeMs)
			if after != "" {
				url += "&after=" + after
			}
			body, err := getJSON(ctx, o.cfg.Client, url)
			if err != nil {
				fmt.Printf("[exchange:okx] funding_history %s: %v (skip)\n", pair, err)
				continue symLoop
			}
			data, ok := body["data"].([]any)
			if !ok || len(data) == 0 {
				break
			}
			var oldest string
			for _, item := range data {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				ftStr, _ := m["fundingTime"].(string)
				rateStr, _ := m["fundingRate"].(string)
				var f float64
				fmt.Sscanf(rateStr, "%f", &f)
				ftMs, _ := strconv.ParseInt(ftStr, 10, 64)
				symFacts = append(symFacts, fact.Fact{
					Kind: fact.KindFundingHistory, Venue: VenueOKX, Symbol: pair,
					Value: annualizeFunding(f, periods),
					Unit:  fact.UnitPctAnnualized, Ts: time.UnixMilli(ftMs), Src: "okx_v5_history",
				})
				oldest = ftStr
			}
			if len(data) < 100 {
				break
			}
			after = oldest // 翻页：取比本页最旧更早、仍新于 watermark 的下一段
		}
		out = append(out, symFacts...)
	}
	return out, nil
}

// GateFundingHistory 采集 Gate funding 结算历史。
// 端点 GET /api/v4/futures/usdt/funding_rate?contract=&from=&to=&limit=1000
// 响应（实测 2026-08-31）：[{"r":"rate(string)","t":sec}]——t 是秒非毫秒（紧凑格式）。
// limit 1000（Gate API 上限，实测 limit=2000 返回 HTTP 400）；30 天 8h=90 条，1h 结算最坏 720 条，1000 够。
type GateFundingHistory struct {
	cfg     Config
	resolve HistoryStartResolver
}

func NewGateFundingHistory(cfg Config, r HistoryStartResolver) *GateFundingHistory {
	return &GateFundingHistory{cfg: cfg, resolve: r}
}

func (g *GateFundingHistory) Kind() string { return fact.KindFundingHistory }

func (g *GateFundingHistory) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range g.cfg.Symbols {
		pair := sym + "USDT"
		start, err := startTS(ctx, g.resolve, VenueGate, pair)
		if err != nil {
			// D-308 S1：逐符号 fail-open（同 Binance）。
			fmt.Printf("[exchange:gate] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		contract := sym + "_USDT"
		url := fmt.Sprintf("%s/api/v4/futures/usdt/funding_rate?contract=%s&from=%d&to=%d&limit=1000",
			g.cfg.GateBaseURL, contract, start.Unix(), time.Now().Unix())
		arr, err := getJSONArray(ctx, g.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:gate] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rateStr, _ := m["r"].(string)
			tSec, _ := m["t"].(float64) // Gate t = Unix 秒
			var f float64
			fmt.Sscanf(rateStr, "%f", &f)
			out = append(out, fact.Fact{
				Kind: fact.KindFundingHistory, Venue: VenueGate, Symbol: pair,
				Value: annualizeFunding(f, fundingHistoryPeriodsPerDay),
				Unit:  fact.UnitPctAnnualized, Ts: time.Unix(int64(tSec), 0), Src: "gate_v4_history",
			})
		}
	}
	return out, nil
}

// getJSONArray 发 GET 请求并解析 JSON 数组（Binance/Gate 历史端点返回数组非对象）。
func getJSONArray(ctx context.Context, client *http.Client, url string) ([]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var arr []any
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}
