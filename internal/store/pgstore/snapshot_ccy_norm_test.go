package pgstore

import (
	"testing"
	"time"
)

// collapseBlankCcyPool 锚（敞口闸分母口径修）：” 币残影行并入主币行防同池双计。
func TestCollapseBlankCcyPool(t *testing.T) {
	old := time.Date(2026, 10, 14, 0, 31, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	pool := func(vt, ccy string, eq float64, at time.Time) CapitalPoolRow {
		return CapitalPoolRow{Venue: "3001157", VenueType: vt, Equity: eq, Currency: ccy, FetchedAt: at}
	}
	collapse := func(rows []CapitalPoolRow) []CapitalPoolRow {
		return collapseBlankCcyPool(rows,
			func(p CapitalPoolRow) (string, string, string) { return p.Venue, p.VenueType, p.Currency },
			func(p CapitalPoolRow) time.Time { return p.FetchedAt },
			func(dst *CapitalPoolRow, src CapitalPoolRow) { dst.Equity = src.Equity })
	}

	// 主案：'' 陈旧残行并入较新 USD 行——残行弃、USD 值保留（3001157 实证型）。
	got := collapse([]CapitalPoolRow{
		pool("mt5", "", 4245.51, old), pool("mt5", "USD", 4247.41, newer),
	})
	if len(got) != 1 || got[0].Currency != "USD" || got[0].Equity != 4247.41 {
		t.Fatalf("%+v", got)
	}
	// '' 较新：采其值入主币行（币名不丢）。
	got = collapse([]CapitalPoolRow{
		pool("mt5", "", 4300.0, newer), pool("mt5", "USD", 4247.41, old),
	})
	if len(got) != 1 || got[0].Currency != "USD" || got[0].Equity != 4300.0 {
		t.Fatalf("%+v", got)
	}
	// 纯 '' 组无命名币→原样保留（如实不猜币名）。
	got = collapse([]CapitalPoolRow{pool("mt5", "", 100, old)})
	if len(got) != 1 || got[0].Currency != "" {
		t.Fatalf("%+v", got)
	}
	// 多命名币组（earn 分币）行不动；'' 折入最新命名行。
	got = collapse([]CapitalPoolRow{
		{Venue: "binance", VenueType: "earn", Currency: "", Equity: 1, FetchedAt: old},
		{Venue: "binance", VenueType: "earn", Currency: "ACH", Equity: 2, FetchedAt: old},
		{Venue: "binance", VenueType: "earn", Currency: "USDT", Equity: 3, FetchedAt: newer},
	})
	if len(got) != 2 || got[1].Currency != "USDT" || got[1].Equity != 3 {
		t.Fatalf("%+v", got)
	}
}
