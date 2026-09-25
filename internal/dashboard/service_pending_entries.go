// service_pending_entries.go D-478 S3/S4：待入场区 RPC + 安全档位。
// 语义钉死：本票是「透出既有闸的当前结论」，不是新闸——reason 串直接复用各闸原输出
// （P3：透出层不是第二判据源）；闸序=engine_opp.handleOpportunity 同序防口径分裂。
// 覆盖口径：只覆盖 xasset_allow_combos 消费的两型=XASSET_CARRY+FUNDING_HEDGE
// （SWAPARB 走 env SwapArbAllowCombos 独立面，engine_opp.go 分派实证，不在本区）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
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
	out := make([]*v1.XAssetPendingEntryRow, 0, len(combos))
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		label := fmt.Sprintf("%s(%s)×%s(%s)", c.CanonicalBuy, c.VenueBuy, c.CanonicalSell, c.VenueSell)
		if s.comboInPosition(ctx, c) {
			continue // 有 open/legged/closing 在持仓 → 归持仓区
		}
		kind, reason, ready := s.probeGates(ctx, c, venueAllow, canonAllow, flags, caps)
		tier, detail, warn := s.comboTier(ctx, c, snaps)
		out = append(out, &v1.XAssetPendingEntryRow{
			ComboId: c.ID, ComboLabel: label, EntryKind: kind,
			WaitReason: reason, Ready: ready,
			Tier: tier, TierDetail: detail, TierWarn: warn,
		})
	}
	return out, nil
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

// probeGates 七闸序（同 handleOpportunity 序）：venue 白名单→旗标→regime→tick→监控簿→cap→就绪。
// 返回 kind/waitReason/ready。
func (s *Service) probeGates(ctx context.Context, c pgstore.XAssetComboAdminRow, venueAllow, canonAllow map[string]bool, flags map[string]pgstore.VenueSymbolFlag, caps map[string]pgstore.VenueExposureCap) (kind, reason string, ready bool) {
	buyMT := strings.HasSuffix(c.VenueBuy, ":mt4") || strings.HasSuffix(c.VenueBuy, ":mt5")
	sellMT := strings.HasSuffix(c.VenueSell, ":mt4") || strings.HasSuffix(c.VenueSell, ":mt5")
	// D-478 返修 R6：kind 推断=「恰好一条 MT 腿→XASSET_CARRY」（buy/sell 双向——原实现缺
	// MT 卖腿分支，生产实证 row1 CLUSDT(gate:perp)×WTI(277842155:mt5) 误报「构型未覆盖」）；
	// 死分支（VenueBuy=="" 空表值/重复 spot×perp 判）并入整理；perp×perp/spot×spot/双 MT
	// →kind 留空=「构型未覆盖」如实。
	switch {
	case buyMT != sellMT:
		kind = "XASSET_CARRY"
	case strings.HasSuffix(c.VenueBuy, ":spot") && strings.HasSuffix(c.VenueSell, ":perp"):
		kind = "FUNDING_HEDGE" // 正向（spot 买×perp 卖）
	case strings.HasSuffix(c.VenueBuy, ":perp") && strings.HasSuffix(c.VenueSell, ":spot"):
		kind = "FUNDING_HEDGE" // 反构型（perp 买腿）→ 下方构型禁入分支接住（SSOT 标签不失义）
	}
	// R5：构型未覆盖（双 CEX/未知形态）→「构型未覆盖」短路，不走七闸（同所组合不产两型 opp）。
	if kind == "" {
		return kind, "构型未覆盖", false
	}
	// 闸 1：venue/canonical allow_rules（engine allowRuleReject 同判据，kind='venue'/'canonical' 值查表）。
	// R2：双腿分别点名实际未放行腿（原实现恒报 buy 腿=误导）。
	if !venueAllow[c.VenueBuy] {
		return kind, fmt.Sprintf("venue 未放行: %s", c.VenueBuy), false
	}
	if !venueAllow[c.VenueSell] {
		return kind, fmt.Sprintf("venue 未放行: %s", c.VenueSell), false
	}
	if len(canonAllow) > 0 && !canonAllow[c.CanonicalBuy] {
		return kind, fmt.Sprintf("canonical 未放行: %s", c.CanonicalBuy), false
	}
	// 闸 2：旗标投影（engine flagRuleReject 同源 flags；blocked/warn 均透出为拦路原因）。
	for _, v := range []string{bareVenueKey(c.VenueBuy), bareVenueKey(c.VenueSell)} {
		for _, sym := range []string{c.CanonicalBuy, c.CanonicalSell, "*"} {
			if f, ok := flags[pgstore.FlagKey(v, sym)]; ok {
				return kind, fmt.Sprintf("旗标 %s: %s:%s %s", f.Level, v, sym, f.Reason), false
			}
		}
	}
	// 构型：FUNDING_HEDGE 反构型（perp 买腿）→ 构型禁入（D-473 v1 正向钉死）。
	if kind == "FUNDING_HEDGE" && strings.HasSuffix(c.VenueBuy, ":perp") {
		return kind, "构型禁入(v1 正向)", false
	}
	// 闸 3：regime 闸（两型分流，reason=各闸原输出）。
	if kind == "XASSET_CARRY" {
		if ok, r := exec.CarryFundingRegimeCheck(ctx, s.store, c.VenueBuy, c.VenueSell, c.CanonicalBuy, c.CanonicalSell, time.Now(), exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12}); !ok {
			return kind, "regime 未持续: " + r, false
		}
	}
	if kind == "FUNDING_HEDGE" {
		perpVenue := strings.TrimSuffix(c.VenueSell, ":perp")
		rows, err := s.store.FundingPredWindow(ctx, perpVenue, c.CanonicalBuy, time.Now().Add(-24*time.Hour))
		if err != nil {
			return kind, fmt.Sprintf("regime 窗口查询失败(fail-closed): %v", err), false
		}
		if gate, _, r := exec.FundingRegimeOK(rows, +1, 2.0, 12, false); !gate {
			return kind, "regime 未持续: " + r, false
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
			return kind, fmt.Sprintf("报价超窗: %s/%s（>5min 无 tick）", bareVenueKey(leg.venue), sym), false
		}
	}
	// 闸 5：XASSET 型监控簿 30min 闸（XAssetMonitorRowNear 现成接口；FUNDING_HEDGE 无簿概念跳过）。
	if kind == "XASSET_CARRY" {
		pair := monitorPairKey(c)
		cexV, mtV := monitorLegKeys(c)
		if _, ok, err := s.store.XAssetMonitorRowNear(ctx, pair, cexV, mtV, time.Now(), monitorFreshWindow); err != nil || !ok {
			return kind, "监控簿过期", false
		}
	}
	// 闸 6：venue 敞口 cap（475a 三态同判据：cap=0 冻结 / Σ超限）。
	for _, v := range []string{bareVenueKey(c.VenueBuy), bareVenueKey(c.VenueSell)} {
		if capRow, ok := caps[v]; ok && capRow.Enabled {
			if capRow.CapUSD == 0 {
				return kind, fmt.Sprintf("cap 冻结: venue %s 敞口闸冻结新批准（cap=0）", v), false
			}
			sum, err := s.venueExposureSum(v)
			if err != nil {
				return kind, fmt.Sprintf("cap 查询失败: venue %s %v（fail-closed）", v, err), false
			}
			if sum > capRow.CapUSD {
				return kind, fmt.Sprintf("cap 超限: venue %s Σ$%.0f > cap $%.0f", v, sum, capRow.CapUSD), false
			}
		}
	}
	// 闸 7：全过 → 条件就绪·等机会（诚实区分：闸过但 detector 未产 opp=价差/息差未到阈）。
	return kind, "条件就绪·等机会", true
}

// comboTier D-478 S4：安全档位（服务端算，前端只渲染）。
// needed=notional_usd（NULL→MaxGrossUSD 回退，仍缺→unset）；vs 腿 venue 权益×crossCap；
// 权益源=fin_venue_account_snapshot 最新行（>5min=缺数据不猜）；任一腿超档即 warn。
func (s *Service) comboTier(ctx context.Context, c pgstore.XAssetComboAdminRow, snaps []pgstore.VenueAccountLatest) (tier, detail string, warn bool) {
	needed := 0.0
	switch {
	case c.NotionalUSD != nil:
		needed = *c.NotionalUSD
	case c.MaxGrossUSD > 0:
		needed = c.MaxGrossUSD
	default:
		return "unset", "档位:未设", false
	}
	cap := s.crossCap
	if cap <= 0 {
		cap = defaultCrossCap
	}
	for _, leg := range []struct{ venue string }{{c.VenueBuy}, {c.VenueSell}} {
		v := bareVenueKey(leg.venue)
		eq, at, found := venueEquity(snaps, v)
		if !found || time.Since(at) > snapshotStaleWindow || eq <= 0 {
			return "missing_data", fmt.Sprintf("档位:缺数据（%s 快照缺失/过期）", v), false
		}
		if needed > eq*cap {
			return "over", fmt.Sprintf("超档: 名义$%.0f > 权益$%.0f×%.1f（%s）", needed, eq, cap, v), true
		}
	}
	return "ok", fmt.Sprintf("档位: 名义$%.0f ≤ 权益×%.1f", needed, cap), false
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

// venueExposureSum 475a 同判据的 venue 已挂名义 Σ（enabled 组合 notional/MaxGross 回退）。
// err 传播=调用方 fail-closed 转「cap 查询失败」reason（R3：原吞 err 返 0=fail-open 误导）。
func (s *Service) venueExposureSum(venue string) (float64, error) {
	combos, err := s.store.ListXAssetAllowCombosAll(context.Background())
	if err != nil {
		return 0, err
	}
	sum := 0.0
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		if bareVenueKey(c.VenueBuy) != venue && bareVenueKey(c.VenueSell) != venue {
			continue
		}
		if c.NotionalUSD != nil {
			sum += *c.NotionalUSD
		} else {
			sum += c.MaxGrossUSD
		}
	}
	return sum, nil
}

// monitorPairKey / monitorLegKeys paper_all_in.go 同式镜像（pair=cexCanon+"-"+bare(mtCanon)）。
func monitorPairKey(c pgstore.XAssetComboAdminRow) string {
	mtCanon := c.CanonicalBuy
	cexCanon := c.CanonicalSell
	if strings.HasSuffix(c.VenueSell, ":mt4") || strings.HasSuffix(c.VenueSell, ":mt5") {
		mtCanon, cexCanon = c.CanonicalSell, c.CanonicalBuy
	}
	bare := strings.TrimSuffix(mtCanon, "USDT")
	if bare == mtCanon {
		bare = strings.TrimSuffix(mtCanon, "USD")
	}
	return cexCanon + "-" + bare
}

func monitorLegKeys(c pgstore.XAssetComboAdminRow) (cexVenue, mtVenue string) {
	if strings.HasSuffix(c.VenueSell, ":mt4") || strings.HasSuffix(c.VenueSell, ":mt5") {
		return legBare(c.VenueBuy, "perp"), legBare(c.VenueSell, "mt5")
	}
	return legBare(c.VenueSell, "perp"), legBare(c.VenueBuy, "mt5")
}

func legBare(venueKey, suffix string) string {
	if i := strings.Index(venueKey, ":"); i >= 0 {
		return venueKey[:i]
	}
	return venueKey + ":" + suffix
}

// legMarket 腿市场形态 → (venue_type, symbol)：MT 腿 ticks=mt4/mt5+canonical；
// cex perp/spot 腿 symbol=canonical（tick 源=采集器原生符号，缺=不设闸）。
func legMarket(venueKey, canonical string) (vt, sym string) {
	switch {
	case strings.HasSuffix(venueKey, ":mt5"):
		return "mt5", canonical
	case strings.HasSuffix(venueKey, ":mt4"):
		return "mt4", canonical
	case strings.HasSuffix(venueKey, ":perp"):
		return "perp", canonical
	case strings.HasSuffix(venueKey, ":spot"):
		return "spot", canonical
	}
	return "", ""
}

// bareVenueKey venue 键剥 :后缀（flags/caps/快照 bare 域对齐 engine bareVenue）。
func bareVenueKey(venueKey string) string {
	if i := strings.Index(venueKey, ":"); i >= 0 {
		return venueKey[:i]
	}
	return venueKey
}

// venueEquity 快照按 bare venue 取权益（优先 perp/unified 行）。
func venueEquity(snaps []pgstore.VenueAccountLatest, venue string) (eq float64, at time.Time, ok bool) {
	for _, r := range snaps {
		if r.Venue != venue {
			continue
		}
		if r.EquityRaw == nil {
			continue
		}
		if r.VenueType == "perp" || r.VenueType == "unified" {
			return *r.EquityRaw, r.FetchedAt, true
		}
	}
	for _, r := range snaps {
		if r.Venue == venue && r.EquityRaw != nil {
			return *r.EquityRaw, r.FetchedAt, true
		}
	}
	return 0, time.Time{}, false
}
