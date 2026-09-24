package exchange

import (
	"context"
	"fmt"
	"time"

	"arb/internal/fact"
)

// OKXFunding 采集 OKX funding rate（REST 轮询）。
type OKXFunding struct {
	cfg Config
}

func NewOKXFunding(cfg Config) *OKXFunding { return &OKXFunding{cfg: cfg} }

func (o *OKXFunding) Kind() string { return fact.KindFunding }

func (o *OKXFunding) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range o.syms() {
		instID := sym + "-USDT-SWAP"
		url := fmt.Sprintf("%s/api/v5/public/funding-rate?instId=%s", o.cfg.OKXBaseURL, instID)
		body, err := getJSON(ctx, o.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:okx] funding %s: %v (skip)\n", instID, err)
			continue
		}
		data, ok := body["data"].([]any)
		if !ok || len(data) == 0 {
			continue
		}
		item, ok := data[0].(map[string]any)
		if !ok {
			continue
		}
		rate, ok := item["fundingRate"].(string)
		if !ok {
			continue
		}
		var f float64
		fmt.Sscanf(rate, "%f", &f)
		// D-079：OKX 返回 fundingTime（当前结算）+ nextFundingTime（下次结算）→ 间隔 = 差值。
		// 旧版只用 fundingTime-now（剩余时间）当间隔 → 结算前年化爆炸（94.9% vs 真实 10.95%）。
		var fundingMs, nextFundingMs float64
		if ft, ok := item["fundingTime"].(string); ok {
			fmt.Sscanf(ft, "%f", &fundingMs)
		}
		if nft, ok := item["nextFundingTime"].(string); ok {
			fmt.Sscanf(nft, "%f", &nextFundingMs)
		}
		intervalMs := nextFundingMs - fundingMs
		if intervalMs <= 0 {
			fmt.Printf("[exchange:okx] %s funding interval missing (ft=%v nft=%v), fallback 3/day\n", sym+"USDT", fundingMs, nextFundingMs)
		}
		periodsPerDay := fundingPeriodsPerDay(intervalMs)
		annualized := annualizeFunding(f, periodsPerDay)
		out = append(out, fact.Fact{
			Kind:   fact.KindFunding,
			Venue:  VenueOKX,
			Symbol: sym + "USDT",
			Value:  annualized,
			Unit:   fact.UnitPctAnnualized,
			Ts:     time.Now(),
			Src:    "okx_v5",
		})
		// D-466 S1：nextFundingTime（ms）+结算间隔同帧发出（实测 8h=28800s）。
		if nextFundingMs > 0 {
			out = append(out, fact.Fact{Kind: fact.KindFundingNextSettle, Venue: VenueOKX,
				Symbol: sym + "USDT", Value: nextFundingMs / 1000, Unit: fact.UnitUnixSec, Ts: time.Now(), Src: "okx_v5"})
		}
		if intervalMs > 0 {
			out = append(out, fact.Fact{Kind: fact.KindFundingInterval, Venue: VenueOKX,
				Symbol: sym + "USDT", Value: intervalMs / 1000, Unit: fact.UnitSeconds, Ts: time.Now(), Src: "okx_v5"})
		}
	}
	return out, nil
}
