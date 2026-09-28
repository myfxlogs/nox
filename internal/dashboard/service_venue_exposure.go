// service_venue_exposure.go D-475a S6：venue 敞口闸 RPC——List 透传 / Set 校验+
// Upsert+kickConfig 即时生效+info 审计留痕。cap=0=冻结新批准（approve 敞口闸消费）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListVenueExposureCaps 闸表全量透传 + D-542 S3②：已配置账户但闸表无行的 venue_key
// 合成 unset 占位行（显式提示未设闸——binance:spot/perp 类缺闸 venue 不再隐身）。
func (s *Service) ListVenueExposureCaps(ctx context.Context, _ *connect.Request[v1.ListVenueExposureCapsRequest]) (*connect.Response[v1.ListVenueExposureCapsResponse], error) {
	caps, err := s.gradDB().ListVenueExposureCaps(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// D-547 S3①：live 面值——allocated=Σ白名单金额（口径同 exposureCapGate），
	// 池面=legPool 链（live 账本/REST 校准/快照兜底；缺取 source=空如实透出）。
	snaps, _ := s.gradDB().LatestVenueAccountSnapshots(ctx)
	existing, _ := s.gradDB().LoadXAssetAllowCombos(ctx, "live")
	allocated := exposureSumOf(existing)
	out := make([]*v1.VenueExposureCapRow, 0, len(caps))
	for _, c := range caps {
		row := &v1.VenueExposureCapRow{
			VenueKey: c.VenueKey, CapUsd: c.CapUSD, Enabled: c.Enabled, Note: c.Note,
			UpdatedAt:    timestamppb.New(c.UpdatedAt),
			AllocatedUsd: allocated[c.VenueKey],
			OverCap:      c.Enabled && c.CapUSD > 0 && allocated[c.VenueKey] > c.CapUSD,
		}
		if p, ok := s.legPool(ctx, snaps, c.VenueKey); ok {
			avail := p.equity
			if p.free != nil {
				avail = *p.free
			}
			row.LiveAvailableUsd = avail
			row.LiveEquityUsd = p.equity
			row.LiveSource = poolSource(p)
			row.LiveUpdatedAt = timestamppb.New(p.at)
		}
		out = append(out, row)
	}
	for _, key := range s.unsetCapKeys(ctx, caps) {
		out = append(out, &v1.VenueExposureCapRow{
			VenueKey: key, Unset: true, Note: "未设闸（approve 放行+warn；建议显式设上限）",
		})
	}
	return connect.NewResponse(&v1.ListVenueExposureCapsResponse{Rows: out}), nil
}

// unsetCapKeys 已配置账户面但闸表无行的 venue_key 集（CEX=exchange_configs live 行
// venue:venue_type；MT=broker_accounts user:mt4|mt5——与 approve 腿 venue_key 同键域）。
func (s *Service) unsetCapKeys(ctx context.Context, caps map[string]pgstore.VenueExposureCap) []string {
	want := map[string]bool{}
	if cfgs, err := s.gradDB().ListExchangeConfigs(ctx); err == nil {
		for _, c := range cfgs {
			if c.Simulated {
				continue
			}
			want[c.Venue+":"+c.VenueType] = true
		}
	}
	if accts, err := s.gradDB().ListBrokerAccounts(ctx); err == nil {
		for _, a := range accts {
			want[a.Venue+":"+brokerVenueType(a.Platform)] = true
		}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	return unsetCapKeysOf(keys, caps)
}

// unsetCapKeysOf 账户键集 − 闸表键集 = 未设闸键序（纯函数，D-542 A4 锚测点）。
func unsetCapKeysOf(want []string, caps map[string]pgstore.VenueExposureCap) []string {
	out := make([]string, 0, len(want))
	seen := map[string]bool{}
	for _, k := range want {
		if seen[k] {
			continue
		}
		seen[k] = true
		if _, ok := caps[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// brokerVenueType platform int → venue_type 段（0=mt4 1=mt5；其他如实回传不猜）。
func brokerVenueType(platform int) string {
	if platform == 1 {
		return "mt5"
	}
	return "mt4"
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
	sum := exposureSumOf(existing)
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

// exposureSumOf venue_key→已挂敞口汇总（双腿向 buy/sell 两键同记=保守高估）；
// NotionalUSD 优先，缺→MaxGrossUSD——与 approve 闸消费口径同公式（D-547 S3① 闸表透出复用）。
func exposureSumOf(existing []pgstore.XAssetAllowRow) map[string]float64 {
	sum := map[string]float64{}
	for _, r := range existing {
		exp := r.MaxGrossUSD
		if r.NotionalUSD != nil {
			exp = *r.NotionalUSD
		}
		sum[r.VenueBuy] += exp
		sum[r.VenueSell] += exp
	}
	return sum
}

// ghostCapKeys 闸表键 − 账户面键 = 幽灵键序（D-547 T2：caps 键域须 ⊆ 账户面键域——
// 不在 exchange_configs/broker_accounts 键域的闸行保护不了任何真腿）。纯函数锚点。
func ghostCapKeys(caps map[string]pgstore.VenueExposureCap, accountKeys []string) []string {
	want := map[string]bool{}
	for _, k := range accountKeys {
		want[k] = true
	}
	var out []string
	for k := range caps {
		if !want[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// poolSource 池来源透出标签（live 账本/REST 校准/快照兜底）。
func poolSource(p allocPool) string {
	if p.src != "" {
		return p.src
	}
	return "snap"
}
