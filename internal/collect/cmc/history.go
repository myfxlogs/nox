package cmc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// HistoryCollector 采集 CMC 交易历史（含每日隔夜利息 OVERNIGHT_SETTLEMENT）。
// 增量拉取：LastCMCHistoryTime + from=last+1s，防 facts 表重复累积。
type HistoryCollector struct {
	auth      jwtProvider
	accountID int64
	store     *pgstore.Store
	restBase  string
}

func NewHistoryCollector(auth jwtProvider, accountID int64, store *pgstore.Store, restBase string) *HistoryCollector {
	return &HistoryCollector{auth: auth, accountID: accountID, store: store, restBase: restBase}
}

func (c *HistoryCollector) Kind() string { return fact.KindCMCHistory }

func (c *HistoryCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	jwt, err := c.auth.JWT(ctx)
	if err != nil {
		return nil, fmt.Errorf("cmc history JWT: %w", err)
	}
	lastTs, err := c.store.LastCMCHistoryTime(ctx, c.accountID)
	if err != nil {
		return nil, fmt.Errorf("cmc history last time: %w", err)
	}
	from := lastTs
	if from.IsZero() {
		from = time.Now().Add(-30 * 24 * time.Hour) // 首次回填窗口
	} else {
		from = from.Add(time.Second) // 增量：last+1s
	}
	to := time.Now()
	// from/to 格式未实证（无抓包证据）——按 UnixMilli 毫秒实现（JS SPA 惯例）。
	// F4 复审真实凭据首验。
	url := fmt.Sprintf("%s/v1/accounts/%d/history?from=%d&to=%d&limit=50",
		c.restBase, c.accountID, from.UnixMilli(), to.UnixMilli())
	body, err := cmcGet(ctx, url, jwt)
	if err != nil {
		return nil, fmt.Errorf("cmc history request: %w", err)
	}
	var h historyJSON
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("cmc history unmarshal: %w", err)
	}
	now := time.Now()
	var facts []fact.Fact
	var txs []pgstore.CMCHistoryTx
	for _, t := range h.Transactions {
		if t.TransactionType != "OVERNIGHT_SETTLEMENT" {
			continue
		}
		txTime, _ := time.Parse(time.RFC3339, t.TransactionTime)
		amount := parseFloat(t.Amount)
		facts = append(facts, fact.Fact{
			Kind: fact.KindCMCHistory, Venue: "cmc", Symbol: "swap",
			Value: amount, Unit: fact.UnitUSD, Ts: txTime, Src: "cmc_rest",
		})
		txs = append(txs, pgstore.CMCHistoryTx{
			AccountID:       c.accountID,
			TransactionID:   t.TransactionID,
			TransactionType: t.TransactionType,
			Amount:          amount,
			Balance:         parseFloat(t.Balance),
			Currency:        t.Currency,
			OrderID:         t.OrderID,
			TransactionTime: txTime,
			FetchedAt:       now,
		})
	}
	if err := c.store.UpsertCMCHistory(ctx, c.accountID, txs); err != nil {
		return nil, fmt.Errorf("cmc history upsert: %w", err)
	}
	return facts, nil
}
