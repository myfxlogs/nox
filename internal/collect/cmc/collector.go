package cmc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
}

func NewPricesCollector(auth jwtProvider, instrumentID string, store *pgstore.Store, restBase string) *PricesCollector {
	return &PricesCollector{auth: auth, instrumentID: instrumentID, store: store, restBase: restBase}
}

func (c *PricesCollector) Kind() string { return fact.KindCMCPrice }

func (c *PricesCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
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
	return []fact.Fact{{
		Kind: fact.KindCMCPrice, Venue: "cmc", Symbol: "TRXUSD",
		Value: mid, Unit: fact.UnitPrice, Ts: time.Now(), Src: "cmc_rest",
	}}, nil
}

// StartWatchdog 数据新鲜度 watchdog（独立 goroutine，不依赖 Poll 成功）。
// 空表（ErrNoRows）也告警：last 取零值 → Since 巨大 → 触发 warning（v7）。
func (c *PricesCollector) StartWatchdog(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var last time.Time
			if t, err := c.store.LastCMCPriceFetchedAt(ctx); err == nil {
				last = t
			} else if !errors.Is(err, pgx.ErrNoRows) {
				continue // 其他 DB 错误（连接故障）不判 stale
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
}

func NewAccountCollector(auth jwtProvider, accountID int64, store *pgstore.Store, restBase string) *AccountCollector {
	return &AccountCollector{auth: auth, accountID: accountID, store: store, restBase: restBase}
}

func (c *AccountCollector) Kind() string { return fact.KindCMCAccount }

func (c *AccountCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
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
		dbPositions = append(dbPositions, pgstore.CMCPosition{
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
		})
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

func (c *AccountCollector) fetchPositions(ctx context.Context, jwt string) (*positionsJSON, error) {
	url := fmt.Sprintf("%s/v2/accounts/%d/positions", c.restBase, c.accountID)
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

// calcCloseout 计算强平距离（只对 X-AFQUL，SSOT §6 公式）。
// 公式：P_closeout = (C + entry × qty) × L / (qty × (L + 0.5))
// C=cash（summary.total_cash_balance），entry=positions[0].price，qty=positions[0].quantity，
// L=1/margin_percentage（从 API 推导，不硬编码）。
// mid=0 或 err 时 distance=0（不除以 0，closeout_price 仍计算写入）。
func (c *AccountCollector) calcCloseout(ctx context.Context, cash float64, pos *positionsJSON) (closeout, distance float64) {
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
	// mid 从 fin_cmc_prices 最近一条 mid_price 获取
	mid, err := c.store.LastCMCPriceMid(ctx, "X-AFQUL")
	if err != nil || mid == 0 {
		return closeout, 0 // 空表/err → distance=0，closeout 仍计算写入
	}
	distance = (closeout - mid) / mid * 100
	return closeout, distance
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
