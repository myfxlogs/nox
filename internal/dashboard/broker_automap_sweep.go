package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// autoMapSweepInterval 目录重识别周期（D-148）：商目录漂移/alias 表长进/归一规则修复
// 后，已 attach venue 的映射增量只在本循环与 UI 手动两条径补齐；4h 与 scan 轮同量级。
const autoMapSweepInterval = 4 * time.Hour

// RunAutoMapSweep 周期性目录重识别（D-148）：对全部 connected venue 逐个跑 autoMapVenue
// 内核（与 AutoMapBroker RPC 同语义幂等——已有映射跳过、只应用 auto 状态、
// conflict/multi_hit/trade_disabled 不自动应用）；新映射经既有
// ReloadSymbolMap+Resubscribe+swap 补轮径运行时生效，ticks/facts/universe 下游自动跟上。
// 业主域保留：映射增量记 info 告警留痕（D-147 口径：系统自决事件不推送）。
func (s *Service) RunAutoMapSweep(ctx context.Context) {
	t := time.NewTicker(autoMapSweepInterval)
	defer t.Stop()
	for {
		s.autoMapSweepOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// autoMapSweepOnce 单轮：connected venue 集来自 AllSymbolGroups（symbolGroups 就绪者）；
// 单 venue 失败只记日志不中断本轮（venue 断连/查询败下轮自愈）。
func (s *Service) autoMapSweepOnce(ctx context.Context) {
	groups, err := s.bm.AllSymbolGroups(ctx)
	if err != nil {
		log.Printf("[auto-map sweep] venues: %v", err)
		return
	}
	for venue := range groups {
		_, applied, skipped, err := s.autoMapVenue(ctx, venue)
		if err != nil {
			log.Printf("[auto-map sweep] %s: %v", venue, err)
			continue
		}
		if applied == 0 {
			continue
		}
		msg := fmt.Sprintf("[auto-map] %s 目录重识别 +%d 映射（skipped %d，自动订阅+facts 已生效）", venue, applied, skipped)
		if err := s.store.InsertAlert(ctx, nil, "info", msg); err != nil {
			log.Printf("[auto-map sweep] alert: %v", err)
		}
	}
}

// equivSeedTargets D-149：venue 已映射 canonical 中 ∈ 既有 equiv class 域者，
// 投影为待补 (class, canonical, venueType, venue) 行集（纯函数）。class 只复用
// 既有 canonical→class 域——不替 venue 猜类；域外品种（无 CEX 对手盘语义的
// 指数/外汇）不产行；venue 已有行（含 disabled）不重复产。
func equivSeedTargets(mappedCanonicals []string, equiv []pgstore.AssetEquivEntry, venueType, venue string) []*pgstore.AssetEquivEntry {
	clsOf := map[string]string{}
	have := map[string]bool{}
	for _, e := range equiv {
		if _, ok := clsOf[e.Canonical]; !ok {
			clsOf[e.Canonical] = e.AssetClass
		}
		if e.Venue == venue {
			have[e.AssetClass+"|"+e.Canonical] = true
		}
	}
	var out []*pgstore.AssetEquivEntry
	seen := map[string]bool{}
	for _, canon := range mappedCanonicals {
		cls, ok := clsOf[canon]
		key := cls + "|" + canon
		if !ok || have[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, &pgstore.AssetEquivEntry{AssetClass: cls, Canonical: canon, VenueType: venueType, Venue: venue})
	}
	return out
}

// seedAssetEquivVenue D-149：automap 应用映射后为 venue 补 asset_equiv 行。
// 簿三元组派生（cfdBookSpecs）与开仓读侧（cfdPairCandidates）经 venue|canonical
// 查 equiv——新账户零行时退 legacy spec（ExSym=canonical），原生 ticks 键
// （XM OILCash# 等）簿写不出→新开仓 entry_basis/est_fee NULL 重演（生产实证）。
func (s *Service) seedAssetEquivVenue(ctx context.Context, venue string, mappedCanonicals []string) (int, error) {
	equiv, err := s.store.ListAssetEquiv(ctx)
	if err != nil {
		return 0, err
	}
	a, err := s.store.GetBrokerAccount(ctx, venue)
	if err != nil {
		return 0, fmt.Errorf("broker account: %w", err)
	}
	venueType := "mt5"
	if a.Platform == 0 {
		venueType = "mt4"
	}
	n := 0
	for _, e := range equivSeedTargets(mappedCanonicals, equiv, venueType, venue) {
		ok, err := s.store.InsertAssetEquivIfAbsent(ctx, e)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}
