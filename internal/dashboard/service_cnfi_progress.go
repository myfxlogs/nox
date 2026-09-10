package dashboard

// CN-FI P3：四层进度 + 手动持仓录入（L2 铁律；响应零金额字段，D-047）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

var cnfiLayers = []string{"liquidity", "coupon", "locked", "opportunity"}

func (s *Service) GetCNFIProgress(ctx context.Context, req *connect.Request[v1.GetCNFIProgressRequest]) (*connect.Response[v1.GetCNFIProgressResponse], error) {
	rows, err := s.store.ListCNFIHoldingsValued(ctx)
	if err != nil {
		return nil, err
	}
	targets, err := s.store.CNFILayerTargets(ctx)
	if err != nil {
		return nil, err
	}
	total := 0.0
	perLayer := map[string]*v1.CnfiLayerProgress{}
	valuedAsOf := ""
	for _, r := range rows {
		total += r.MarketValue
		lp, ok := perLayer[r.Layer]
		if !ok {
			lp = &v1.CnfiLayerProgress{Layer: r.Layer}
			perLayer[r.Layer] = lp
		}
		lp.Holdings++
		if r.NAVAsOf > valuedAsOf {
			valuedAsOf = r.NAVAsOf
		}
	}
	for _, r := range rows {
		if total > 0 {
			perLayer[r.Layer].ActualPct += r.MarketValue / total * 100
		}
	}
	resp := &v1.GetCNFIProgressResponse{ValuedAsOf: valuedAsOf}
	for _, layer := range cnfiLayers {
		lp, ok := perLayer[layer]
		if !ok {
			lp = &v1.CnfiLayerProgress{Layer: layer}
		}
		lp.TargetPct = targets[layer] // 未配置=0（不猜）
		resp.Layers = append(resp.Layers, lp)
	}
	if err := s.enrichProgressExpectation(ctx, resp); err != nil {
		return nil, err
	}
	// D-247 P1 S4：候选池对比（每层一张，服务端算）。
	layers, err := buildCNFICompare(ctx, s, time.Now())
	if err != nil {
		return nil, err
	}
	resp.LayerCompares = layers
	return connect.NewResponse(resp), nil
}

// AddCNFIAction 记录一笔行动（投资笔记；buy/sell 强制 rationale；快照当时信号状态）。
func (s *Service) AddCNFIAction(ctx context.Context, req *connect.Request[v1.AddCNFIActionRequest]) (*connect.Response[v1.AddCNFIActionResponse], error) {
	m := req.Msg
	resp := &v1.AddCNFIActionResponse{}
	validLayers := map[string]bool{"liquidity": true, "coupon": true, "locked": true, "opportunity": true, "none": true}
	validTypes := map[string]bool{"buy": true, "sell": true, "dividend": true, "note": true}
	if !validLayers[m.Layer] || !validTypes[m.ActionType] {
		resp.Error = "layer/action_type 非法"
		return connect.NewResponse(resp), nil
	}
	day := m.ActionDate
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		resp.Error = fmt.Sprintf("action_date 格式应为 YYYY-MM-DD: %v", err)
		return connect.NewResponse(resp), nil
	}
	snap, err := s.store.CNFISignalSnapshotJSON(ctx)
	if err != nil {
		return nil, err
	}
	var navAt *float64
	var ratePct *float64
	if m.RatePct != nil {
		ratePct = m.RatePct
	}
	id, err := s.store.AddCNFIAction(ctx, m.Code, m.Layer, m.ActionType, m.Qty, m.AmountCny,
		navAt, ratePct, string(snap), m.Rationale, day)
	if err != nil {
		resp.Error = err.Error()
		return connect.NewResponse(resp), nil
	}
	_ = id
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

func (s *Service) ListCNFIActions(ctx context.Context, req *connect.Request[v1.ListCNFIActionsRequest]) (*connect.Response[v1.ListCNFIActionsResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.store.ListCNFIActions(ctx, limit)
	if err != nil {
		return nil, err
	}
	resp := &v1.ListCNFIActionsResponse{}
	for _, r := range rows {
		row := &v1.CnfiActionRow{
			Id: r.ID, Code: r.Code, Layer: r.Layer, ActionType: r.ActionType,
			Qty: r.Qty, AmountCny: r.AmountCny, Rationale: r.Rationale,
			ActionDate: r.ActionDate, SignalSnapshot: r.SignalSnapshot,
		}
		if r.RatePct != nil {
			row.RatePct = r.RatePct
		}
		resp.Actions = append(resp.Actions, row)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) AddCNFIHolding(ctx context.Context, req *connect.Request[v1.AddCNFIHoldingRequest]) (*connect.Response[v1.AddCNFIHoldingResponse], error) {
	m := req.Msg
	resp := &v1.AddCNFIHoldingResponse{}
	// 输入校验（层枚举/正数/日期格式）；真实资金动作仍由业主线下执行，此处仅记账（L2 记录，非下单）。
	validLayer := false
	for _, l := range cnfiLayers {
		if m.Layer == l {
			validLayer = true
		}
	}
	if !validLayer {
		resp.Error = fmt.Sprintf("invalid layer %q", m.Layer)
		return connect.NewResponse(resp), nil
	}
	if m.Code == "" || m.Qty <= 0 || m.CostValue <= 0 {
		resp.Error = "code/qty/cost_value 必填且为正"
		return connect.NewResponse(resp), nil
	}
	day := m.OpenedDate
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		resp.Error = fmt.Sprintf("opened_date 格式应为 YYYY-MM-DD: %v", err)
		return connect.NewResponse(resp), nil
	}
	if _, err := s.store.AddCNFIHolding(ctx, m.Code, m.Layer, m.Qty, m.CostValue, day, strings.TrimSpace(m.Note)); err != nil {
		return nil, err
	}
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// CNFIProgress P2 扩展（D-242）：预期年化列（公开参数 signal_config）+ 预期金额（服务端算，运行时数据）。
func (s *Service) enrichProgressExpectation(ctx context.Context, resp *v1.GetCNFIProgressResponse) error {
	var expected map[string]float64
	if raw, err := s.store.CNFISignalConfigJSON(ctx, "layer_expected_annual"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &expected)
	}
	for _, lp := range resp.Layers {
		if e, ok := expected[lp.Layer]; ok {
			lp.ExpectedAnnualPct = e
			lp.ExpectedAnnualCny = lp.ActualPct / 100 * 0 // 市值合计在下方重算后填充
		}
	}
	// 金额按层市值：重算 holdings 市值（服务端算，D-043：前端只渲染）
	rows, err := s.store.ListCNFIHoldingsValued(ctx)
	if err != nil {
		return err
	}
	layerMV := map[string]float64{}
	for _, r := range rows {
		layerMV[r.Layer] += r.MarketValue
	}
	for _, lp := range resp.Layers {
		if e, ok := expected[lp.Layer]; ok {
			lp.ExpectedAnnualCny = layerMV[lp.Layer] * e / 100
		}
		lp.MarketValueCny = layerMV[lp.Layer]
	}
	return nil
}

// ListRepoOps 列出逆回购操作记录（D-270 周四操作闭环审计）。
func (s *Service) ListRepoOps(ctx context.Context, req *connect.Request[v1.ListRepoOpsRequest]) (*connect.Response[v1.ListRepoOpsResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.store.ListRepoOps(ctx, limit)
	if err != nil {
		return nil, err
	}
	resp := &v1.ListRepoOpsResponse{}
	for _, r := range rows {
		row := &v1.RepoOpRow{
			Id: r.ID, OpportunityWeek: r.OpportunityWeek,
			InstrumentCode: r.InstrumentCode, InstrumentName: r.InstrumentName,
			Status: r.Status, TriggerRatePct: r.RatePctAtTrigger,
			HoldingDays: int32(r.HoldingDays), ReminderCount: int32(r.ReminderCount),
			CreatedAt: r.CreatedAt.Format("2006-01-02 15:04"),
			UpdatedAt: r.UpdatedAt.Format("2006-01-02 15:04"),
		}
		if r.ExpectedReturnCny != nil {
			row.ExpectedReturnCny = *r.ExpectedReturnCny
		}
		if r.SignalSnapshot != nil {
			row.SignalSnapshot = *r.SignalSnapshot
		}
		if r.SkipReason != nil {
			row.SkipReason = *r.SkipReason
		}
		if r.OperatedAmountCny != nil {
			row.OperationAmountCny = *r.OperatedAmountCny
		}
		if r.OperatedRatePct != nil {
			row.OperationRatePct = *r.OperatedRatePct
		}
		resp.Ops = append(resp.Ops, row)
	}
	return connect.NewResponse(resp), nil
}
