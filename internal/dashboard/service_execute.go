package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
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

func (s *Service) ClosePosition(ctx context.Context, req *connect.Request[v1.ClosePositionRequest]) (*connect.Response[v1.ClosePositionResponse], error) {
	if s.pipeline == nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: "executor not wired",
		}), nil
	}
	s.logCloseTrigger(ctx, req.Msg.GetPositionId(), "manual_rpc") // D-245 触发源持久化
	if err := s.pipeline.ClosePosition(ctx, req.Msg.GetPositionId()); err != nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: err.Error(),
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
