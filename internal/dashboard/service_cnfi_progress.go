package dashboard

// CN-FI P3：四层进度 + 手动持仓录入（L2 铁律；响应零金额字段，D-047）。

import (
	"context"
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
