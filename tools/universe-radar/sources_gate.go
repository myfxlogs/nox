package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ---------- Gate（2 调用：contracts 自带 funding + tickers） ----------

type gate struct {
	hc   *http.Client
	base string
}

func (g gate) Venue() string { return "gate" }

func (g gate) Scan(ctx context.Context) ([]Row, error) {
	var cs []struct {
		Name             string    `json:"name"`
		Status           string    `json:"status"`
		InDelisting      bool      `json:"in_delisting"`
		LaunchTime       flexInt   `json:"launch_time"`      // 秒
		FundingRate      flexFloat `json:"funding_rate"`     // 单次结算费率
		FundingInterval  flexInt   `json:"funding_interval"` // 秒（14400=4h）
		IndexPrice       flexFloat `json:"index_price"`
		MarkPrice        flexFloat `json:"mark_price"`
		QuantoMultiplier flexFloat `json:"quanto_multiplier"` // D-311 S2：合约面值（CL_USDT=0.01）
	}
	if err := getJSON(ctx, g.hc, g.base+"/api/v4/futures/usdt/contracts", &cs); err != nil {
		return nil, fmt.Errorf("contracts: %w", err)
	}
	rows := make([]Row, 0, len(cs))
	idx := make(map[string]int)
	for _, c := range cs {
		st := c.Status
		if c.Status == "trading" && !c.InDelisting {
			st = "TRADING"
		} // in_delisting=true 视同 status≠trading
		idx[c.Name] = len(rows)
		rows = append(rows, Row{
			Symbol: c.Name, Base: strings.TrimSuffix(c.Name, "_USDT"),
			Status: st, LaunchTS: secTime(int64(c.LaunchTime)),
			FundingRate: float64(c.FundingRate),
			IntervalH:   int(c.FundingInterval) / 3600,
			Mark:        float64(c.MarkPrice), Index: float64(c.IndexPrice),
			CtVal: f64ptr(float64(c.QuantoMultiplier)),
		})
	}
	var tk []struct {
		Contract       string    `json:"contract"`
		Volume24hQuote flexFloat `json:"volume_24h_quote"`
	}
	if err := getJSON(ctx, g.hc, g.base+"/api/v4/futures/usdt/tickers", &tk); err != nil {
		return nil, fmt.Errorf("tickers: %w", err)
	}
	for _, t := range tk {
		if i, ok := idx[t.Contract]; ok {
			rows[i].Vol24h = float64(t.Volume24hQuote)
		}
	}
	return rows, nil
}
