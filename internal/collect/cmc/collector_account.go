// CMC AccountCollector（从 collector.go 拆出，保 collector.go ≤300 行）。
package cmc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

type AccountCollector struct {
	auth      jwtProvider
	accountID int64
	store     *pgstore.Store
	restBase  string
	// wsHealthy 注入 ws 健康查询（wire 层回填）；nil=无 ws，照旧 REST 轮询。
	wsHealthy func() bool
}

func NewAccountCollector(auth jwtProvider, accountID int64, store *pgstore.Store, restBase string) *AccountCollector {
	return &AccountCollector{auth: auth, accountID: accountID, store: store, restBase: restBase}
}

// SetWSHealthy 注入 ws 健康查询函数（wire 层回填）。
func (c *AccountCollector) SetWSHealthy(fn func() bool) { c.wsHealthy = fn }

func (c *AccountCollector) Kind() string { return fact.KindCMCAccount }

func (c *AccountCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// ws 健康时短路：REST 兜底降级（健康期账户 REST 调用 = 0）
	if c.wsHealthy != nil && c.wsHealthy() {
		return nil, nil
	}
	jwt, err := c.auth.JWT(ctx)
	if err != nil {
		return nil, fmt.Errorf("cmc account JWT: %w", err)
	}
	// 1. summary
	summary, err := c.fetchSummary(ctx, jwt)
	if err != nil {
		return nil, fmt.Errorf("cmc account summary: %w", err)
	}
	// 2. positions
	pos, err := c.fetchPositions(ctx, jwt)
	if err != nil {
		return nil, fmt.Errorf("cmc account positions: %w", err)
	}
	// 3. 强平距离计算（只对 X-AFQUL）
	cash := parseFloat(summary.TotalCashBalance)
	closeout, distance := c.calcCloseout(ctx, cash, pos)
	// 4. 写入（已知限制 v7：两 INSERT 非同事务，fetch 阶段任一失败不写已覆盖；写阶段第二笔失败留单侧快照，容忍）
	now := time.Now()
	if err := c.store.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID:                c.accountID,
		TotalAccountValue:        parseFloat(summary.TotalAccountValue),
		TotalCashBalance:         cash,
		TotalUnrealisedPnL:       parseFloat(summary.TotalUnrealisedPnL),
		TotalEquityFreeForMargin: parseFloat(summary.TotalEquityFreeForMargin),
		TotalEquityUsedInMargin:  parseFloat(summary.TotalEquityUsedInMargin),
		CloseoutPrice:            closeout,
		DistanceToCloseoutPct:    distance,
		FetchedAt:                now,
	}); err != nil {
		return nil, fmt.Errorf("cmc account insert summary: %w", err)
	}
	var dbPositions []pgstore.CMCPosition
	for _, p := range pos.Positions {
		dp := pgstore.CMCPosition{
			AccountID:          c.accountID,
			InstrumentID:       p.InstrumentID,
			Direction:          p.Direction,
			Quantity:           parseFloat(p.Quantity),
			EntryPrice:         parseFloat(p.Price),
			UnrealisedPnL:      parseFloat(p.UnrealisedPnLInPrimaryCurrency),
			MarginAmount:       parseFloat(p.MarginAmount),
			MarginPercentage:   parseFloat(p.StandardMarginPercentage),
			EvaluationBidPrice: parseFloat(p.EvaluationBidPrice),
			EvaluationAskPrice: parseFloat(p.EvaluationAskPrice),
			FetchedAt:          now,
		}
		// D-208 v2-A：解析 orders[0]——submittedTimestamp→opened_at、orderId→order_id。
		// RFC3339 可能含 7 位小数秒，time.Parse 宽容处理（标准库支持最多 9 位）。
		if len(p.Orders) > 0 {
			dp.OrderID = p.Orders[0].OrderID
			if t, err := parseRFC3339Loose(p.Orders[0].SubmittedTimestamp); err == nil {
				dp.OpenedAt = &t
			}
		}
		dbPositions = append(dbPositions, dp)
	}
	if err := c.store.InsertCMCPositions(ctx, c.accountID, dbPositions); err != nil {
		return nil, fmt.Errorf("cmc account insert positions: %w", err)
	}
	return []fact.Fact{{
		Kind: fact.KindCMCAccount, Venue: "cmc", Symbol: "summary",
		Value: parseFloat(summary.TotalAccountValue), Unit: fact.UnitUSD, Ts: now, Src: "cmc_rest",
	}}, nil
}

func (c *AccountCollector) fetchSummary(ctx context.Context, jwt string) (*accountSummaryJSON, error) {
	url := fmt.Sprintf("%s/v2/accounts/%d/value/summary", c.restBase, c.accountID)
	body, err := cmcGet(ctx, url, jwt)
	if err != nil {
		return nil, err
	}
	var s accountSummaryJSON
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("unmarshal summary: %w", err)
	}
	return &s, nil
}

// fetchPositionsJSON 包级函数：REST 拉取全量 positions（WsClient 播种 + AccountCollector 共用，R3）。
func fetchPositionsJSON(ctx context.Context, jwt, restBase string, accountID int64) (*positionsJSON, error) {
	url := fmt.Sprintf("%s/v2/accounts/%d/positions", restBase, accountID)
	body, err := cmcGet(ctx, url, jwt)
	if err != nil {
		return nil, err
	}
	var p positionsJSON
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("unmarshal positions: %w", err)
	}
	return &p, nil
}

func (c *AccountCollector) fetchPositions(ctx context.Context, jwt string) (*positionsJSON, error) {
	return fetchPositionsJSON(ctx, jwt, c.restBase, c.accountID)
}

// calcCloseoutValues 包级纯计算函数：强平距离（只对 X-AFQUL，SSOT §6 公式）。
// D-225 v2.1：参数 mid → px（平仓方向价，short=ask；公式本体不动，硬约束 7）。
// px=0 时 distance=0（不除以 0，closeout_price 仍计算写入）。
func calcCloseoutValues(cash float64, pos *positionsJSON, px float64) (closeout, distance float64) {
	var afqul *positionJSON
	for i := range pos.Positions {
		if pos.Positions[i].InstrumentID == "X-AFQUL" {
			afqul = &pos.Positions[i]
			break
		}
	}
	if afqul == nil {
		return 0, 0
	}
	marginPct := parseFloat(afqul.StandardMarginPercentage)
	if marginPct == 0 {
		return 0, 0
	}
	L := 1 / marginPct
	entry := parseFloat(afqul.Price)
	qty := parseFloat(afqul.Quantity)
	if qty == 0 {
		return 0, 0
	}
	closeout = (cash + entry*qty) * L / (qty * (L + 0.5))
	if px == 0 {
		return closeout, 0
	}
	distance = (closeout - px) / px * 100
	return closeout, distance
}

// calcCloseout 方法薄壳：查询方向平仓价后调包级 calcCloseoutValues（D-225 v2.1：LastCMCPriceForSide）。
func (c *AccountCollector) calcCloseout(ctx context.Context, cash float64, pos *positionsJSON) (closeout, distance float64) {
	px, _, _, err := c.store.LastCMCPriceForSide(ctx, "X-AFQUL", "Sell") // short → ask（F11 保守修正）
	if err != nil {
		px = 0
	}
	return calcCloseoutValues(cash, pos, px)
}

// cmcGet REST GET 辅助（connectapi 端点头只有两个：Authorization + Origin）。
func cmcGet(ctx context.Context, url, jwt string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Origin", "https://platform.cmcmarkets.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
