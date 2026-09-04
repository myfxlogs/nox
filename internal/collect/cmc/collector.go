package cmc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"

	"github.com/jackc/pgx/v5"
)

// jwtProvider 抽象 JWT 获取（AuthManager 实现；测试用 mockAuth，提示词 S7 要求 mock AuthManager）。
type jwtProvider interface {
	JWT(ctx context.Context) (string, error)
}

// ---------- PricesCollector ----------

type PricesCollector struct {
	auth         jwtProvider
	instrumentID string
	restBase     string
	store        *pgstore.Store
	// 门控缓存（S4）：Poll 成功后缓存 market_status + 探测时刻；休市 15min 内短路不发 REST。
	lastStatus string
	lastProbe  time.Time
}

func NewPricesCollector(auth jwtProvider, instrumentID string, store *pgstore.Store, restBase string) *PricesCollector {
	return &PricesCollector{auth: auth, instrumentID: instrumentID, store: store, restBase: restBase}
}

func (c *PricesCollector) Kind() string { return fact.KindCMCPrice }

func (c *PricesCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// 门控：休市且距上次探测 <15min → 直接短路（不发 REST；周末/日结请求 1440/天→~96/天）
	if strings.EqualFold(c.lastStatus, "closed") && time.Since(c.lastProbe) < 15*time.Minute {
		return nil, nil
	}
	jwt, err := c.auth.JWT(ctx)
	if err != nil {
		return nil, fmt.Errorf("cmc price JWT: %w", err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET",
		c.restBase+"/v1/markets/prices/"+c.instrumentID, nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Origin", "https://platform.cmcmarkets.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cmc price request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cmc price: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cmc price read: %w", err)
	}
	var p priceJSON
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("cmc price unmarshal: %w", err)
	}
	quoteTime, _ := time.Parse(time.RFC3339, p.Time)
	mid := parseFloat(p.MidPrice)
	rec := pgstore.CMCPrice{
		InstrumentID: c.instrumentID,
		BidPrice:     parseFloat(p.BidPrice),
		AskPrice:     parseFloat(p.AskPrice),
		MidPrice:     mid,
		MarketStatus: p.MarketStatus,
		QuoteTime:    quoteTime,
		FetchedAt:    time.Now(),
	}
	if err := c.store.InsertCMCPrice(ctx, rec); err != nil {
		return nil, fmt.Errorf("cmc price insert: %w", err)
	}
	// 门控缓存更新（S4）：成功后记录 status + 探测时刻
	c.lastStatus = p.MarketStatus
	c.lastProbe = time.Now()
	return []fact.Fact{{
		Kind: fact.KindCMCPrice, Venue: "cmc", Symbol: "TRXUSD",
		Value: mid, Unit: fact.UnitPrice, Ts: time.Now(), Src: "cmc_rest",
	}}, nil
}

// StartWatchdog 数据新鲜度 watchdog（独立 goroutine，不依赖 Poll 成功）。
// 空表（ErrNoRows）也告警：last 取零值 → Since 巨大 → 触发 warning（v7）。
// v1.2 R'-1：门控使休市段更新间隔变 15min > 现行 stale 阈值 5min → 休市期误报。
// 修法：判定前查价格行最近 market_status——closed（EqualFold）则本轮跳过 stale 判定。
func (c *PricesCollector) StartWatchdog(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, last, err := c.store.LastCMCPriceStatusAndFetchedAt(ctx)
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					continue // 其他 DB 错误（连接故障）不判 stale
				}
				// 空表：last 取零值 → Since 巨大 → 触发 warning（v7 行为保留）
			} else if strings.EqualFold(status, "closed") {
				continue // 休市：门控使更新间隔变 15min，跳过 stale 判定（v1.2 R'-1）
			}
			if time.Since(last) > 5*time.Minute {
				log.Printf("[cmc] stale warning: no price update for %v", time.Since(last).Round(time.Second))
			}
		}
	}
}

// ---------- AccountCollector ----------

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
// mid 由调用方查询 LastCMCPriceMid 传入（零依赖，WsClient + AccountCollector 共用）。
// mid=0 时 distance=0（不除以 0，closeout_price 仍计算写入）。
func calcCloseoutValues(cash float64, pos *positionsJSON, mid float64) (closeout, distance float64) {
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
	if mid == 0 {
		return closeout, 0
	}
	distance = (closeout - mid) / mid * 100
	return closeout, distance
}

// calcCloseout 方法薄壳：查询 mid 后调包级 calcCloseoutValues（行为不变，D-208 复算口径勿动）。
func (c *AccountCollector) calcCloseout(ctx context.Context, cash float64, pos *positionsJSON) (closeout, distance float64) {
	mid, err := c.store.LastCMCPriceMid(ctx, "X-AFQUL")
	if err != nil {
		mid = 0
	}
	return calcCloseoutValues(cash, pos, mid)
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

// ---------- HistoryCollector 见 history.go ----------
