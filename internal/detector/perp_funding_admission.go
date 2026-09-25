// perp_funding_admission.go —— D-491 S3：PERP_FUNDING 证据准入评估器（硬编码白名单退役）。
// 证据源=PG（facts 7d 滚窗 DB 侧聚合 + ticks 腿可执行性 + fin_cex_depth 深度 +
// exchange_configs.simulated real 判定）；产出=准入表行 + 检测器原子快照
// （admitted/owner_pinned 才放行）+ depth_want 补采集（pending_depth 与 admitted
// 持续采深——admitted 离集则深度证据老化失新，闭环保活）。
// 分层：本包不依赖 pgstore——AdmissionStore 窄接口由装配层适配（assetEquivSource 同族）。
package detector

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"arb/internal/bus"
)

// D-491 准入状态（perp_funding_admission.status）与业主钉死位（owner_override）。
const (
	AdmissionAdmitted     = "admitted"
	AdmissionPendingDepth = "pending_depth"
	AdmissionReview       = "review"
	AdmissionDenied       = "denied"

	OverridePinned = "pinned"
	OverrideDenied = "denied"
)

// PerpFundingAdmissionConfig 八判据阈值（env 注入；Window 默认 7d）。
type PerpFundingAdmissionConfig struct {
	MinNetAnnualized float64       // 均值差下限（年化 pp；=ARB_PERP_FUNDING_MIN_NET_ANNUALIZED）
	DirPct           float64       // 方向一致性下限（0..1）
	MaxSpreadSD      float64       // pair 差 σ 上限（pp）
	MinSamples       int           // 每腿窗内样本下限
	PinPct           float64       // 钉顶防护：任一腿 |均值| ≥ 即 review
	MinDepthUSD      float64       // 两腿顶档深度较小者下限
	Window           time.Duration // 滚窗（默认 7d）
}

// FundingLegStat/FundingPairStat/AdmissionRow 评估器本地视图（pgstore 同名行
// 结构镜像——装配层逐字段换算，本包不 import pgstore，assetEquivSource 先例）。
type FundingLegStat struct {
	Symbol, Venue string
	Mean, SD      float64
	N             int
}
type FundingPairStat struct {
	Symbol, VenueA, VenueB string
	MeanDiff, SDDiff       float64
	N                      int
	DirPct                 float64 // a.v > b.v 桶占比
}
type AdmissionRow struct {
	Canonical, Status, BestPair, OwnerOverride, Note string
	MeanSpread, SpreadSD, DirConsistency             float64
	DepthMinUSD                                      *float64
	Samples                                          int
	EvaluatedAt                                      *time.Time
}

// AdmissionStore 评估器证据/回写窄接口（cmd/core 适配器实现）。
type AdmissionStore interface {
	ListVenueIsReal(ctx context.Context) (map[string]*bool, error)
	FundingWindowLegStats(ctx context.Context, since time.Time) ([]FundingLegStat, error)
	FundingWindowPairStats(ctx context.Context, since time.Time) ([]FundingPairStat, error)
	PerpTickPresence(ctx context.Context, since time.Time) (map[string]map[string]bool, error)
	LatestCexDepthUSD(ctx context.Context) (map[string]map[string]float64, error)
	UpsertPerpFundingAdmission(ctx context.Context, rows []AdmissionRow) error
	ListPerpFundingAdmission(ctx context.Context) ([]AdmissionRow, error)
}

// AdmissionSink 检测器快照注入点（*PerpFundingDetector 实现）。
type AdmissionSink interface {
	SetAdmissionSnapshot(map[string]bool)
}

// PerpFundingAdmissionEvaluator 证据准入评估器。
// Evaluate 串行（evalMu）；快照/depth_want atomic 发布——崩溃/未首评时
// 检测器 nil→deny fail-closed（今日白名单外全拒等价）。
type PerpFundingAdmissionEvaluator struct {
	cfg  PerpFundingAdmissionConfig
	st   AdmissionStore
	sink AdmissionSink

	evalMu   sync.Mutex
	wantSnap atomic.Value // map[string]bool：depth_want canonical 集
}

func NewPerpFundingAdmissionEvaluator(cfg PerpFundingAdmissionConfig, st AdmissionStore, sink AdmissionSink) *PerpFundingAdmissionEvaluator {
	if cfg.Window <= 0 {
		cfg.Window = 7 * 24 * time.Hour
	}
	e := &PerpFundingAdmissionEvaluator{cfg: cfg, st: st, sink: sink}
	e.wantSnap.Store(map[string]bool{})
	return e
}

// DepthWantedBases depth_want 基名集（canonical 去 USDT 尾；非 USDT 尾符号不产
// ——补采链路只认 USDT 计价合约）。cexDepthCollector 每轮合并静态白名单。
func (e *PerpFundingAdmissionEvaluator) DepthWantedBases() []string {
	want, _ := e.wantSnap.Load().(map[string]bool)
	out := make([]string, 0, len(want))
	for c := range want {
		if b := strings.TrimSuffix(c, "USDT"); b != "" && b != c {
			out = append(out, b)
		}
	}
	return out
}

// Reevaluate RPC 手动复估入口（与事件重估同路径，evalMu 串行）。
func (e *PerpFundingAdmissionEvaluator) Reevaluate(ctx context.Context) error { return e.Evaluate(ctx) }

// RefreshSnapshot 只重发快照（owner_override 写后即时生效，不等下轮评估）。
// evalMu 串行化：否则在途 Evaluate 的先写后读旧表可能反盖 override 后发布的快照
// （短暂回跳，最坏持续至下个 funding 事件）——持锁保证写后刷新顺序=最后发布者。
func (e *PerpFundingAdmissionEvaluator) RefreshSnapshot(ctx context.Context) error {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()
	rows, err := e.st.ListPerpFundingAdmission(ctx)
	if err != nil {
		return err
	}
	e.publish(rows)
	return nil
}

// Evaluate 一轮证据评估：DB 聚合 → 八判据 → 状态机 → upsert → 快照/depth_want 发布。
// 出错保持上次快照（陈旧准入胜过全断；nil=从未成功→检测器仍全拒）。
func (e *PerpFundingAdmissionEvaluator) Evaluate(ctx context.Context) error {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()

	since := time.Now().Add(-e.cfg.Window)
	realMap, err := e.st.ListVenueIsReal(ctx)
	if err != nil {
		return fmt.Errorf("venue real map: %w", err)
	}
	realVenues := make(map[string]bool)
	for key, v := range realMap {
		if v != nil && *v && strings.HasSuffix(key, ":perp") {
			realVenues[strings.TrimSuffix(key, ":perp")] = true
		}
	}
	legStats, err := e.st.FundingWindowLegStats(ctx, since)
	if err != nil {
		return err
	}
	pairStats, err := e.st.FundingWindowPairStats(ctx, since)
	if err != nil {
		return err
	}
	ticks, err := e.st.PerpTickPresence(ctx, since)
	if err != nil {
		return err
	}
	rawDepth, err := e.st.LatestCexDepthUSD(ctx)
	if err != nil {
		return err
	}
	// 深度行 symbol=venue 原生形 → CexSymbolKey 归一 canonical（D-481 单源归一器）。
	depth := make(map[string]map[string]float64) // canonical → venue → usd
	for venue, syms := range rawDepth {
		for sym, usd := range syms {
			c := bus.CexSymbolKey(sym)
			if depth[c] == nil {
				depth[c] = make(map[string]float64)
			}
			depth[c][venue] = usd
		}
	}
	legs := make(map[string]map[string]FundingLegStat) // symbol → venue → stat
	for _, l := range legStats {
		if legs[l.Symbol] == nil {
			legs[l.Symbol] = make(map[string]FundingLegStat)
		}
		legs[l.Symbol][l.Venue] = l
	}
	pairs := make(map[string][]FundingPairStat)
	for _, p := range pairStats {
		pairs[p.Symbol] = append(pairs[p.Symbol], p)
	}

	var rows []AdmissionRow
	now := time.Now()
	for canonical, ps := range pairs {
		row := e.evalCanonical(canonical, ps, legs[canonical], realVenues, ticks[canonical], depth[canonical])
		row.EvaluatedAt = &now
		rows = append(rows, row)
	}
	if err := e.st.UpsertPerpFundingAdmission(ctx, rows); err != nil {
		return err
	}
	stored, err := e.st.ListPerpFundingAdmission(ctx)
	if err != nil {
		return err
	}
	e.publish(stored)
	log.Printf("[pf-admission] evaluated %d canonicals (window=%s)", len(rows), e.cfg.Window)
	return nil
}

// publish 快照+depth_want 原子发布。有效准入=owner_override 覆盖优先：
// pinned→放行 / denied→终拒 / ”→status==admitted 才放行。
func (e *PerpFundingAdmissionEvaluator) publish(rows []AdmissionRow) {
	snap := make(map[string]bool, len(rows))
	want := make(map[string]bool)
	for _, r := range rows {
		switch r.OwnerOverride {
		case OverridePinned:
			snap[r.Canonical] = true
		case OverrideDenied:
			// 终拒——不进快照
		default:
			snap[r.Canonical] = r.Status == AdmissionAdmitted
		}
		// depth_want=pending_depth/admitted（admitted 持续采深保活）；业主终拒行不进
		// ——override=denied 的 canonical 不再为它耗补采配额。
		if r.OwnerOverride != OverrideDenied && (r.Status == AdmissionPendingDepth || r.Status == AdmissionAdmitted) {
			want[r.Canonical] = true
		}
	}
	if e.sink != nil {
		e.sink.SetAdmissionSnapshot(snap)
	}
	e.wantSnap.Store(want)
}
