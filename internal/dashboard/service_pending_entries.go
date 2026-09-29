// service_pending_entries.go D-478 S3/S4：待入场区 RPC + 安全档位。
// 透出既有闸结论非新闸（P3：reason 复用各闸原输出；闸序=engine_opp.handleOpportunity）。
// 覆盖=xasset_allow_combos 两型 XASSET_CARRY+FUNDING_HEDGE（SWAPARB 走独立面）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 档位判据常量（D-478 S4）。
const (
	tickFreshWindow     = 5 * time.Minute  // 双腿 tick 新鲜度（同档案 5min 闸）
	monitorFreshWindow  = 30 * time.Minute // XASSET 监控簿新鲜度闸
	snapshotStaleWindow = 5 * time.Minute  // 账户快照 >5min=缺数据不猜
	tierAlertInterval   = 6 * time.Hour    // 恶化告警去重（对齐 D-472 S3）
	defaultCrossCap     = 0.5              // cfg 未注入时的占比档（ARB_CROSS_NOTIONAL_EQUITY_CAP 缺省同值）
)

// ListXAssetPendingEntries D-478 S3：enabled 组合−在持仓=挂单集，按序探闸。
func (s *Service) ListXAssetPendingEntries(ctx context.Context, _ *connect.Request[v1.ListXAssetPendingEntriesRequest]) (*connect.Response[v1.ListXAssetPendingEntriesResponse], error) {
	rows, err := s.pendingEntries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ListXAssetPendingEntriesResponse{Rows: rows}), nil
}

// connectErr 内部错误包装（connect CodeInternal）。
func connectErr(err error) error {
	return connect.NewError(connect.CodeInternal, err)
}

// SetCrossCap D-478 S4：注入 cross 占比档（cfg.CrossNotionalEquityCap；未注入=缺省 0.5）。
func (s *Service) SetCrossCap(cap float64) { s.crossCap = cap }

// pendingEntries 七闸序推导主逻辑（供 RPC 与测试共用）。
func (s *Service) pendingEntries(ctx context.Context) ([]*v1.XAssetPendingEntryRow, error) {
	combos, err := s.store.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	rules, err := s.store.ListAutotradeAllowRulesAll(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	venueAllow, canonAllow := map[string]bool{}, map[string]bool{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if r.Kind == "venue" {
			venueAllow[r.Value] = true
		}
		if r.Kind == "canonical" {
			canonAllow[r.Value] = true
		}
	}
	flags, err := s.store.ListActiveFlags(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	caps, err := s.store.ListVenueExposureCaps(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	snaps, err := s.store.LatestVenueAccountSnapshots(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	rejects := s.fundingRejectFacts(ctx) // D-560 S2b：近6h资金拒批量透出（禁N+1）
	var scored []scoredRow
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		label := fmt.Sprintf("%s(%s)×%s(%s)", c.CanonicalBuy, c.VenueBuy, c.CanonicalSell, c.VenueSell)
		if s.comboInPosition(ctx, c) {
			continue // 有 open/legged/closing 在持仓 → 归持仓区
		}
		kind, reason, depth, ready := s.probeGates(ctx, c, venueAllow, canonAllow, flags, caps, snaps)
		tier, detail, warn := s.comboTier(ctx, c, snaps)
		row := &v1.XAssetPendingEntryRow{
			ComboId: c.ID, ComboLabel: label, EntryKind: kind,
			WaitReason: reason, Ready: ready,
			Tier: tier, TierDetail: detail, TierWarn: warn,
		}
		s.fillWaitCondition(ctx, c, row, ready) // D-580：等待条件透出（等什么+当前多少+还差多少）
		if r, ok := fundingRejectFor(rejects, c.CanonicalBuy, c.CanonicalSell, c.VenueBuy, c.VenueSell); ok {
			row.RecentFundingReject = true
			row.RecentRejectReason = rejectReasonText(r)
		}
		if be := s.breakerFor(c); be != nil { // D-560 S3：熔断态透出
			row.BreakerPaused = true
			row.BreakerClass = be.Class
			row.BreakerResumeAt = timestamppb.New(be.ResumeAt)
			row.BreakerCount = int32(be.Count)
		}
		scored = append(scored, scoredRow{depth: depth, row: row})
	}
	// D-483 排序（服务端）：最可能入场的排最上——闸深降序（闯到越深的闸=越接近
	// 入场，就绪天然置顶）；同深按 comboId 稳定序。
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].depth != scored[j].depth {
			return scored[i].depth > scored[j].depth
		}
		return scored[i].row.ComboId < scored[j].row.ComboId
	})
	out := make([]*v1.XAssetPendingEntryRow, 0, len(scored))
	for _, sr := range scored {
		out = append(out, sr.row)
	}
	return out, nil
}

// scoredRow D-483：排序中间态（depth 不透 proto——纯排序键，前端零信任不二次排序）。
type scoredRow struct {
	depth int
	row   *v1.XAssetPendingEntryRow
}

// comboInPosition gate 防重同源：positions.opportunity_id × combo 包匹配（两个 canonical 变体都试）。
func (s *Service) comboInPosition(ctx context.Context, c pgstore.XAssetComboAdminRow) bool {
	active, err := s.store.ListActivePositions(ctx)
	if err != nil {
		return false // fail-closed：查不到=按在持处理（保守归持仓区）
	}
	for _, canon := range []string{c.CanonicalBuy, c.CanonicalSell} {
		key := combo.Key("XASSET_CARRY", canon, c.VenueBuy, c.VenueSell)
		for _, p := range active {
			if p.OpportunityID != "" && combo.MatchOppID(p.OpportunityID, key) {
				return true
			}
		}
	}
	return false
}

// probeGates 闸序（同 handleOpportunity 序）→ kind/waitReason/depth/ready。
// depth=D-483 排序键：0 未覆盖/1 配置禁入/2 旗标/4 regime/5 tick/6 监控簿/7 cap/8 腿池/9 就绪。
func (s *Service) probeGates(ctx context.Context, c pgstore.XAssetComboAdminRow, venueAllow, canonAllow map[string]bool, flags map[string]pgstore.VenueSymbolFlag, caps map[string]pgstore.VenueExposureCap, snaps []pgstore.VenueAccountLatest) (kind, reason string, depth int, ready bool) {
	buyMT := keydomain.IsMTKey(c.VenueBuy)
	sellMT := keydomain.IsMTKey(c.VenueSell)
	// D-478 返修 R6：kind 推断=「恰好一条 MT 腿→XASSET_CARRY」（buy/sell 双向）；
	// perp×perp/spot×spot/双 MT/空 → kind 留空=「构型未覆盖」如实。
	switch {
	case buyMT != sellMT:
		kind = "XASSET_CARRY"
	case keydomain.IsType(c.VenueBuy, "spot") && keydomain.IsType(c.VenueSell, "perp"):
		kind = "FUNDING_HEDGE" // 正向（spot 买×perp 卖）
	case keydomain.IsType(c.VenueBuy, "perp") && keydomain.IsType(c.VenueSell, "spot"):
		kind = "FUNDING_HEDGE" // 反构型（perp 买腿）→ 下方构型禁入分支接住（SSOT 标签不失义）
	}
	// R5：构型未覆盖（双 CEX/未知形态）→「构型未覆盖」短路，不走七闸（同所组合不产两型 opp）。
	if kind == "" {
		return kind, "组合形态暂不支持（两腿结构未接入任何策略）", 0, false
	}
	// 闸 1：venue/canonical allow_rules（engine allowRuleReject 同判据，kind='venue'/'canonical' 值查表）。
	// R2：双腿分别点名实际未放行腿（原实现恒报 buy 腿=误导）。
	if !venueAllow[c.VenueBuy] {
		return kind, fmt.Sprintf("通道未放行: %s", c.VenueBuy), 1, false
	}
	if !venueAllow[c.VenueSell] {
		return kind, fmt.Sprintf("通道未放行: %s", c.VenueSell), 1, false
	}
	if len(canonAllow) > 0 && !canonAllow[c.CanonicalBuy] {
		return kind, fmt.Sprintf("标的未放行: %s", c.CanonicalBuy), 1, false
	}
	// 闸 2：旗标投影（engine flagRuleReject 同源 flags；blocked/warn 均透出为拦路原因）。
	for _, v := range []string{bareVenueKey(c.VenueBuy), bareVenueKey(c.VenueSell)} {
		for _, sym := range []string{c.CanonicalBuy, c.CanonicalSell, "*"} {
			if f, ok := flags[pgstore.FlagKey(v, sym)]; ok {
				return kind, fmt.Sprintf("风控旗标 %s: %s:%s %s", f.Level, v, sym, f.Reason), 2, false
			}
		}
	}
	// 构型：FUNDING_HEDGE 反构型（perp 买腿）→ 构型禁入（D-473 v1 正向钉死）。
	if kind == "FUNDING_HEDGE" && keydomain.IsType(c.VenueBuy, "perp") {
		return kind, "组合方向不支持：仅允许 现货买×合约空 结构", 1, false
	}
	// 闸 3：regime 闸（两型分流，reason=各闸原输出）。
	if kind == "XASSET_CARRY" {
		if ok, r := exec.CarryFundingRegimeCheck(ctx, s.store, c.VenueBuy, c.VenueSell, c.CanonicalBuy, c.CanonicalSell, time.Now(), exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12}); !ok {
			return kind, "息差环境未持续: " + r, 4, false
		}
	}
	if kind == "FUNDING_HEDGE" {
		perpVenue := keydomain.BareOf(c.VenueSell)
		rows, err := s.store.FundingPredWindow(ctx, perpVenue, c.CanonicalBuy, time.Now().Add(-24*time.Hour))
		if err != nil {
			return kind, fmt.Sprintf("息差数据查询失败（保守不放行）: %v", err), 4, false
		}
		if gate, _, r := exec.FundingRegimeOK(rows, +1, 2.0, 12, false); !gate {
			return kind, "息差环境未持续: " + r, 4, false
		}
	}
	// 闸 4：双腿 tick 新鲜度（5min 窗，同档案判据；LatestTick(venue,venueType,symbol) 现成接口）。
	for _, leg := range []struct{ venue, canon string }{{c.VenueBuy, c.CanonicalBuy}, {c.VenueSell, c.CanonicalSell}} {
		vt, sym := legMarket(leg.venue, leg.canon)
		if vt == "" {
			continue // 未知市场形态不设 tick 闸（由后续闸兜底）
		}
		tr, ok, err := s.store.LatestTick(ctx, bareVenueKey(leg.venue), vt, sym)
		if err != nil || !ok || time.Since(tr.Ts) > tickFreshWindow {
			return kind, fmt.Sprintf("行情报价过期: %s/%s（超过5分钟无更新）", bareVenueKey(leg.venue), sym), 5, false
		}
	}
	// 闸 5：XASSET 型监控簿 30min 闸（XAssetMonitorRowNear 现成接口；FUNDING_HEDGE 无簿概念跳过）。
	if kind == "XASSET_CARRY" {
		pair := monitorPairKey(c)
		cexV, mtV := monitorLegKeys(c)
		if _, ok, err := s.store.XAssetMonitorRowNear(ctx, pair, cexV, mtV, time.Now(), monitorFreshWindow); err != nil || !ok {
			return kind, "监控数据过期（超过30分钟无更新）", 6, false
		}
	}
	// 闸 6：venue 敞口 cap（475a 三态同判据：cap=0 冻结 / Σ超限）。
	for _, v := range []string{bareVenueKey(c.VenueBuy), bareVenueKey(c.VenueSell)} {
		if capRow, ok := caps[v]; ok && capRow.Enabled {
			if capRow.CapUSD == 0 {
				return kind, fmt.Sprintf("敞口上限冻结：账户 %s 上限设为0，暂停新入场", v), 7, false
			}
			sum, err := s.venueExposureSum(v)
			if err != nil {
				return kind, fmt.Sprintf("敞口查询失败（保守不放行）: 账户 %s %v", v, err), 7, false
			}
			if sum > capRow.CapUSD {
				return kind, fmt.Sprintf("敞口超限：账户 %s 现有敞口 $%.0f 超过上限 $%.0f", v, sum, capRow.CapUSD), 7, false
			}
		}
	}
	// D-563 S3 腿池闸（杠杆感知保证金需求；同判据 comboPoolVerdict）：conflict=锁死/unknown=待定。
	if verdict, detail := s.comboPoolVerdict(ctx, c, snaps); verdict != "ok" {
		return kind, detail, 8, false
	}
	return kind, "条件就绪·等机会", 9, true // 闸过·等 detector 产 opp（价差未到阈）

}

// TierKickAll D-478 S4：恶化告警事件入口——CEX 账户 WS 余额事件（快照写成功）与组合行写
// 操作回调触发；重算 enabled 组合档位，安全→超档转移时发 warn（6h 去重），
// 安全→安全不发声（transition 语义，禁定时器）。
func (s *Service) TierKickAll(ctx context.Context) {
	if s == nil || s.store == nil {
		return // 部分构造 Service（D-471 测试字面量）nil 安全
	}
	combos, err := s.store.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		return
	}
	snaps, err := s.store.LatestVenueAccountSnapshots(ctx)
	if err != nil {
		return
	}
	s.tierMu.Lock()
	defer s.tierMu.Unlock()
	if s.tierState == nil {
		s.tierState = map[string]tierState{} // R1：init 移入锁内（原锁外惰性 init=数据竞争）
	}
	now := time.Now()
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		tier, _, warn := s.comboTier(ctx, c, snaps)
		key := fmt.Sprintf("%d", c.ID)
		prev, seen := s.tierState[key]
		s.tierState[key] = tierState{tier: tier, at: now}
		// transition 语义：仅 安全(ok/unset)→超档(over) 转移发声；首见不发声（无前态）。
		if seen && prev.tier != "over" && tier == "over" && warn && now.Sub(prev.alertAt) >= tierAlertInterval {
			if err := s.store.InsertAlert(ctx, nil, "warn", fmt.Sprintf("[档位恶化] 组合#%d 超档：存续态档位由安全转超档，详见待入场区", c.ID)); err != nil {
				continue
			}
		}
		if warn {
			if st, ok2 := s.tierState[key]; ok2 {
				st.alertAt = now
				s.tierState[key] = st
			}
		}
	}
}

type tierState struct {
	tier    string
	at      time.Time
	alertAt time.Time
}
