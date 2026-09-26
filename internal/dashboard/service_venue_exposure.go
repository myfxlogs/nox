// service_venue_exposure.go D-475a S6：venue 敞口闸 RPC——List 透传 / Set 校验+
// Upsert+kickConfig 即时生效+info 审计留痕。cap=0=冻结新批准（approve 敞口闸消费）。
package dashboard

import (
	"context"
	"fmt"
	"strings"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListVenueExposureCaps 闸表全量透传（前端「敞口上限」小节行源）。
func (s *Service) ListVenueExposureCaps(ctx context.Context, _ *connect.Request[v1.ListVenueExposureCapsRequest]) (*connect.Response[v1.ListVenueExposureCapsResponse], error) {
	caps, err := s.store.ListVenueExposureCaps(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.VenueExposureCapRow, 0, len(caps))
	for _, c := range caps {
		out = append(out, &v1.VenueExposureCapRow{
			VenueKey: c.VenueKey, CapUsd: c.CapUSD, Enabled: c.Enabled, Note: c.Note,
			UpdatedAt: timestamppb.New(c.UpdatedAt),
		})
	}
	return connect.NewResponse(&v1.ListVenueExposureCapsResponse{Rows: out}), nil
}

// SetVenueExposureCap 写/改闸：venue_key 非空+cap_usd>=0（负值 DB CHECK 也拒，
// 前置给友好文案）→Upsert→kickConfig（写即踢，敞口变更即时生效）+info 审计。
// 域内错误 in-band ok=false（前端可展示）。
func (s *Service) SetVenueExposureCap(ctx context.Context, req *connect.Request[v1.SetVenueExposureCapRequest]) (*connect.Response[v1.SetVenueExposureCapResponse], error) {
	g := req.Msg
	resp := &v1.SetVenueExposureCapResponse{}
	key := strings.TrimSpace(g.VenueKey)
	if key == "" {
		resp.Error = "venue_key 必填（账户级键：'3001157:mt5'/'okx:perp'）"
		return connect.NewResponse(resp), nil
	}
	if g.CapUsd < 0 {
		resp.Error = "cap_usd 禁负（0=显式全禁=冻结新批准）"
		return connect.NewResponse(resp), nil
	}
	if err := s.store.UpsertVenueExposureCap(ctx, &pgstore.VenueExposureCap{
		VenueKey: key, CapUSD: g.CapUsd, Enabled: g.Enabled, Note: g.Note,
	}); err != nil {
		resp.Error = fmt.Sprintf("写入失败: %v", err)
		return connect.NewResponse(resp), nil
	}
	s.kickConfig() // D-475a S6：Set→kick（nil 安全）
	if err := s.store.InsertAlert(ctx, nil, "info",
		fmt.Sprintf("[敞口闸] %s cap=$%.0f enabled=%v %s", key, g.CapUsd, g.Enabled, g.Note)); err != nil {
		resp.Error = fmt.Sprintf("已写入但审计告警失败: %v", err)
		return connect.NewResponse(resp), nil
	}
	s.PublishDataRefresh(RefreshAllowRules)
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// exposureCapGate D-475a S3 敞口闸（G5，从 approve 正文提取保行数）：
// 两腿 venue_key 各查 caps——行缺席→放行+capWarns 如实透出；enabled=false→跳过；
// cap=0→冻结新批准（不吊销存量 enabled 组合）；Σ存量+本次>cap→拒。读失败 fail-closed。
// proposed：NotionalUSD 优先，缺→MaxGrossUSD 保守兜底。返回 (warns, "") 或 (nil, 拒因)。
func (s *Service) exposureCapGate(ctx context.Context, legKeys []string, grossUSD float64, notionalUSD *float64) ([]string, string) {
	db := s.gradDB()
	caps, err := db.ListVenueExposureCaps(ctx)
	if err != nil {
		return nil, fmt.Sprintf("敞口闸表读取失败: %v（fail-closed 不批钱）", err)
	}
	existing, err := db.LoadXAssetAllowCombos(ctx, "live")
	if err != nil {
		return nil, fmt.Sprintf("白名单敞口汇总失败: %v", err)
	}
	sum := map[string]float64{} // venue_key→已挂敞口（双腿向 buy/sell 两键同记=保守高估）
	for _, r := range existing {
		exp := r.MaxGrossUSD
		if r.NotionalUSD != nil {
			exp = *r.NotionalUSD
		}
		sum[r.VenueBuy] += exp
		sum[r.VenueSell] += exp
	}
	proposed := grossUSD
	if notionalUSD != nil {
		proposed = *notionalUSD
	}
	var warns []string
	for _, legKey := range legKeys {
		capRow, ok := caps[legKey]
		if !ok {
			warns = append(warns, fmt.Sprintf("venue %s 未设敞口闸（fin_venue_exposure_caps 无行）", legKey))
			continue
		}
		if !capRow.Enabled {
			continue // 闸停用（行仍在档）
		}
		if capRow.CapUSD == 0 {
			return nil, fmt.Sprintf("venue %s 敞口闸冻结新批准（cap=0）", legKey)
		}
		if sum[legKey]+proposed > capRow.CapUSD {
			return nil, fmt.Sprintf("venue %s 敞口超限: 已挂 $%.0f+本次 $%.0f > cap $%.0f",
				legKey, sum[legKey], proposed, capRow.CapUSD)
		}
	}
	return warns, ""
}
