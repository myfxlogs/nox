// dci_gate.go —— D-183：Gate DCI 报价采集+解析（自 dci.go 拆分，纯移动零语义改）。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

func (c *EarnCollector) collectGateDCI(ctx context.Context) ([]pgstore.CexDCIQuote, error) {
	return c.collectGateDCIWithBase(ctx, gateDualURL)
}

func (c *EarnCollector) collectGateDCIWithBase(ctx context.Context, url string) ([]pgstore.CexDCIQuote, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate dci request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gate dci HTTP %d: %s", resp.StatusCode, string(body))
	}
	return parseGateDCI(body, time.Now())
}

// parseGateDCI dual investment_plan 公开数组→行（BTC/ETH exercise_currency 过滤；
// apy_display 小数年化原样；type call/put→CALL/PUT）。
func parseGateDCI(body []byte, now time.Time) ([]pgstore.CexDCIQuote, error) {
	var raw []struct {
		ID               int64       `json:"id"`
		InstrumentName   string      `json:"instrument_name"`
		Type             string      `json:"type"`
		InvestCurrency   string      `json:"invest_currency"`
		ExerciseCurrency string      `json:"exercise_currency"`
		ExercisePrice    json.Number `json:"exercise_price"`
		DeliveryTime     json.Number `json:"delivery_time"`
		Status           string      `json:"status"`
		ApyDisplay       json.Number `json:"apy_display"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("gate dci decode: %w", err)
	}
	want := map[string]bool{"BTC": true, "ETH": true}
	var out []pgstore.CexDCIQuote
	for _, d := range raw {
		if !want[d.ExerciseCurrency] {
			continue
		}
		apy, err := d.ApyDisplay.Float64()
		if err != nil {
			log.Printf("[cexscan] gate dci apy parse %q: %v", d.ApyDisplay.String(), err)
			continue
		}
		row := pgstore.CexDCIQuote{Venue: "gate", Underlying: d.ExerciseCurrency,
			InvestCcy: d.InvestCurrency, OptType: strings.ToUpper(d.Type), APY: apy,
			ProductRef: firstNonEmpty(strconv.FormatInt(d.ID, 10), d.InstrumentName)}
		if v, err := d.ExercisePrice.Float64(); err == nil {
			row.Strike = &v
		}
		if sec, err := d.DeliveryTime.Int64(); err == nil && sec > 0 {
			ts := time.Unix(sec, 0)
			td := tenorOf(ts, now)
			row.DeliveryTs, row.TenorDays = &ts, &td
		}
		out = append(out, row)
	}
	return out, nil
}
