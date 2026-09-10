// D-271 S5 调度器：定时轮询 scanOnce 全流程。
package cexscan

import (
	"context"
	"log"
	"net/http"
	"time"

	"arb/internal/config"
	"arb/internal/httpx"
	"arb/internal/store/pgstore"
)

// Scanner CEX 只读扫描器调度器。
type Scanner struct {
	cfg      *config.Config
	store    *pgstore.Store
	accounts AccountSnapshotter
	earn     *EarnCollector
	alert    *AlertEvaluator
	client   *http.Client
	bnAPIURL string
	symbols  []string
}

// NewScanner 创建扫描器。
func NewScanner(cfg *config.Config, store *pgstore.Store, am AccountSnapshotter, exchs []pgstore.ExchangeConfig) *Scanner {
	earn := NewEarnCollector(cfg, exchs)
	bnAPIURL := ""
	for _, e := range exchs {
		if e.Venue == "binance" && e.VenueType == "spot" && !e.Simulated {
			bnAPIURL = e.BaseURL
			break
		}
	}
	return &Scanner{
		cfg:      cfg,
		store:    store,
		accounts: am,
		earn:     earn,
		alert:    NewAlertEvaluator(cfg, store),
		client:   httpx.Client(),
		bnAPIURL: bnAPIURL,
		symbols:  cfg.CexScanSymbols,
	}
}

// Run 启动即扫一次 → ticker(cfg.CexScanInterval) → scanOnce。
func (s *Scanner) Run(ctx context.Context) {
	// 启动即扫一次
	if err := s.scanOnce(ctx); err != nil {
		log.Printf("[cexscan] initial scan: %v", err)
	}
	ticker := time.NewTicker(s.cfg.CexScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.scanOnce(ctx); err != nil {
				log.Printf("[cexscan] scan: %v", err)
			}
		}
	}
}

// scanOnce 串行 ①ScanAccounts ②ScanFundingRates ③earn.Collect→InsertCexEarnAPR ④CalcSpreads→InsertCexSpread ⑤alert.Evaluate。
func (s *Scanner) scanOnce(ctx context.Context) error {
	// ① 账户扫描（纯读缓存，不报错）
	_ = ScanAccounts(s.accounts)

	// ② Funding 扫描（读库）
	fundings, err := ScanFundingRates(ctx, s.store)
	if err != nil {
		log.Printf("[cexscan] funding scan: %v", err)
	}

	// ③ 理财 APR 采集 → 入库
	earnAPRs, err := s.earn.Collect(ctx)
	if err != nil {
		log.Printf("[cexscan] earn collect: %v", err)
	} else {
		for i := range earnAPRs {
			if err := s.store.InsertCexEarnAPR(ctx, &earnAPRs[i]); err != nil {
				log.Printf("[cexscan] insert earn apr %s/%s: %v", earnAPRs[i].Venue, earnAPRs[i].Ccy, err)
			}
		}
	}

	// ④ 跨所价差 → 入库
	spreads, err := CalcSpreads(ctx, s.client, s.bnAPIURL, s.symbols)
	if err != nil {
		log.Printf("[cexscan] calc spreads: %v", err)
	} else {
		for i := range spreads {
			row := &pgstore.CexSpread{
				Symbol:    spreads[i].Symbol,
				VenueBuy:  spreads[i].VenueBuy,
				VenueSell: spreads[i].VenueSell,
				PriceBuy:  spreads[i].PriceBuy,
				PriceSell: spreads[i].PriceSell,
				SpreadBps: spreads[i].SpreadBps,
			}
			if err := s.store.InsertCexSpread(ctx, row); err != nil {
				log.Printf("[cexscan] insert spread %s: %v", spreads[i].Symbol, err)
			}
		}
	}

	// ⑤ 告警评估
	if err := s.alert.Evaluate(ctx, earnAPRs, spreads, fundings); err != nil {
		return err
	}
	return nil
}
