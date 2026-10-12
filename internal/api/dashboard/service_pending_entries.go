// service_pending_entries.go D-478 S3/S4：待入场区 RPC + 安全档位。
// 透出既有闸结论非新闸（P3：reason 复用各闸原输出；闸序=engine_opp.handleOpportunity）。
// 覆盖=xasset_allow_combos 两型 XASSET_CARRY+FUNDING_HEDGE（SWAPARB 走独立面）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/platform/combo"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
)

// 档位判据常量（D-478 S4）。
const (
	tickFreshWindow     = 5 * time.Minute  // 双腿 tick 新鲜度（同档案 5min 闸）
	monitorFreshWindow  = 30 * time.Minute // XASSET 监控簿新鲜度闸
	snapshotStaleWindow = 5 * time.Minute  // 账户快照 >5min=缺数据不猜
	tierAlertInterval   = 6 * time.Hour    // 恶化告警去重（对齐 D-472 S3）
	defaultCrossCap     = 0.5              // cfg 未注入时的占比档（NOX_CROSS_NOTIONAL_EQUITY_CAP 缺省同值）
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
	in, err := s.loadPendingInputs(ctx)
	if err != nil {
		return nil, err
	}
	var scored []scoredRow
	for _, c := range combos {
		row, depth, ok := s.evalPendingRow(ctx, c, in)
		if !ok {
			continue
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

// evalPendingRow 单组合待入场行求值（pendingEntries 与闸态 diff 器共用，D-007 S1）。
// ok=false=不属待入场集（disabled 或在持仓）。
func (s *Service) evalPendingRow(ctx context.Context, c pgstore.XAssetComboAdminRow, in *pendingInputs) (row *v1.XAssetPendingEntryRow, depth int, ok bool) {
	if !c.Enabled {
		return nil, 0, false
	}
	if comboInPosition(in.active, c) {
		return nil, 0, false // 有 open/legged/closing 在持仓 → 归持仓区
	}
	kind, reason, depth, ready := s.probeGates(ctx, c, in.venueAllow, in.canonAllow, in.flags, in.caps, in.snaps)
	tier, detail, warn := s.comboTier(ctx, c, in.snaps)
	row = &v1.XAssetPendingEntryRow{
		ComboId: c.ID, ComboLabel: fmt.Sprintf("%s(%s)×%s(%s)", c.CanonicalBuy, c.VenueBuy, c.CanonicalSell, c.VenueSell), EntryKind: kind,
		WaitReason: reason, Ready: ready,
		Tier: tier, TierDetail: detail, TierWarn: warn,
	}
	s.fillWaitCondition(ctx, c, row, ready) // D-580：等待条件透出（等什么+当前多少+还差多少）
	// D-010 S5：保证金预算透显（服务端算，前端只渲染；frac/min 输入缺→字段留空不猜）。
	row.MarginBudgetUsd = c.MarginUSD
	if n, fB, fS, kB, kS, ok := s.comboLegNotional(ctx, c); ok {
		row.LegNotionalUsd = &n
		// D-014 ④：回本估同源——econ 单结构（注入未接线/缺账→留空不猜）。
		if s.econEntryFn != nil {
			if be, ok2 := s.econEntryFn(ctx, c.VenueBuy, c.VenueSell, c.CanonicalBuy, c.CanonicalSell, n); ok2 {
				row.EstBreakevenDays = &be
			}
		}
		if kB {
			m := n * fB
			row.BuyLegMarginUsd = &m
		}
		if kS {
			m := n * fS
			row.SellLegMarginUsd = &m
		}
	}
	if mm, ok := s.comboMinMarginUSD(ctx, c); ok {
		row.MinMarginUsd = &mm
	}
	if r, ok2 := fundingRejectFor(in.rejects, c.CanonicalBuy, c.CanonicalSell, c.VenueBuy, c.VenueSell); ok2 {
		row.RecentFundingReject = true
		row.RecentRejectReason = rejectReasonText(r)
	}
	if be := s.breakerFor(c); be != nil { // D-560 S3：熔断态透出
		row.BreakerPaused = true
		row.BreakerClass = be.Class
		row.BreakerResumeAt = timestamppb.New(be.ResumeAt)
		row.BreakerCount = int32(be.Count)
	}
	return row, depth, true
}

// comboInPosition gate 防重同源：positions.opportunity_id × combo 包匹配（两个 canonical 变体都试）。
// active 由调用方批量载入（D-007 S1：原逐组合 N+1 查询收进 loadPendingInputs 单源）。
func comboInPosition(active []pgstore.Position, c pgstore.XAssetComboAdminRow) bool {
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
	// 闸 6+6.5：venue 敞口 cap（前瞻化）+ canonical 聚簇 cap（D-006 S3）——
	// 判据体在 exposureCapsGate（service_pending_entries_helpers.go）。
	if reason, ok := s.exposureCapsGate(ctx, c, caps); !ok {
		return kind, reason, 7, false
	}
	// 闸 7.5 qty 明示（D-009 S4）：notional/2÷perLot < 腿 minStep = 死额度——
	// waitReason 明写最小可开金额（原静默吞/绕 MaxLongQty 地板黑盒）。
	if reason, blocked := s.comboQtyBelowMin(ctx, c); blocked {
		return kind, reason, 8, false
	}
	// D-563 S3 腿池闸（杠杆感知保证金需求；同判据 comboPoolVerdict）：conflict=锁死/unknown=待定。
	if verdict, detail := s.comboPoolVerdict(ctx, c, snaps); verdict != "ok" {
		return kind, detail, 8, false
	}
	if t := map[string]string{"XASSET_CARRY": "条件就绪·等价差达阈", "FUNDING_HEDGE": "条件就绪·等净息差达阈"}[kind]; t != "" { // D-587 坑4：按 kind 说清等什么
		return kind, t, 9, true
	}
	return kind, "条件就绪·等检测器产机会", 9, true // 防御：新 kind 不黑话
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
	snaps, err := s.liveAccountLatest(ctx)
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
