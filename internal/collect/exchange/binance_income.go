// D-334ab 位点①：binance income（FUNDING_FEE 实数轨）结算对齐采集器。
// 端点 GET {base}/v1/income?incomeType=FUNDING_FEE&startTime=&limit=1000（USER_DATA，
// HMAC-SHA256 hex 签名 + X-MBX-APIKEY——签名算法各包独立实现先例，exec 包同法）。
// 游标=income_events 该账户 MAX(ts)+1ms（表内派生零新状态，重启安全）；空表首拉=now-24h。
// 幂等=表唯一约束 (venue,account_id,tran_id) ON CONFLICT DO NOTHING；
// 匹配=OPEN 腿唯一命中才填 position_id/leg_no（零/多义留 NULL+warn，不猜不丢）。
package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"arb/internal/store/pgstore"
)

// BinanceIncomeAccount 采集账户（wiring 从 exchange_configs binance perp 行逐轮供给——配置驱动不硬编码 id）。
type BinanceIncomeAccount struct {
	ID        int64
	APIKey    string
	APISecret string
	BaseURL   string
	Simulated bool // D-362 S5：sim 配置事件只配 demo 腿（environment 一致性）
}

// IncomeEventSink income_events 落库/游标/匹配依赖（pgstore.Store 实现；单测用 fake）。
type IncomeEventSink interface {
	MaxIncomeEventTS(ctx context.Context, venue, venueType string) (time.Time, bool, error) // D-496：(venue,venue_type) 键域
	// D-362 S5：venue 传全形（"binance:perp"=腿 venue 口径，修 "binance" 格式不匹配的
	// 结构性死匹配）；env=demo/live 配置环境一致性过滤。
	MatchOpenLegForIncome(ctx context.Context, venue, symbol, env string) (posID int64, legNo int16, hits int, err error)
	InsertIncomeEvents(ctx context.Context, evs []pgstore.IncomeEvent) (int64, error)
}

// BinanceIncomeCollector income 实数轨采集器（S3 结算对齐定时器逐轮触发 RunOnce）。
type BinanceIncomeCollector struct {
	cfg      Config
	sink     IncomeEventSink
	accounts func() []BinanceIncomeAccount
}

// NewBinanceIncomeCollector 构造。accounts 由 wiring 供给（每轮重读 exchange_configs，
// 新配置行无需重启即被覆盖——CexExecutorManager 逐轮注册同款配置驱动姿态）。
func NewBinanceIncomeCollector(cfg Config, sink IncomeEventSink, accounts func() []BinanceIncomeAccount) *BinanceIncomeCollector {
	return &BinanceIncomeCollector{cfg: cfg, sink: sink, accounts: accounts}
}

// binanceIncomeRow fapi/v1/income 响应行（income 字符串、time 毫秒、tranId 数字）。
type binanceIncomeRow struct {
	Symbol     string
	IncomeType string
	Income     float64
	TranID     int64
	Ts         time.Time
}

// RunOnce 对全部 binance 账户跑一轮采集，返回实插入行数（幂等重跑=0）。
func (c *BinanceIncomeCollector) RunOnce(ctx context.Context) (int64, error) {
	var written int64
	if c.accounts == nil {
		return 0, nil
	}
	for _, acc := range c.accounts() {
		if acc.APIKey == "" || acc.APISecret == "" || acc.BaseURL == "" {
			continue // 无凭据/端点行跳过（配置驱动）
		}
		start := time.Now().Add(-24 * time.Hour) // 空表首拉窗口
		if maxTS, ok, err := c.sink.MaxIncomeEventTS(ctx, VenueBinance, "perp"); err != nil {
			fmt.Printf("[income] binance #%d cursor: %v (fallback 24h window)\n", acc.ID, err)
		} else if ok {
			start = maxTS.Add(time.Millisecond)
		}
		rows, err := c.fetchIncome(ctx, acc, start)
		if err != nil {
			fmt.Printf("[income] binance #%d fetch: %v (skip)\n", acc.ID, err)
			continue
		}
		evs := make([]pgstore.IncomeEvent, 0, len(rows))
		for _, r := range rows {
			ev := pgstore.IncomeEvent{
				Venue: VenueBinance, VenueType: "perp", Symbol: r.Symbol,
				IncomeType: r.IncomeType, AmountUSD: r.Income, TranID: r.TranID, Ts: r.Ts,
			}
			// D-362 S5：venue 全形（腿口径）+ 环境一致性（sim 配置事件只配 demo 腿，
			// real 只配 live——income_events.venue 列保持 'binance' 原值，核证点④）。
			env := "live"
			if acc.Simulated {
				env = "demo"
			}
			posID, legNo, hits, err := c.sink.MatchOpenLegForIncome(ctx, VenueBinance+":perp", r.Symbol, env)
			switch {
			case err != nil:
				fmt.Printf("[income] match %s: %v (leave NULL)\n", r.Symbol, err)
			case hits == 1:
				ev.PositionID = &posID
				leg32 := int32(legNo)
				ev.LegNo = &leg32
			case hits > 1:
				fmt.Printf("[income] unmatched %s: %d OPEN legs ambiguous (leave NULL, not guessing)\n", r.Symbol, hits)
			default:
				fmt.Printf("[income] unmatched %s: no OPEN leg (leave NULL)\n", r.Symbol)
			}
			evs = append(evs, ev)
		}
		n, err := c.sink.InsertIncomeEvents(ctx, evs)
		if err != nil {
			fmt.Printf("[income] binance #%d insert: %v (skip)\n", acc.ID, err)
			continue
		}
		written += n
		fmt.Printf("[income] binance #%d: fetched=%d inserted=%d\n", acc.ID, len(rows), n)
	}
	return written, nil
}

// fetchIncome 拉一页 income（limit 1000；溢出部分下一轮游标收敛）。
func (c *BinanceIncomeCollector) fetchIncome(ctx context.Context, acc BinanceIncomeAccount, start time.Time) ([]binanceIncomeRow, error) {
	params := fmt.Sprintf("incomeType=FUNDING_FEE&startTime=%d&limit=1000&timestamp=%d&recvWindow=5000",
		start.UnixMilli(), time.Now().UnixMilli())
	sig := binanceIncomeSign(acc.APISecret, params)
	url := fmt.Sprintf("%s/v1/income?%s&signature=%s", acc.BaseURL, params, sig)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", acc.APIKey)
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(data))
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	out := make([]binanceIncomeRow, 0, len(raw))
	for _, m := range raw {
		var row binanceIncomeRow
		row.Symbol, _ = m["symbol"].(string)
		row.IncomeType, _ = m["incomeType"].(string)
		fmt.Sscanf(fmt.Sprint(m["income"]), "%f", &row.Income)
		if ft, ok := m["time"].(float64); ok {
			row.Ts = time.UnixMilli(int64(ft))
		}
		if tid, ok := m["tranId"].(float64); ok {
			row.TranID = int64(tid)
		}
		if row.Symbol == "" || row.Ts.IsZero() || row.TranID == 0 {
			continue // 字段缺失行丢弃（无幂等锚不落库）
		}
		out = append(out, row)
	}
	return out, nil
}

// binanceIncomeSign HMAC-SHA256 hex 签名（与 exec.binanceSign 同法；各包独立实现先例）。
func binanceIncomeSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
