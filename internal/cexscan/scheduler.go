// D-271 S5 调度器：定时轮询 scanOnce 全流程。
package cexscan

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
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
	okxSpotBase string // D-578：okx spot base_url（cex-proxy 深度通道）
	symbols     []string
	// refreshTopic D-007 S3：scanOnce 落库毕 emit "cex_scan"（前端失效触发；nil=未接线静默）。
	refreshTopic *bus.Topic[string]

	// D-383 B1：order-event 定向落库收合态（per-venue lastWrite，OnOrderEvent 专用）。
	orderSnapMu   sync.Mutex
	orderSnapLast map[string]time.Time
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
			okxSpotBase = e.BaseURL // D-578：cex-proxy 基址（token 由 httpx 注入）
		}
	}
	return &Scanner{
		cfg:           cfg,
		store:         store,
		accounts:      am,
		earn:          earn,
		client:        httpx.Client(),
		bnAPIURL:      bnAPIURL,
		okxSpotBase:   okxSpotBase,
		symbols:       cfg.CexScanSymbols,
		orderSnapLast: make(map[string]time.Time),
	}
}

// Run 启动即扫一次 → ticker(cfg.CexScanInterval) → scanOnce。
// D-562 S3 三禁令③注记：本扫描面（earn APR/spread 快照）=统计参考面——earn 利率无 WS 流
// （venue 无推送），低频拉取合法但不作即时开仓判据（FUNDING_HEDGE 判定链=funding regime
// facts+实时双腿报价，不经本表）。
func (s *Scanner) Run(ctx context.Context) {
	// D-578：三所 spot 深度定时采样（同生命周期 goroutine；env ARB_DEPTH_SAMPLER_OFF=1 停）。
	go newDepthSampler(s.store, s.okxSpotBase).Run(ctx)
	// 启动即扫一次：账户快照全量水合 + ②③④
	if err := s.snapshotAccounts(ctx, true); err != nil {
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
			if err := s.snapshotAccounts(ctx, false); err != nil {
				log.Printf("[cexscan] snapshot: %v", err)
			}
			if err := s.scanOnce(ctx); err != nil {
				log.Printf("[cexscan] scan: %v", err)
			}
		}
	}
}

// SetRefreshTopic D-007 S3：装配注入（cmd/core；nil=不 emit）。
func (s *Scanner) SetRefreshTopic(t *bus.Topic[string]) { s.refreshTopic = t }

// snapshotAccounts 账户快照落库（读 AccountMonitor 内存缓存→InsertVenueAccountSnapshots）。
// D-004 收窄：full=true 仅 boot 水合全量；full=false 周期分支排除有私有 WS 事件写口的键
// （事件写口=私有 WS 直写同表，venue.HasPrivateStream）——保留 cfd/MT（行待 #1b 事件化）、
// okx funding/earn、binance funding、gate spot 等无流键（子账户无 WS 余额频道=无流源
// D-006，统计/展示用途）。
// D-360 S2：写时过滤只收 real（证真才留）——cex=!Simulated / cfd=venue∈is_real 键集
// （核证点①②见 account.go IsRealAccount）；键集查询失败 → 本轮整批不落（fail-closed）。
func (s *Scanner) snapshotAccounts(ctx context.Context, full bool) error {
	snaps := ScanAccounts(s.accounts)
	if len(snaps) == 0 {
		return nil
	}
	realKeys, err := s.store.BrokerRealAccountKeys(ctx)
	if err != nil {
		return fmt.Errorf("broker real keys: %w（本轮快照不落库，证真才留 fail-closed）", err)
	}
	rows := snapshotRowsFor(snaps, realKeys, full)
	if len(rows) == 0 {
		return nil
	}
	if err := s.store.InsertVenueAccountSnapshots(ctx, rows); err != nil {
		return fmt.Errorf("insert venue account snapshots: %w", err)
	}
	return nil
}

// snapshotRowsFor full=false 时排除有私有 WS 事件写口的键（venue.HasPrivateStream，
// 判据=原始 venue_type——okx spot/perp 归一 unified 之前判定），再复用
// buildVenueSnapshotRows 同口径构造（real 过滤+归一+去重）。full=true 全量（boot 水合）。
func snapshotRowsFor(snaps []AccountSnapshot, realKeys map[string]bool, full bool) []pgstore.VenueAccountSnapshot {
	if !full {
		kept := snaps[:0]
		for _, sn := range snaps {
			if venue.HasPrivateStream(sn.Venue, sn.VenueType, sn.Simulated) {
				continue
			}
			kept = append(kept, sn)
		}
		snaps = kept
	}
	return buildVenueSnapshotRows(snaps, realKeys)
}

// scanOnce 串行 ②earn.Collect→InsertCexEarnAPR ③CalcSpreads→InsertCexSpread ④earn APR 判断。
// （① 账户快照仅 boot 水合+无流键周期补写，见 snapshotAccounts——D-004：有流键周期快照退役。）
// D-271 R3：funding 扫描已移除——现有 engine/evaluator 管线已覆盖 funding 机会分析。
// D-271 R5：理财 APR + 价差告警已移除——理财告警是毛数据噪音（280 条实证），
// 价差告警现有 CrossVenueDetector 已事件驱动覆盖。保留采集入库供前端展示。
// D-273 ④：earn APR 判断结果 upsert 入库（fin_cex_earn_judgment），供前端展示，不发告警。
func (s *Scanner) scanOnce(ctx context.Context) error {

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

// isUnifiedAccountVenue D-361 S3：统一账户 venue 白名单（仅 okx——统一账户 spot/perp 双
// config 查同一 /account/balance；binance/gate 现货合约分钱包实证不归一，SSOT §0）。
func isUnifiedAccountVenue(venue string) bool { return venue == "okx" }

// normalizeVenueType D-361 S3/D-363-R1：统一账户写侧归一——仅 okx **交易账户**
// （spot/perp，双 config 同钱）归一 "unified"；D-363 子账户（funding/earn）是不同钱
// （各自独立端点独立余额），原样穿透——归一无条件压全部 type 会把 earn $5,261 塌进
// unified（生产 23:06 实证顶掉交易账户行）。读侧零改。
func normalizeVenueType(assetType, venue, venueType string) string {
	if assetType == "cex" && isUnifiedAccountVenue(venue) && (venueType == "spot" || venueType == "perp") {
		return "unified"
	}
	return venueType
}

// buildVenueSnapshotRows 快照行构造（纯函数，D-361 S3 自 scanOnce 抽出可测）：
// real 过滤（IsRealAccount 证真才留）→ venue_type 归一 → 同 (venue,venue_type) 去重仅留首行
// （okx spot/perp 双 config 各返回一次统一账户=同钱，去重后落一行 unified）。
func buildVenueSnapshotRows(snaps []AccountSnapshot, realKeys map[string]bool) []pgstore.VenueAccountSnapshot {
	rows := make([]pgstore.VenueAccountSnapshot, 0, len(snaps))
	seen := map[string]bool{}
	for _, sn := range snaps {
		if !IsRealAccount(sn.AssetType, sn.Simulated, sn.Venue, realKeys) {
			continue
		}
		vt := normalizeVenueType(sn.AssetType, sn.Venue, sn.VenueType)
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
