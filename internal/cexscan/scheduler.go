// D-271 S5 调度器：定时轮询 scanOnce 全流程。
package cexscan

import (
	"context"
	"log"
	"net/http"
	"time"

	"arb/internal/config"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/httpx"
	"arb/internal/store/pgstore"
)

// Scanner CEX 只读扫描器调度器。
type Scanner struct {
	cfg      *config.Config
	store    *pgstore.Store
	accounts AccountSnapshotter
	earn     *EarnCollector
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

// scanOnce 串行 ①ScanAccounts ②earn.Collect→InsertCexEarnAPR ③CalcSpreads→InsertCexSpread ④earn APR 判断。
// D-271 R3：funding 扫描已移除——现有 engine/evaluator 管线已覆盖 funding 机会分析。
// D-271 R5：理财 APR + 价差告警已移除——理财告警是毛数据噪音（280 条实证），
// 价差告警现有 CrossVenueDetector 已事件驱动覆盖。保留采集入库供前端展示。
// D-273 ④：earn APR 判断结果 upsert 入库（fin_cex_earn_judgment），供前端展示，不发告警。
func (s *Scanner) scanOnce(ctx context.Context) error {
	// ① 账户扫描（纯读缓存，不报错）→ D-358 S2：快照落库（append 语义，喂覆盖率面板；
	// 失败只记日志不阻断采集，同②③惯例）。
	snaps := ScanAccounts(s.accounts)
	if len(snaps) > 0 {
		rows := make([]pgstore.VenueAccountSnapshot, len(snaps))
		for i, sn := range snaps {
			rows[i] = pgstore.VenueAccountSnapshot{
				Venue: sn.Venue, VenueType: sn.VenueType,
				BalanceRaw: sn.Balance, EquityRaw: sn.Equity, FreeMarginRaw: sn.FreeMargin,
				Currency: sn.Currency, FetchedAt: sn.Ts,
			}
		}
		if err := s.store.InsertVenueAccountSnapshots(ctx, rows); err != nil {
			log.Printf("[cexscan] insert venue account snapshots: %v", err)
		}
	}

	// ② 理财 APR 采集 → 入库
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

	// ③ 跨所价差 → 入库
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

	// ④ earn APR 判断（D-273）：只依赖本币余额明细与逆回购基准，失败只记日志不阻断采集。
	holdings := buildHoldingsFromSnapshot(s.accounts.Snapshot())
	judge := NewEarnJudge(s.store, holdings)
	judgments, err := judge.Judge(ctx, earnAPRs)
	if err != nil {
		log.Printf("[cexscan] earn judge: %v", err)
	} else {
		for i := range judgments {
			row := &pgstore.CexEarnJudgment{
				Venue:       judgments[i].Venue,
				Ccy:         judgments[i].Ccy,
				ProductType: judgments[i].ProductType,
				APR:         judgments[i].APR,
				Category:    judgments[i].Category,
				Verdict:     judgments[i].Verdict,
				Reasons:     toJSONString(judgments[i].Reasons),
			}
			if err := s.store.UpsertCexEarnJudgment(ctx, row); err != nil {
				log.Printf("[cexscan] upsert earn judgment %s/%s: %v", judgments[i].Venue, judgments[i].Ccy, err)
			}
		}
	}
	return nil
}

// buildHoldingsFromSnapshot 从账户快照提取现货持币明细（D-273 S4）。
// 账户范围三条件过滤：CEX + 现货 + 非模拟——perp 余额是合约保证金不是现货持仓，
// 模拟盘是 demo 资金，CFD 不是币；同 venue 多 spot 账户按 venue 聚合累加。
func buildHoldingsFromSnapshot(infos []*v1.AccountInfo) map[string]map[string]float64 {
	out := make(map[string]map[string]float64)
	for _, info := range infos {
		if info == nil || info.AssetType != "cex" || info.VenueType != "spot" || info.Simulated {
			continue
		}
		if out[info.Venue] == nil {
			out[info.Venue] = make(map[string]float64)
		}
		for _, h := range info.Holdings {
			out[info.Venue][h.Ccy] += h.Qty
		}
	}
	return out
}
