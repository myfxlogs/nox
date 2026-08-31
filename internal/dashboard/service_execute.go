package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// --- 执行（§13） ---

// checkFreezeGuard D-155 交易时段防线：开仓前双腿各自校验「该腿报价在窗口内发生过价格值变化」。
// 任一腿冻结（从未变化 / 超窗口未变）→ 拒绝整组（LEGGED 零容忍，单腿不降级）。
// freezeGuard≤0 = 禁用（全放行）。预检在 pipeline.Execute 前拦截，不落 position、不开任何腿。
// 与 D-104 freshness 分层：freshness=采集侧时间戳门禁（心跳报价骗得过），freeze guard=执行侧价格变化门禁（骗不过）。
func (s *Service) checkFreezeGuard(canonical string, legs []exec.LegSpec) error {
	if s.freezeGuard <= 0 {
		return nil
	}
	now := time.Now()
	for _, l := range legs {
		t, ok := s.qbus.LastChangeAt(canonical, l.VenueKey)
		if !ok {
			// 从未发生过价格变化（首见 / 仅心跳）→ 冻结
			log.Printf("[exec] freeze guard: leg %s %s price frozen (lastChange=%v)", canonical, l.VenueKey, t)
			return fmt.Errorf("reject combo: leg %s %s price frozen (no price change observed)", l.VenueKey, l.VenueSymbol)
		}
		if age := now.Sub(t); age > s.freezeGuard {
			log.Printf("[exec] freeze guard: leg %s %s price frozen (lastChange=%v)", canonical, l.VenueKey, t)
			return fmt.Errorf("reject combo: leg %s %s price frozen (last change %v ago > %v)", l.VenueKey, l.VenueSymbol, age, s.freezeGuard)
		}
	}
	return nil
}

// validateLegQtys D-127 P3a：下单前预检——任一腿 qty<=0 → 拒 combo（fail-closed）。
// floorToStepQty 返回 0 = 未知品种（step=0）或 qty 过小取整后为 0；
// 旧路径会发 qty=0 订单被 venue 拒单 → 浪费一次 LEGGED 风险。
// 此预检在 pipeline.Execute 前拦截，不落 position、不开任何腿。
func (s *Service) validateLegQtys(legs []exec.LegSpec) error {
	for _, l := range legs {
		if l.Qty <= 0 {
			return fmt.Errorf("reject combo: leg %s %s qty invalid (%.6f≤0)", l.VenueKey, l.VenueSymbol, l.Qty)
		}
	}
	return nil
}

// metaForType 按策略型构造 PositionMeta（D-087 §3.4 typed 化）。
// openBps 语义随型不同：收敛型=一次性 bps / SWAPARB=入场 spread / CARRY=net bps / FUNDING 族=年化百分点。
// SWAPARB 的 SwapBpsDaily 由调用方（trySwapArb）事后补写——本函数无 netSwap 值，仅写 EntrySpreadBps。
func metaForType(posType string, openBps float64) pgstore.PositionMeta {
	v := openBps
	switch posType {
	case "SWAPARB":
		return pgstore.PositionMeta{EntrySpreadBps: &v}
	case "CARRY":
		annual := v * 365 / 100
		return pgstore.PositionMeta{NetBps: &v, AnnualPct: &annual}
	case "FUNDING_HEDGE", "PERP_FUNDING":
		return pgstore.PositionMeta{NetBps: &v, AnnualPct: &v}
	default: // CROSS_VENUE / MEANREVERT / 未知
		return pgstore.PositionMeta{NetBps: &v}
	}
}

func (s *Service) ExecuteOpportunity(ctx context.Context, req *connect.Request[v1.ExecuteOpportunityRequest]) (*connect.Response[v1.ExecuteOpportunityResponse], error) {
	if s.pipeline == nil {
		return connect.NewResponse(&v1.ExecuteOpportunityResponse{
			Status:  "FAILED",
			Message: "executor not wired",
		}), nil
	}
	status, msg, posID := s.ExecuteOpportunityInternal(ctx, req.Msg.GetOpportunityId(), req.Msg.GetRequestId(), req.Msg.GetQty())
	return connect.NewResponse(&v1.ExecuteOpportunityResponse{
		PositionId: posID,
		Status:     status,
		Message:    msg,
	}), nil
}

// ExecuteOpportunityInternal 执行机会的核心逻辑（D-070 抽取，供 RPC 和 autotrade engine 复用）。
// 返回 status（EXECUTED/FAILED）、message、positionID（失败时为0）。
func (s *Service) ExecuteOpportunityInternal(ctx context.Context, oppID, requestID string, qty float64) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "executor not wired", 0
	}
	if qty <= 0 {
		return "FAILED", "invalid quantity", 0
	}
	opps, err := s.store.ListOpportunities(ctx, "", 200, 0)
	if err != nil {
		return "FAILED", fmt.Sprintf("list opportunities: %v", err), 0
	}
	var found *pgstore.Opportunity
	for i := range opps {
		if opps[i].ID == oppID {
			found = &opps[i]
			break
		}
	}
	if found == nil {
		return "FAILED", "opportunity not found or expired", 0
	}
	// D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY——非 RUNNING 拒绝开仓（fail-closed）。
	state, err := s.checkAutotradeState(ctx)
	if err != nil {
		return "FAILED", fmt.Sprintf("autotrade state: %v (fail-closed)", err), 0
	}
	if state != "RUNNING" {
		return "FAILED", fmt.Sprintf("autotrade not RUNNING (state=%s), open rejected", state), 0
	}
	if !s.pipeline.Revalidate(found.Canonical, found.VenueBuy, found.VenueSell, found.Type, found.GrossBps) {
		return "FAILED", "quote stale or net benefit degraded beyond threshold", 0
	}
	if err := s.checkTradability(found); err != nil {
		return "FAILED", err.Error(), 0
	}
	legs, err := s.resolveLegs(found, qty)
	if err != nil {
		return "FAILED", err.Error(), 0
	}
	if err := s.validateLegQtys(legs); err != nil { // D-127 P3a：qty 预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	if err := s.checkFreezeGuard(found.Canonical, legs); err != nil { // D-155：冻结腿预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	// D-159 W1a R1：SWAPARB 精确组合门禁（手动入口同源，pipeline.Execute 前拦截）。
	// swapArbComboAllow 非 nil 时检查；nil = 未接线（gate 层兜底）。
	if found.Type == "SWAPARB" && len(s.swapArbComboAllow) > 0 {
		if exec.MatchComboAllow(s.swapArbComboAllow, found.Canonical, found.VenueBuy, found.VenueSell) == nil {
			return "FAILED", fmt.Sprintf("combo not in allowlist: %s %s×%s", found.Canonical, found.VenueBuy, found.VenueSell), 0
		}
	}
	// D-159 W1a I6：environment/is_real 一致性检查（手动入口同源，pipeline.Execute 前拦截）。
	if s.environment != "" {
		if msg := s.checkEnvironmentIsReal(ctx, found.VenueBuy, found.VenueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	// D-159 W6a S2：LIVE 人工开关（手动入口同源，pipeline.Execute 前拦截）。
	if !s.liveTrade {
		if msg := s.checkLiveGate(ctx, found.VenueBuy, found.VenueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, found.Type, legs)
	if err != nil {
		if errors.Is(err, exec.ErrMaxOpenPositions) || errors.Is(err, exec.ErrDuplicateExecution) ||
			errors.Is(err, exec.ErrEmptyPositionType) || errors.Is(err, exec.ErrEmptyLegs) {
			return "FAILED", err.Error(), 0
		}
		return "FAILED", fmt.Sprintf("execute: %v", err), 0
	}
	// D-070：回填 position 元数据（expected_net_bps/expected_annual_pct；type 已在创建时写入，修复⑭ S4）
	// D-087：typed 化（metaForType 按型构造 PositionMeta）
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, found.Type, metaForType(found.Type, found.NetBps))
	}
	return result.Status, "", result.PositionID
}

// ExecuteMeanRevert D-070 均值回归直接执行（绕过 detector，autotrade engine 专用）。
// 直接接收 canonical + venueBuy + venueSell + qty，构造 LegSpec 并执行。
// maxNotional: CFD 名义额上限（0=用默认$10K）。
// 返回 status、message、positionID。
func (s *Service) ExecuteMeanRevert(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty float64, openBps float64) (status, message string, positionID int64) {
	return s.ExecuteMeanRevertWithCap(ctx, canonical, venueBuy, venueSell, requestID, qty, openBps, 10000.0, "MEANREVERT")
}

// ExecuteMeanRevertWithCap 带 notional cap 参数的均值回归执行（swap 套利可用更大 cap）。
// strategyType: 写入 positions.type（D-072 §4.1，SWAPARB/MEANREVERT）。
func (s *Service) ExecuteMeanRevertWithCap(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty float64, openBps float64, maxNotional float64, strategyType string) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "executor not wired", 0
	}
	if qty <= 0 {
		return "FAILED", "invalid quantity", 0
	}
	// D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY——非 RUNNING 拒绝开仓（fail-closed）。
	state, err := s.checkAutotradeState(ctx)
	if err != nil {
		return "FAILED", fmt.Sprintf("autotrade state: %v (fail-closed)", err), 0
	}
	if state != "RUNNING" {
		return "FAILED", fmt.Sprintf("autotrade not RUNNING (state=%s), open rejected", state), 0
	}
	if maxNotional <= 0 {
		maxNotional = 10000.0
	}
	qbuy, okBuy := s.qbus.Latest(canonical, venueBuy)
	qsell, okSell := s.qbus.Latest(canonical, venueSell)
	if !okBuy || !okSell {
		return "FAILED", "buy/sell leg quote unavailable", 0
	}
	csBuy, err := s.legContractSizeOf(venueBuy, qbuy.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("buy CS: %v", err), 0
	}
	csSell, err := s.legContractSizeOf(venueSell, qsell.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("sell CS: %v", err), 0
	}
	sellQty := math.Round(qty*csBuy/csSell*100) / 100
	qty = s.floorToStepQty(venueBuy, qbuy.VenueSymbol, qty)
	sellQty = s.floorToStepQty(venueSell, qsell.VenueSymbol, sellQty)
	// CFD notional cap
	if isMTVenue(venueBuy) {
		notional := qty * csBuy * qbuy.Ask
		if notional > maxNotional {
			qty = s.floorToStepQtyMin(venueBuy, qbuy.VenueSymbol)
		}
	}
	if isMTVenue(venueSell) {
		notional := sellQty * csSell * qsell.Bid
		if notional > maxNotional {
			sellQty = s.floorToStepQtyMin(venueSell, qsell.VenueSymbol)
		}
	}
	// imbalance 检查
	ref := qbuy.Ask
	notionalBuy := qty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || imb > maxImb/100 {
		return "FAILED", fmt.Sprintf("leg notional imbalance: buy $%.2f vs sell $%.2f (%.2f%% > %.1f%%)",
			notionalBuy, notionalSell, imb*100, maxImb), 0
	}
	if err := s.checkLegVolume(venueBuy, qbuy.VenueSymbol, qty); err != nil {
		return "FAILED", err.Error(), 0
	}
	if err := s.checkLegVolume(venueSell, qsell.VenueSymbol, sellQty); err != nil {
		return "FAILED", err.Error(), 0
	}
	oppID := combo.Key("MEANREVERT", canonical, venueBuy, venueSell)
	legs := []exec.LegSpec{
		{VenueKey: venueBuy, VenueSymbol: qbuy.VenueSymbol, Side: "BUY", Qty: qty, PriceExpected: qbuy.Ask},
		{VenueKey: venueSell, VenueSymbol: qsell.VenueSymbol, Side: "SELL", Qty: sellQty, PriceExpected: qsell.Bid},
	}
	if err := s.validateLegQtys(legs); err != nil { // D-127 P3a：qty 预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	if err := s.checkFreezeGuard(canonical, legs); err != nil { // D-155：冻结腿预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	// D-159 W1a R1：SWAPARB 精确组合门禁（手动入口同源，pipeline.Execute 前拦截）。
	if strategyType == "SWAPARB" && len(s.swapArbComboAllow) > 0 {
		if exec.MatchComboAllow(s.swapArbComboAllow, canonical, venueBuy, venueSell) == nil {
			return "FAILED", fmt.Sprintf("combo not in allowlist: %s %s×%s", canonical, venueBuy, venueSell), 0
		}
	}
	// D-159 W1a I6：environment/is_real 一致性检查（手动入口同源，pipeline.Execute 前拦截）。
	if s.environment != "" {
		if msg := s.checkEnvironmentIsReal(ctx, venueBuy, venueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	// D-159 W6a S2：LIVE 人工开关（手动入口同源，pipeline.Execute 前拦截）。
	if !s.liveTrade {
		if msg := s.checkLiveGate(ctx, venueBuy, venueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, strategyType, legs)
	if err != nil {
		if errors.Is(err, exec.ErrMaxOpenPositions) || errors.Is(err, exec.ErrDuplicateExecution) ||
			errors.Is(err, exec.ErrEmptyPositionType) || errors.Is(err, exec.ErrEmptyLegs) {
			return "FAILED", err.Error(), 0
		}
		return "FAILED", fmt.Sprintf("execute: %v", err), 0
	}
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, strategyType, metaForType(strategyType, openBps))
	}
	return result.Status, "", result.PositionID
}

func (s *Service) ClosePosition(ctx context.Context, req *connect.Request[v1.ClosePositionRequest]) (*connect.Response[v1.ClosePositionResponse], error) {
	if s.pipeline == nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: "executor not wired",
		}), nil
	}
	if err := s.pipeline.ClosePosition(ctx, req.Msg.GetPositionId()); err != nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: err.Error(),
		}), nil
	}
	// D-087 §3.2：手动平仓 → settlement.Settle（fire-and-forget，不阻塞 RPC）
	// D-159 W2a R3-9：settlement 失败 → 标记 settle_failed 到 position.note（可重试状态）。
	if s.settler != nil {
		go func() {
			posID := req.Msg.GetPositionId()
			if err := s.settler.Settle(context.Background(), posID); err != nil {
				log.Printf("[dashboard] settle pos=%d failed (retry next round): %v", posID, err)
				_ = s.store.SetPositionNote(context.Background(), posID, "settle_failed: "+err.Error())
			}
		}()
	}
	// 平仓成功 → 触发统计复盘（§13.6，D-034；fire-and-forget，不阻塞 RPC）
	if s.reviewer != nil {
		go s.reviewer()
	}
	return connect.NewResponse(&v1.ClosePositionResponse{
		Status: "CLOSED",
	}), nil
}

// checkEnvironmentIsReal D-159 W1a I6：environment/is_real 一致性检查（手动入口同源）。
// 返回非空字符串 = 拒绝原因；空 = 通过。
// 校验逻辑与 autotrade.Gate.environmentIsRealCheck 同源：
//   - is_real=true ↔ environment='live' / is_real=false ↔ environment='demo'
//   - is_real=nil（未知）→ fail-closed 拒绝
//   - venue 不存在 → fail-closed 拒绝
func (s *Service) checkEnvironmentIsReal(ctx context.Context, venueBuy, venueSell string) string {
	wantLive := s.environment == "live"
	for _, venueKey := range []string{venueBuy, venueSell} {
		venue, _, _ := strings.Cut(venueKey, ":")
		if venue == "" {
			return fmt.Sprintf("environment check: empty venue in %s", venueKey)
		}
		acct, err := s.store.GetBrokerAccount(ctx, venue)
		if err != nil {
			return fmt.Sprintf("environment check: venue %s not found (%v)", venue, err)
		}
		if acct.IsReal == nil {
			return fmt.Sprintf("environment check: venue %s is_real unknown (null)", venue)
		}
		isLive := *acct.IsReal
		if isLive != wantLive {
			return fmt.Sprintf("environment check: venue %s is_real=%v != environment=%s", venue, isLive, s.environment)
		}
	}
	return ""
}

// checkLiveGate D-159 W6a S2：LIVE 人工开关检查（手动入口同源）。
// LiveTrade=false 时，任一腿 is_real=true → 返回拒绝原因；demo 放行。
// 校验逻辑与 autotrade.Gate.liveGateCheck 同源。
func (s *Service) checkLiveGate(ctx context.Context, venueBuy, venueSell string) string {
	for _, venueKey := range []string{venueBuy, venueSell} {
		venue, _, _ := strings.Cut(venueKey, ":")
		if venue == "" {
			return fmt.Sprintf("live gate: empty venue in %s", venueKey)
		}
		acct, err := s.store.GetBrokerAccount(ctx, venue)
		if err != nil {
			return fmt.Sprintf("live gate: venue %s not found (%v)", venue, err)
		}
		if acct.IsReal == nil {
			return fmt.Sprintf("live gate: venue %s is_real unknown (null)", venue)
		}
		if *acct.IsReal {
			return fmt.Sprintf("live trade disabled (ARB_LIVE_TRADE=false, venue=%s is_real=true)", venue)
		}
	}
	return ""
}

// checkAutotradeState D-159 W6a S5：手动 RPC 开仓前 autotrade state 检查（不绕 CLOSE_ONLY）。
// 返回 (state, error)：error != nil（含表空）→ fail-closed 拒绝开仓。
// state != "RUNNING" → 调用方拒绝开仓（CLOSE_ONLY 等状态不开仓）。
func (s *Service) checkAutotradeState(ctx context.Context) (string, error) {
	st, err := s.store.GetAutotradeState(ctx)
	if err != nil {
		return "", fmt.Errorf("get autotrade state: %w", err)
	}
	return st.State, nil
}
