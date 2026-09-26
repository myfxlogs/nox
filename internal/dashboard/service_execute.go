package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/exec"
	"arb/internal/safego"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// --- 执行（§13） ---

// checkFreezeGuard D-155 交易时段防线：开仓前双腿各自校验「该腿报价在窗口内发生过价格值变化」。
// 任一腿冻结（从未变化 / 超窗口未变）→ 拒绝整组（LEGGED 零容忍，单腿不降级）。
// freezeGuard≤0 = 禁用（全放行）。预检在 pipeline.Execute 前拦截，不落 position、不开任何腿。
// 与 D-104 freshness 分层：freshness=采集侧时间戳门禁（心跳报价骗得过），freeze guard=执行侧价格变化门禁（骗不过）。
// D-311 S4：双腿各自 canonical——BUY 腿用 buyCanonical、其余腿用 sellCanonical 查 LastChangeAt
// （修前单 canonical：跨 canonical 卖腿 tick 挂在 canonical_sell 下 → 永判 frozen 拒）。
func (s *Service) checkFreezeGuard(buyCanonical, sellCanonical string, legs []exec.LegSpec) error {
	if s.freezeGuard <= 0 {
		return nil
	}
	now := time.Now()
	for _, l := range legs {
		canonical := sellCanonical
		if l.Side == "BUY" {
			canonical = buyCanonical
		}
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
			// D-433：拒因显式化——LegSpec 无原始请求量字段，统一提示两态排查方向：
			// 请求量>0 被 floor 抹零（低于 venue 最小步长）或上游产出即 0（未知品种/上游 bug）。
			return fmt.Errorf("reject combo: leg %s %s qty invalid (%.6f≤0, 低于venue最小步长或未知品种——检查 notional_usd 是否低于该腿最小合约名义额)", l.VenueKey, l.VenueSymbol, l.Qty)
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
	case "FUNDING_HEDGE", "PERP_FUNDING", "XASSET_CARRY": // D-332：XASSET net_bps=年化百分点同口径（484-488 实证），归 FUNDING 直存非 CARRY 折算
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

func (s *Service) ClosePosition(ctx context.Context, req *connect.Request[v1.ClosePositionRequest]) (*connect.Response[v1.ClosePositionResponse], error) {
	if s.pipeline == nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: "executor not wired",
		}), nil
	}
	s.logCloseTrigger(ctx, req.Msg.GetPositionId(), "manual_rpc") // D-245 触发源持久化
	// D-495 S5：force=true=业主显式逃逸门（前端 confirm 后重发）；ErrLegNotActionable
	// 透传 §1.3 定稿文案+leg_not_actionable 标记（前端据标记弹 confirm，不做判定）。
	var closeErr error
	if req.Msg.GetForce() {
		closeErr = s.pipeline.ClosePositionForce(ctx, req.Msg.GetPositionId())
	} else {
		closeErr = s.pipeline.ClosePosition(ctx, req.Msg.GetPositionId())
	}
	if closeErr != nil {
		msg := closeErr.Error()
		var lae *exec.LegNotActionableError
		if errors.As(closeErr, &lae) {
			msg = fmt.Sprintf("leg_not_actionable: 品种 %s@%s 当前无可交易行情（休市或断流），强制平仓将导致单腿裸露——如确需平仓请勾选强制",
				lae.Symbol, lae.VenueKey)
		}
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: msg,
		}), nil
	}
	// D-087 §3.2：手动平仓 → settlement.Settle（fire-and-forget，不阻塞 RPC）
	// D-159 W2a R3-9：settlement 失败 → 标记 settle_failed 到 position.note（可重试状态）。
	if s.settler != nil {
		safego.Go("service_execute:settle", func() {
			posID := req.Msg.GetPositionId()
			if err := s.settler.Settle(context.Background(), posID); err != nil {
				log.Printf("[dashboard] settle pos=%d failed (retry next round): %v", posID, err)
				_ = s.store.SetPositionNote(context.Background(), posID, "settle_failed: "+err.Error())
			}
		})
	}
	// 平仓成功 → 触发统计复盘（§13.6，D-034；fire-and-forget，不阻塞 RPC）
	if s.reviewer != nil {
		safego.Go("service_execute:reviewer", s.reviewer)
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
//
// D-311 S7：GetBrokerAccount → ListVenueIsReal 合并视图（broker_accounts.is_real ∪
// exchange_configs.simulated 取反）——CEX venue 开始可判；MT 语义不变（严格超集）。
func (s *Service) checkEnvironmentIsReal(ctx context.Context, venueBuy, venueSell string) string {
	wantLive := s.environment == "live"
	isRealMap, err := s.store.ListVenueIsReal(ctx)
	if err != nil {
		return fmt.Sprintf("environment check: is_real map query failed (%v)", err)
	}
	for _, venueKey := range []string{venueBuy, venueSell} {
		// D-371 S2：venueKey 全键直查
		if venueKey == "" {
			return "environment check: empty venueKey"
		}
		isReal, known := isRealMap[venueKey]
		if !known {
			return fmt.Sprintf("environment check: venue %s not found", venueKey)
		}
		if isReal == nil {
			return fmt.Sprintf("environment check: venue %s is_real unknown (null)", venueKey)
		}
		isLive := *isReal
		if isLive != wantLive {
			return fmt.Sprintf("environment check: venue %s is_real=%v != environment=%s", venueKey, isLive, s.environment)
		}
	}
	return ""
}

// checkLiveGate D-159 W6a S2：LIVE 人工开关检查（手动入口同源）。
// LiveTrade=false 时，任一腿 is_real=true → 返回拒绝原因；demo 放行。
// 校验逻辑与 autotrade.Gate.liveGateCheck 同源。
// D-311 S7：同 checkEnvironmentIsReal 切 ListVenueIsReal 合并视图（CEX venue 可判）。
func (s *Service) checkLiveGate(ctx context.Context, venueBuy, venueSell string) string {
	isRealMap, err := s.store.ListVenueIsReal(ctx)
	if err != nil {
		return fmt.Sprintf("live gate: is_real map query failed (%v)", err)
	}
	for _, venueKey := range []string{venueBuy, venueSell} {
		// D-371 S2：venueKey 全键直查
		if venueKey == "" {
			return "live gate: empty venueKey"
		}
		isReal, known := isRealMap[venueKey]
		if !known {
			return fmt.Sprintf("live gate: venue %s not found", venueKey)
		}
		if isReal == nil {
			return fmt.Sprintf("live gate: venue %s is_real unknown (null)", venueKey)
		}
		if *isReal {
			return fmt.Sprintf("live trade disabled (ARB_LIVE_TRADE=false, venueKey=%s is_real=true)", venueKey)
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

// logCloseTrigger D-245 缺口2：手动 RPC 平仓触发源持久化。
// Position 无按 ID 单取方法（pgstore 既有面），走 ListActivePositions 过滤；未命中按空字段落行（NOT NULL 空串容忍）。
// 写失败只 log 不阻断（观测路径不挡执行路径）。
func (s *Service) logCloseTrigger(ctx context.Context, posID int64, reason string) {
	var pos *pgstore.Position
	if actives, err := s.store.ListActivePositions(ctx); err == nil {
		for i := range actives {
			if actives[i].ID == posID {
				pos = &actives[i]
				break
			}
		}
	}
	l := &pgstore.AutotradeLog{PositionID: &posID, Status: "CLOSE_TRIGGER", Reason: reason}
	if pos != nil {
		l.OpportunityID = pos.OpportunityID
		l.Strategy = pos.Type
	}
	if err := s.store.InsertAutotradeLog(ctx, l); err != nil {
		log.Printf("[dashboard] D-245 CLOSE_TRIGGER 落库失败 pos=%d: %v", posID, err)
	}
}
