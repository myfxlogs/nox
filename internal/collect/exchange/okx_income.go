// D-385 S2：okx bills（资金费 subType=173 支出/174 收入）→ income_events 采集器。
// 仿 BinanceIncomeCollector 同构（接口复用 IncomeEventSink）：游标=表内 MAX(ts)、
// 幂等=ON CONFLICT (venue,account_id,tran_id)、MatchOpenLegForIncome 唯一命中才挂腿。
// 端点 GET /api/v5/account/bills?limit=100（近 7 天有界；R0-A1：不带 instType 参数）；
// 签名 prehash=ts+GET+path?query（KB okx-post-signature-includes-body：query 必须进签名）；
// balChg 自带符号（正=收）直写 amount_usd（USDT≈USD 口径与 binance 轨一致，偏差<0.1%）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"arb/internal/store/pgstore"
)

// OKXIncomeAccount 采集账户（wiring 从 exchange_configs okx perp 行逐轮供给——配置驱动）。
// OKX 签名需 passphrase，binance 无此字段——独立结构体不复用 BinanceIncomeAccount。
type OKXIncomeAccount struct {
	ID         int64
	APIKey     string
	APISecret  string
	Passphrase string
	BaseURL    string
	Simulated  bool // demo 账户带 x-simulated-trading:1，匹配 env=demo 腿（D-362 S5 环境一致性）
}

// OKXIncomeCollector okx bills 采集器（15min 固定轮询由 wiring 驱动，弃结算对齐——
// 账本归因容忍分钟级延迟，per-symbol 周期需额外 public 拉取，为对齐多花一套缓存不值得）。
type OKXIncomeCollector struct {
	cfg      Config
	sink     IncomeEventSink
	accounts func() []OKXIncomeAccount
}

// NewOKXIncomeCollector 构造。
func NewOKXIncomeCollector(cfg Config, sink IncomeEventSink, accounts func() []OKXIncomeAccount) *OKXIncomeCollector {
	return &OKXIncomeCollector{cfg: cfg, sink: sink, accounts: accounts}
}

// okxBillRow /api/v5/account/bills 行（字段名以 OKX v5 官方文档+实测为准）。
type okxBillRow struct {
	BillID  string `json:"billId"`  // 数值字符串
	InstID  string `json:"instId"`  // 与 leg.symbol 同形（XAU-USDT-SWAP），无需 canonical 转换
	SubType string `json:"subType"` // 173=funding 支出 / 174=funding 收入
	BalChg  string `json:"balChg"`  // 余额变更，自带符号（正=收），币种由 ccy 决定
	Ccy     string `json:"ccy"`     // balChg 币种（R0-A2：非 USDT 不按 USD 记账）
	Ts      string `json:"ts"`      // ms
}

// RunOnce 对全部 okx 账户跑一轮采集，返回实插入行数（幂等重跑=0）。
func (c *OKXIncomeCollector) RunOnce(ctx context.Context) (int64, error) {
	var written int64
	if c.accounts == nil {
		return 0, nil
	}
	for _, acc := range c.accounts() {
		if acc.APIKey == "" || acc.APISecret == "" || acc.Passphrase == "" || acc.BaseURL == "" {
			continue // 无凭据/端点行跳过（配置驱动）
		}
		var cursor time.Time
		if maxTS, ok, err := c.sink.MaxIncomeEventTS(ctx, VenueOKX, acc.ID); err != nil {
			fmt.Printf("[income] okx #%d cursor: %v (first page only)\n", acc.ID, err)
		} else if ok {
			cursor = maxTS
		}
		var rows []okxBillRow
		// 停机>7d 空洞修补（疑义点）：cursor 早于 bills 端点 7 天窗口时，先走
		// bills-archive（3 月窗口）把 >cursor 的旧账补齐，再走正常端点；幂等键兜底重叠。
		if !cursor.IsZero() && cursor.Before(time.Now().Add(-7*24*time.Hour)) {
			arch, err := c.fetchBills(ctx, acc, cursor, "/api/v5/account/bills-archive")
			if err != nil {
				fmt.Printf("[income] okx #%d bills-archive: %v (gap left, continue normal window)\n", acc.ID, err)
			} else {
				rows = arch
			}
		}
		fresh, err := c.fetchBills(ctx, acc, cursor, "/api/v5/account/bills")
		if err != nil {
			fmt.Printf("[income] okx #%d fetch: %v (skip)\n", acc.ID, err)
			continue
		}
		rows = append(rows, fresh...)
		env := "live"
		if acc.Simulated {
			env = "demo"
		}
		evs := make([]pgstore.IncomeEvent, 0, len(rows))
		for _, r := range rows {
			tranID, err := strconv.ParseInt(r.BillID, 10, 64)
			if err != nil {
				fmt.Printf("[income] okx #%d non-numeric billId %q (drop)\n", acc.ID, r.BillID)
				continue
			}
			if r.Ccy != "USDT" {
				// R0-A2：balChg 币种由 ccy 决定，非 USDT 不按 USD 记账（log+丢弃不写死假设）
				fmt.Printf("[income] okx #%d bill %s ccy=%q non-USDT (drop)\n", acc.ID, r.BillID, r.Ccy)
				continue
			}
			amt, _ := strconv.ParseFloat(r.BalChg, 64)
			tsMs, _ := strconv.ParseInt(r.Ts, 10, 64)
			ev := pgstore.IncomeEvent{
				Venue: VenueOKX, VenueType: "perp", ConfigID: acc.ID, Symbol: r.InstID,
				IncomeType: "FUNDING_FEE", AmountUSD: amt, TranID: tranID,
				Ts: time.UnixMilli(tsMs),
			}
			// 匹配 OPEN 腿唯一命中才挂 position_id/leg_no（零/多义留 NULL，照抄 binance 语义）。
			posID, legNo, hits, err := c.sink.MatchOpenLegForIncome(ctx, VenueOKX+":perp", r.InstID, env)
			if err != nil || hits != 1 {
				posID, legNo = 0, 0 // R0-B3：hits≠1 显式清零（MatchOpenLegForIncome 多义时仍返回末条，不依赖返回值防误挂）
			}
			switch {
			case err != nil:
				fmt.Printf("[income] match %s: %v (leave NULL)\n", r.InstID, err)
			case hits == 1:
				ev.PositionID = &posID
				leg32 := int32(legNo)
				ev.LegNo = &leg32
			case hits > 1:
				fmt.Printf("[income] unmatched %s: %d OPEN legs ambiguous (leave NULL, not guessing)\n", r.InstID, hits)
			default:
				fmt.Printf("[income] unmatched %s: no OPEN leg (leave NULL)\n", r.InstID)
			}
			evs = append(evs, ev)
		}
		n, err := c.sink.InsertIncomeEvents(ctx, evs)
		if err != nil {
			fmt.Printf("[income] okx #%d insert: %v (skip)\n", acc.ID, err)
			continue
		}
		written += n
		fmt.Printf("[income] okx #%d: fetched=%d inserted=%d\n", acc.ID, len(rows), n)
	}
	return written, nil
}

// fetchBills 拉指定账单端点（bills=近 7 天 / bills-archive=近 3 月；响应按 billId 倒序，
// after=本页最旧 billId 续拉更早页——R0-A3 实盘实证 after=<max billId> 返回更旧行）。
// 不带 instType 请求参数（R0-A1 裁定：账户级行 instType 可为空，该维度不成立；
// subType∈{173,174} 客户端过滤）。终止=本页最旧行 ts≤cursor 或空页或 3 页上限——
// 3 页上限是保护不是常态（R0-A4）：触顶打 critical，已采行照常插入（部分覆盖>零）。
func (c *OKXIncomeCollector) fetchBills(ctx context.Context, acc OKXIncomeAccount, cursor time.Time, endpoint string) ([]okxBillRow, error) {
	var out []okxBillRow
	after := ""
	capped := true
	for page := 0; page < 3; page++ {
		path := endpoint + "?limit=100"
		if after != "" {
			path += "&after=" + after
		}
		body, err := c.signedGet(ctx, acc, path)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Code string       `json:"code"`
			Msg  string       `json:"msg"`
			Data []okxBillRow `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("okx bills json: %w", err)
		}
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx bills: code=%s msg=%s", resp.Code, resp.Msg)
		}
		if len(resp.Data) == 0 {
			capped = false
			break
		}
		oldest := resp.Data[len(resp.Data)-1]
		oldestMs, _ := strconv.ParseInt(oldest.Ts, 10, 64)
		for _, r := range resp.Data {
			if r.SubType != "173" && r.SubType != "174" {
				continue
			}
			tsMs, _ := strconv.ParseInt(r.Ts, 10, 64)
			if !cursor.IsZero() && tsMs <= cursor.UnixMilli() {
				continue
			}
			out = append(out, r)
		}
		if !cursor.IsZero() && oldestMs <= cursor.UnixMilli() {
			capped = false
			break
		}
		after = oldest.BillID
	}
	if capped {
		log.Printf("[income] okx #%d pagination cap hit（3页上限），缺口方向=更旧段，可事后回填", acc.ID)
	}
	return out, nil
}

// signedGet okx 签名 GET（OK-ACCESS 四头 + sim 账户 x-simulated-trading:1）。
// 签名 prehash 含 path?query 全串（KB okx-post-signature-includes-body）。
// 与 cex_io.signedOkxGet 同法但带 simulated 头（cex_io 只采 real 行无此需求，不共享改它）。
func (c *OKXIncomeCollector) signedGet(ctx context.Context, acc OKXIncomeAccount, pathWithQuery string) ([]byte, error) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxHMACBase64(acc.APISecret, ts+"GET"+pathWithQuery)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, acc.BaseURL+pathWithQuery, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("OK-ACCESS-KEY", acc.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", acc.Passphrase)
	req.Header.Set("Content-Type", "application/json")
	if acc.Simulated {
		req.Header.Set("x-simulated-trading", "1")
	}
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
