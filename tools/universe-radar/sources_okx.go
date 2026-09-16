package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ---------- OKX（3 调用：instruments + funding-rate ANY 全量 + market/tickers） ----------

type okx struct {
	hc   *http.Client
	base string
}

func (o okx) Venue() string { return "okx" }

func (o okx) Scan(ctx context.Context) ([]Row, error) {
	var ins struct {
		Data []struct {
			InstID   string  `json:"instId"`
			State    string  `json:"state"`
			ListTime flexInt `json:"listTime"` // ms
		} `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/public/instruments?instType=SWAP", &ins); err != nil {
		return nil, fmt.Errorf("instruments: %w", err)
	}
	rows := make([]Row, 0, len(ins.Data))
	idx := make(map[string]int)
	for _, d := range ins.Data {
		if !strings.HasSuffix(d.InstID, "-USDT-SWAP") {
			continue
		}
		st := d.State
		if st == "live" {
			st = "TRADING"
		}
		idx[d.InstID] = len(rows)
		rows = append(rows, Row{
			Symbol: d.InstID, Base: strings.TrimSuffix(d.InstID, "-USDT-SWAP"),
			Status: st, LaunchTS: msTime(int64(d.ListTime)),
		})
	}
	var fr struct {
		Data []struct {
			InstID          string    `json:"instId"`
			FundingRate     flexFloat `json:"fundingRate"`
			FundingTime     flexInt   `json:"fundingTime"`     // ms
			NextFundingTime flexInt   `json:"nextFundingTime"` // ms
		} `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/public/funding-rate?instId=ANY", &fr); err != nil {
		return nil, fmt.Errorf("funding-rate: %w", err)
	}
	for _, d := range fr.Data {
		if i, ok := idx[d.InstID]; ok {
			rows[i].FundingRate = float64(d.FundingRate)
			// interval=(nextFundingTime−fundingTime)/3600000 h（实测 14400000ms=4h）
			if d.NextFundingTime > d.FundingTime {
				rows[i].IntervalH = int((d.NextFundingTime - d.FundingTime) / 3600000)
			}
		}
	}
	var tk struct {
		Data []struct {
			InstID    string    `json:"instId"`
			VolCcy24h flexFloat `json:"volCcy24h"`
			Last      flexFloat `json:"last"`
		} `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/market/tickers?instType=SWAP", &tk); err != nil {
		return nil, fmt.Errorf("market/tickers: %w", err)
	}
	for _, d := range tk.Data {
		if i, ok := idx[d.InstID]; ok {
			rows[i].Vol24h = float64(d.VolCcy24h) * float64(d.Last)
			rows[i].Mark = float64(d.Last)
		}
	}
	return rows, nil
}
