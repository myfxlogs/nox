// D-271 S5 告警评估：阈值判断 + InsertAlert + 去重（5 分钟 TTL）。
package cexscan

import (
	"context"
	"fmt"
	"sync"
	"time"

	"arb/internal/config"
	"arb/internal/store/pgstore"
)

// AlertEvaluator 告警评估器（去重 map[string]time.Time，TTL 5 分钟）。
type AlertEvaluator struct {
	cfg       *config.Config
	store     *pgstore.Store
	lastAlert map[string]time.Time
	mu        sync.Mutex
}

// NewAlertEvaluator 创建告警评估器。
func NewAlertEvaluator(cfg *config.Config, store *pgstore.Store) *AlertEvaluator {
	return &AlertEvaluator{
		cfg:       cfg,
		store:     store,
		lastAlert: make(map[string]time.Time),
	}
}

// Evaluate 评估理财 APR + 跨所价差 + Funding，超阈值 → InsertAlert("warn", ...)。
func (e *AlertEvaluator) Evaluate(ctx context.Context, earnAPRs []pgstore.CexEarnAPR, spreads []SpreadResult, fundings []FundingEntry) error {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()

	// 理财 APR：apr >= cfg.CexScanEarnAprMin → 告警
	for _, a := range earnAPRs {
		if a.APR >= e.cfg.CexScanEarnAprMin {
			key := "earn:" + a.Venue + ":" + a.Ccy
			if !e.shouldAlert(key, now) {
				continue
			}
			msg := fmt.Sprintf("[CEX理财] %s %s Flexible %.2f%%", a.Venue, a.Ccy, a.APR*100)
			if err := e.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
				return fmt.Errorf("insert earn alert: %w", err)
			}
		}
	}

	// 跨所价差：spreadBps >= cfg.CexScanSpreadBps → 告警
	for _, s := range spreads {
		if s.SpreadBps >= e.cfg.CexScanSpreadBps {
			key := "spread:" + s.Symbol
			if !e.shouldAlert(key, now) {
				continue
			}
			msg := fmt.Sprintf("[CEX价差] %s %s→%s %.1fbps", s.Symbol, s.VenueBuy, s.VenueSell, s.SpreadBps)
			if err := e.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
				return fmt.Errorf("insert spread alert: %w", err)
			}
		}
	}

	// Funding：rate*100 >= cfg.CexScanFundingAprMin → 告警
	for _, f := range fundings {
		pct := f.Rate * 100
		if pct >= e.cfg.CexScanFundingAprMin {
			key := "funding:" + f.Venue + ":" + f.Symbol
			if !e.shouldAlert(key, now) {
				continue
			}
			msg := fmt.Sprintf("[CEX funding] %s %s %.2f%%/yr", f.Venue, f.Symbol, pct)
			if err := e.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
				return fmt.Errorf("insert funding alert: %w", err)
			}
		}
	}
	return nil
}

// shouldAlert 检查去重（5 分钟 TTL），未过期返回 false。
func (e *AlertEvaluator) shouldAlert(key string, now time.Time) bool {
	last, ok := e.lastAlert[key]
	if ok && now.Sub(last) < 5*time.Minute {
		return false
	}
	e.lastAlert[key] = now
	return true
}
