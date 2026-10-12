// dci.go —— D-183：三所 DCI/双币产品报价采集（只读）。
// 端点侦测结论（D-183 ⓪，SG 实拍）：binance sapi dci/product/list 签名可达；
// okx /api/v5/finance/sfp/dcd/products 签名可达（401 须凭据）；
// gate /api/v4/earn/dual/investment_plan **public 200** 无凭据可采。
// 覆盖 BTC+ETH；采集失败按所隔离不拖垮整轮（对齐 earn.Collect 先例）。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// dciUnderlyings 起步覆盖面（托管 carry 线 BTC+ETH，票面钦定）。
var dciUnderlyings = []string{"BTC", "ETH"}

// CollectDCI 合并三所 DCI 报价，单所失败 log warn 继续（不拖垮整轮）。
// 回传 per-venue 错误面——D-188② 权限探活复用同批端点结果写 fin_capability_permission
// （禁重调：scanOnce 每轮已调过，重调=API 翻倍撞 429）。
func (c *EarnCollector) CollectDCI(ctx context.Context) ([]pgstore.CexDCIQuote, map[string]error) {
	var out []pgstore.CexDCIQuote
	errs := map[string]error{}
	if rows, err := c.collectBinanceDCI(ctx); err != nil {
		errs["binance"] = err
		log.Printf("[cexscan] binance dci collect: %v", err)
	} else {
		out = append(out, rows...)
	}
	if rows, err := c.collectOKXDCI(ctx); err != nil {
		errs["okx"] = err
		log.Printf("[cexscan] okx dci collect: %v", err)
	} else {
		out = append(out, rows...)
	}
	if rows, err := c.collectGateDCI(ctx); err != nil {
		errs["gate"] = err
		log.Printf("[cexscan] gate dci collect: %v", err)
	} else {
		out = append(out, rows...)
	}
	return out, errs
}

// tenorOf 交割时点→剩余天数（≥1 天取整，0/负=当日记 0）。
func tenorOf(delivery time.Time, now time.Time) int {
	d := int(math.Round(delivery.Sub(now).Hours() / 24))
	if d < 0 {
		return 0
	}
	return d
}

// ---------- Binance：GET {bnSapiURL}/v1/dci/product/list（签名） ----------

// collectBinanceDCIWithBase 可注入 base 的实现（测试用）。
// 实拍实证参数面（-1102 三轮逐参报错）：exercisedCoin+optionType+investCoin 全必传——
// underlying×{underlying,USDT}×{CALL,PUT} 8 调，空组合回空表。
func (c *EarnCollector) collectBinanceDCIWithBase(ctx context.Context, base string) ([]pgstore.CexDCIQuote, error) {
	if c.bnKey == "" {
		return nil, fmt.Errorf("binance key not configured")
	}
	var out []pgstore.CexDCIQuote
	for _, u := range dciUnderlyings {
		// 象限对：(exercised,invest) —— (u,u)/(u,USDT) 买低面 + (USDT,u) 卖高面
		// （D-191 R2：covered-call=持币卖 CALL，exercisedCoin=USDT&investCoin=underlying）。
		for _, pair := range [][2]string{{u, u}, {u, "USDT"}, {"USDT", u}} {
			exercised, inv := pair[0], pair[1]
			for _, opt := range []string{"CALL", "PUT"} {
				ms := time.Now().UnixMilli()
				query := fmt.Sprintf("exercisedCoin=%s&optionType=%s&investCoin=%s&timestamp=%d&recvWindow=5000", exercised, opt, inv, ms)
				url := fmt.Sprintf("%s/v1/dci/product/list?%s&signature=%s", base, query, binanceSign(c.bnSecret, query))
				req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
				if err != nil {
					return nil, err
				}
				req.Header.Set("X-MBX-APIKEY", c.bnKey)
				resp, err := c.client.Do(req)
				if err != nil {
					return nil, fmt.Errorf("binance dci %s/%s/%s request: %w", u, inv, opt, err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					return nil, fmt.Errorf("binance dci %s/%s/%s HTTP %d: %s", u, inv, opt, resp.StatusCode, string(body))
				}
				if len(body) < 400 {
					log.Printf("[cexscan] binance dci %s/%s/%s body: %s", u, inv, opt, string(body))
				} else {
					log.Printf("[cexscan] binance dci %s/%s/%s body head: %.400s", u, inv, opt, string(body))
				}
				rows, err := parseBinanceDCI(body, time.Now())
				if err != nil {
					return nil, fmt.Errorf("binance dci %s/%s/%s: %w", u, inv, opt, err)
				}
				out = append(out, rows...)
			}
		}
	}
	return out, nil
}

func (c *EarnCollector) collectBinanceDCI(ctx context.Context) ([]pgstore.CexDCIQuote, error) {
	return c.collectBinanceDCIWithBase(ctx, c.bnSapiURL)
}

// parseBinanceDCI sapi dci/product/list 响应→行（exerciseCoin=underlying；apy 小数原样）。
func parseBinanceDCI(body []byte, now time.Time) ([]pgstore.CexDCIQuote, error) {
	type bRow struct {
		ID             string      `json:"id"`
		ExerciseCoin   string      `json:"exercisedCoin"` // 实拍实证：exercisedCoin（不是 exerciseCoin）
		InvestCoin     string      `json:"investCoin"`
		StrikePrice    json.Number `json:"strikePrice"`
		SettleDate     json.Number `json:"settleDate"`
		Duration       json.Number `json:"duration"` // 天数直给
		APR            json.Number `json:"apr"`      // 实拍实证：apr（不是 apy），%口径原样
		OptionType     string      `json:"optionType"`
		CanPurchase    bool        `json:"canPurchase"`
		InstrumentName string      `json:"instrumentName"`
		OrderID        json.Number `json:"orderId"`
		// 申购量界三字段（venue SDK 键名实拍校）。
		MinAmount       json.Number `json:"minAmount"`
		MaxAmount       json.Number `json:"maxAmount"`
		PurchaseDecimal json.Number `json:"purchaseDecimal"`
	}
	var raw struct {
		Data []bRow `json:"data"`
		List []bRow `json:"list"` // 实拍实证：продукты под ключом list（{"total":N,"list":[...]}）
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance dci decode: %w", err)
	}
	rows := raw.Data
	if len(rows) == 0 {
		rows = raw.List
	}
	want := map[string]bool{"BTC": true, "ETH": true}
	var out []pgstore.CexDCIQuote
	for _, d := range rows {
		if !d.CanPurchase {
			continue // D-191-R10⑤：不可售档不入候选
		}
		// 卖高象限行 exercisedCoin=USDT&investCoin=BTC——双币任一腿命中即留,
		// underlying 取币腿（D-191 R2；两过滤键任一非稳币腿）。
		under := d.ExerciseCoin
		if !want[under] {
			under = d.InvestCoin
		}
		if !want[under] {
			continue
		}
		apy, err := d.APR.Float64()
		if err != nil {
			log.Printf("[cexscan] binance dci apr parse %q: %v", d.APR.String(), err)
			continue
		}
		row := pgstore.CexDCIQuote{Venue: "binance", Underlying: under,
			InvestCcy: d.InvestCoin, OptType: strings.ToUpper(d.OptionType), APY: apy,
			ProductRef: firstNonEmpty(d.ID, d.InstrumentName),
			OrderRef:   d.OrderID.String()} // D-191-R10②：orderId 解析入库（申购发参源）
		// 申购量界三字段入库（decimal 缺行执行期跳次档）。
		if pd, err := d.PurchaseDecimal.Int64(); err == nil {
			pdv := int(pd)
			row.PurchaseDecimal = &pdv
		}
		if v, err := d.MinAmount.Float64(); err == nil {
			row.MinAmount = &v
		}
		if v, err := d.MaxAmount.Float64(); err == nil {
			row.MaxAmount = &v
		}
		if v, err := d.StrikePrice.Float64(); err == nil {
			row.Strike = &v
		}
		if ms, err := d.SettleDate.Int64(); err == nil && ms > 0 {
			ts := time.UnixMilli(ms)
			td := tenorOf(ts, now)
			row.DeliveryTs, row.TenorDays = &ts, &td
		} else if days, err := d.Duration.Int64(); err == nil {
			td := int(days)
			row.TenorDays = &td
		}
		out = append(out, row)
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------- OKX：GET /api/v5/finance/sfp/dcd/products（签名，baseCcy×optType 组合） ----------

// okxSignPath DCI 签名头：prehash=ts+GET+path?query（okxEarnPath 同法泛化）。
