package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ---------- Binance（4 调用：exchangeInfo + premiumIndex 全量 + ticker/24hr + fundingInfo） ----------

type binance struct {
	hc   *http.Client
	base string
}

func (b binance) Venue() string { return "binance" }

func (b binance) Scan(ctx context.Context) ([]Row, error) {
	var ei struct {
		Symbols []struct {
			Symbol         string  `json:"symbol"`
			BaseAsset      string  `json:"baseAsset"`
			QuoteAsset     string  `json:"quoteAsset"`
			ContractType   string  `json:"contractType"`
			UnderlyingType string  `json:"underlyingType"`
			Status         string  `json:"status"`
			OnboardDate    flexInt `json:"onboardDate"` // ms
		} `json:"symbols"`
	}
	if err := getJSON(ctx, b.hc, b.base+"/fapi/v1/exchangeInfo", &ei); err != nil {
		return nil, fmt.Errorf("exchangeInfo: %w", err)
	}
	universe := make(map[string]bool)
	rows := make([]Row, 0, len(ei.Symbols))
	idx := make(map[string]int)
	for _, s := range ei.Symbols {
		// 仅收 USDT 本位 perp（contractType 含 PERPETUAL、quoteAsset=USDT）
		if !strings.Contains(s.ContractType, "PERPETUAL") || s.QuoteAsset != "USDT" {
			continue
		}
		idx[s.Symbol] = len(rows)
		universe[s.Symbol] = true
		// Class=underlyingType 原样存（COIN/COMMODITY/…，不臆造 TRADIFI 类）；status 原样存
		rows = append(rows, Row{
			Symbol: s.Symbol, Base: s.BaseAsset, Class: s.UnderlyingType,
			Status: s.Status, LaunchTS: msTime(int64(s.OnboardDate)),
		})
	}
	var pi []struct {
		Symbol          string    `json:"symbol"`
		MarkPrice       flexFloat `json:"markPrice"`
		IndexPrice      flexFloat `json:"indexPrice"`
		LastFundingRate flexFloat `json:"lastFundingRate"`
	}
	if err := getJSON(ctx, b.hc, b.base+"/fapi/v1/premiumIndex", &pi); err != nil {
		return nil, fmt.Errorf("premiumIndex: %w", err)
	}
	for _, p := range pi {
		if i, ok := idx[p.Symbol]; ok { // 全量比 universe 多，只 join universe 内 symbol
			rows[i].Mark = float64(p.MarkPrice)
			rows[i].Index = float64(p.IndexPrice)
			rows[i].FundingRate = float64(p.LastFundingRate)
		}
	}
	var tk []struct {
		Symbol      string    `json:"symbol"`
		QuoteVolume flexFloat `json:"quoteVolume"`
	}
	if err := getJSON(ctx, b.hc, b.base+"/fapi/v1/ticker/24hr", &tk); err != nil {
		return nil, fmt.Errorf("ticker/24hr: %w", err)
	}
	for _, t := range tk {
		if i, ok := idx[t.Symbol]; ok {
			rows[i].Vol24h = float64(t.QuoteVolume)
		}
	}
	var fi []struct {
		Symbol               string  `json:"symbol"`
		FundingIntervalHours flexInt `json:"fundingIntervalHours"`
	}
	if err := getJSON(ctx, b.hc, b.base+"/fapi/v1/fundingInfo", &fi); err != nil {
		return nil, fmt.Errorf("fundingInfo: %w", err)
	}
	for _, f := range fi {
		if i, ok := idx[f.Symbol]; ok {
			rows[i].IntervalH = int(f.FundingIntervalHours)
		}
	}
	return rows, nil
}
