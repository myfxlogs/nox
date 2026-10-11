// D-271 S5 调度器：定时轮询 scanOnce 全流程。
package cexscan

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"arb/internal/bus"

	"arb/internal/config"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/httpx"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// Scanner CEX 只读扫描器调度器。
type Scanner struct {
	cfg         *config.Config
	store       *pgstore.Store
	accounts    AccountSnapshotter
	earn        *EarnCollector
	client      *http.Client
	bnAPIURL    string
	okxSpotBase string // okx spot base_url（cex-proxy；spread 轨 D-011 回落通道）
	symbols     []string
	// refreshTopic D-007 S3：scanOnce 落库毕 emit "cex_scan"（前端失效触发；nil=未接线静默）。
	refreshTopic *bus.Topic[string]
}

// NewScanner 创建扫描器。
func NewScanner(cfg *config.Config, store *pgstore.Store, am AccountSnapshotter, exchs []pgstore.ExchangeConfig) *Scanner {
	earn := NewEarnCollector(cfg, exchs)
	bnAPIURL := ""
	okxSpotBase := ""
	for _, e := range exchs {
		if e.Venue == "binance" && e.VenueType == "spot" && !e.Simulated && bnAPIURL == "" {
			bnAPIURL = e.BaseURL
		}
		if e.Venue == "okx" && e.VenueType == "spot" && !e.Simulated && okxSpotBase == "" {
			okxSpotBase = e.BaseURL // cex-proxy 基址（token 由 httpx 注入）
		}
	}
	return &Scanner{
		cfg:         cfg,
		store:       store,
		accounts:    am,
		earn:        earn,
		client:      httpx.Client(),
		bnAPIURL:    bnAPIURL,
		okxSpotBase: okxSpotBase,
		symbols:     cfg.CexScanSymbols,
	}
}

// Run 启动即扫一次 → ticker(cfg.CexScanInterval) → scanOnce。
// D-562 S3 三禁令③注记：本扫描面（earn APR/spread 快照）=统计参考面——earn 利率无 WS 流
// （venue 无推送），低频拉取合法但不作即时开仓判据（FUNDING_HEDGE 判定链=funding regime
// facts+实时双腿报价，不经本表）。
func (s *Scanner) Run(ctx context.Context) {
	// 启动即水合一次账户快照（#1c：boot 历史水合唯一写口；实时值=AccountMonitor，
	// MT=余额帧事件写 / CEX=私有流事件写 / 无流子账户=资金页打开按需回读 Kick）。
	if err := s.hydrateAccountSnapshots(ctx); err != nil {
		log.Printf("[cexscan] initial snapshot: %v", err)
	}
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

// SetRefreshTopic D-007 S3：装配注入（cmd/core；nil=不 emit）。
func (s *Scanner) SetRefreshTopic(t *bus.Topic[string]) { s.refreshTopic = t }

// hydrateAccountSnapshots boot 一次性账户快照水合（3d-P1#1c：周期补写整体退役——
// fin_venue_account_snapshot 降为历史记录/审计留痕，实时展示与门禁唯一源=AccountMonitor：
// CEX 私有流事件写 + MT 余额帧写（src=mt_frame）+ 无流子账户按需回读 Kick）。
// D-360 S2：写时过滤只收 real（证真才留）——cex=!Simulated / cfd=venue∈is_real 键集
// （venue.IsRealAccount）；键集查询失败 → 本轮整批不落（fail-closed）。
func (s *Scanner) hydrateAccountSnapshots(ctx context.Context) error {
	snaps := ScanAccounts(s.accounts)
	if len(snaps) == 0 {
		return nil
	}
	realKeys, err := s.store.BrokerRealAccountKeys(ctx)
	if err != nil {
		return fmt.Errorf("broker real keys: %w（本轮快照不落库，证真才留 fail-closed）", err)
	}
	rows := buildVenueSnapshotRows(snaps, realKeys)
	if len(rows) == 0 {
		return nil
	}
	if err := s.store.InsertVenueAccountSnapshots(ctx, rows); err != nil {
		return fmt.Errorf("insert venue account snapshots: %w", err)
	}
	return nil
}

// scanOnce 串行 ②earn.Collect→InsertCexEarnAPR ③CalcSpreads→InsertCexSpread ④earn APR 判断。
// （① 账户快照=boot 一次性水合 hydrateAccountSnapshots——3d-P1#1c：周期补写退役。）
// D-271 R3：funding 扫描已移除——现有 engine/evaluator 管线已覆盖 funding 机会分析。
// D-271 R5：理财 APR + 价差告警已移除——理财告警是毛数据噪音（280 条实证），
// 价差告警现有 CrossVenueDetector 已事件驱动覆盖。保留采集入库供前端展示。
// D-273 ④：earn APR 判断结果 upsert 入库（fin_cex_earn_judgment），供前端展示，不发告警。
func (s *Scanner) scanOnce(ctx context.Context) error {

	// ② 理财 APR 采集 → 入库
	earnAPRs, earnErrs := s.earn.Collect(ctx)
	for i := range earnAPRs {
		if err := s.store.InsertCexEarnAPR(ctx, &earnAPRs[i]); err != nil {
			log.Printf("[cexscan] insert earn apr %s/%s: %v", earnAPRs[i].Venue, earnAPRs[i].Ccy, err)
		}
	}

	// D-183 ②b 三所 DCI/双币报价采集 → 入库（单所失败在 CollectDCI 内隔离，
	// 本段只做入库——写败单行 log 不拖垮后续扫描段）。
	dciRows, dciErrs := s.earn.CollectDCI(ctx)
	for i, q := range dciRows {
		qq := q
		if err := s.store.InsertCexDCIQuote(ctx, &qq); err != nil {
			log.Printf("[cexscan] insert dci quote %s/%s[%d]: %v", q.Venue, q.Underlying, i, err)
		}
	}

	// D-188② 权限面落库（吃同轮采集错误面零重调，dual_order/earn_subscribe）。
	s.RecordPermissions(ctx, earnErrs, dciErrs)

	// ③ 跨所价差 → 入库（okxSpotBase=D-011 S2 OKX 腿直连传输层错误的 cex-proxy 回落通道）
	spreads, err := CalcSpreads(ctx, s.client, s.bnAPIURL, s.okxSpotBase, s.symbols)
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

	// ④ earn APR 判断（D-273）：只依赖本币余额明细，失败只记日志不阻断采集。
	holdings := buildHoldingsFromSnapshot(s.accounts.Snapshot())
	judge := NewEarnJudge(holdings)
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
	// D-007 S3：一轮采集写毕→emit（写源 emit；部分失败仍发=快照是尽力面，前端重取读到最新已落）。
	if s.refreshTopic != nil {
		s.refreshTopic.Publish("cex_scan")
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

// isUnifiedAccountVenue/normalizeVenueType 已随 3d-P1#1c 下沉 internal/venue
// （venue.NormalizeVenueType；写侧归一单源，dashboard 实时态读侧共用）。

// buildVenueSnapshotRows 快照行构造（纯函数，D-361 S3 自 scanOnce 抽出可测）：
// real 过滤（venue.IsRealAccount 证真才留）→ venue_type 归一 → 同 (venue,venue_type) 去重仅留首行
// （okx spot/perp 双 config 各返回一次统一账户=同钱，去重后落一行 unified）。
func buildVenueSnapshotRows(snaps []AccountSnapshot, realKeys map[string]bool) []pgstore.VenueAccountSnapshot {
	rows := make([]pgstore.VenueAccountSnapshot, 0, len(snaps))
	seen := map[string]bool{}
	for _, sn := range snaps {
		if !venue.IsRealAccount(sn.AssetType, sn.Simulated, sn.Venue, realKeys) {
			continue
		}
		vt := venue.NormalizeVenueType(sn.AssetType, sn.Venue, sn.VenueType)
		key := keydomain.Join(sn.Venue, vt)
		if seen[key] {
			continue
		}
		seen[key] = true
		row := pgstore.VenueAccountSnapshot{
			Venue: sn.Venue, VenueType: vt,
			BalanceRaw: sn.Balance, EquityRaw: sn.Equity, FreeMarginRaw: sn.FreeMargin,
			Currency: sn.Currency, FetchedAt: sn.Ts,
		}
		// D-405 S2：CEX buffer 写时归一透传（<=0=未采 → NULL 诚实缺省）。
		if sn.BufferMultiple > 0 {
			b := sn.BufferMultiple
			row.BufferMultiple = &b
		}
		rows = append(rows, row)
	}
	return rows
}
