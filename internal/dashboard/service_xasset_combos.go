// service_xasset_combos.go D-471 S3：xasset_allow_combos 管理面 RPC（白名单不再裸 SQL）。
// List=全量行（含禁用）；Upsert=insert（id=0）/update（id>0，事务+行锁 B-016）；
// 写后必走 xassetReloader+refreshXAssetSnapshots（reloader==nil→fail-closed 拒写）。
// 在持仓知情：禁用/caps 下调命中 open|legged|closing 持仓→needs_confirm（confirm=true 放行）；
// caps 下调至 <在持名义→拒改（B-020 防业主把自己锁死）。禁用只闸入场不伤在持仓退出评估
// （pipeline_stages.go combo 检查仅在建 position 前，平仓不查 allowlist——钉死语义）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

var comboVenueSuffixes = map[string]bool{":mt4": true, ":mt5": true, ":perp": true, ":spot": true}

// comboVenueOK venue_key 后缀校验（fail-closed 枚举：B-016.4 非法后缀拒写）。
func comboVenueOK(v string) bool {
	for suf := range comboVenueSuffixes {
		if strings.HasSuffix(v, suf) {
			return true
		}
	}
	return false
}

func (s *Service) ListXAssetAllowCombos(ctx context.Context, _ *connect.Request[v1.ListXAssetAllowCombosRequest]) (*connect.Response[v1.ListXAssetAllowCombosResponse], error) {
	rows, err := s.gradDB().ListXAssetAllowCombosAll(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.XAssetAllowComboRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &v1.XAssetAllowComboRow{
			Id: r.ID, CanonicalBuy: r.CanonicalBuy, VenueBuy: r.VenueBuy,
			CanonicalSell: r.CanonicalSell, VenueSell: r.VenueSell,
			MaxLongQty: r.MaxLongQty, MaxShortQty: r.MaxShortQty, MaxGrossUsd: r.MaxGrossUSD,
			NotionalUsd: r.NotionalUSD, Enabled: r.Enabled, Simulated: r.Simulated, Note: r.Note,
		})
	}
	return connect.NewResponse(&v1.ListXAssetAllowCombosResponse{Rows: out}), nil
}

// UpsertXAssetAllowCombo insert/update + 热重载 + 在持仓知情（B-016/B-020）。
func (s *Service) UpsertXAssetAllowCombo(ctx context.Context, req *connect.Request[v1.UpsertXAssetAllowComboRequest]) (*connect.Response[v1.UpsertXAssetAllowComboResponse], error) {
	m := req.Msg
	resp := &v1.UpsertXAssetAllowComboResponse{}
	fail := func(format string, args ...any) (*connect.Response[v1.UpsertXAssetAllowComboResponse], error) {
		resp.Error = fmt.Sprintf(format, args...)
		return connect.NewResponse(resp), nil
	}
	// fail-closed 前置：重载器未接线=写表假通（同 approve 先例）
	if s.xassetReloader == nil {
		return fail("重载器未接线（SetXAssetReloader 缺装配），fail-closed 拒绝写白名单")
	}
	if m.CanonicalBuy == "" || m.CanonicalSell == "" || m.VenueBuy == "" || m.VenueSell == "" {
		return fail("canonical/venue 四元组必填")
	}
	if !comboVenueOK(m.VenueBuy) || !comboVenueOK(m.VenueSell) {
		return fail("venue 后缀非法（只允许 :mt4/:mt5/:perp/:spot）：%s / %s", m.VenueBuy, m.VenueSell)
	}
	if m.MaxLongQty <= 0 || m.MaxShortQty <= 0 || m.MaxGrossUsd <= 0 {
		return fail("三 cap 必填且 >0")
	}
	if m.NotionalUsd != nil && *m.NotionalUsd <= 0 {
		return fail("notional_usd 显式传值禁非正（D-367 加载期剔除=假授权）")
	}
	db := s.gradDB()
	if m.Id == 0 {
		// insert 分支（同 approve 写路径）
		id, err := db.InsertXAssetAllowCombo(ctx, &pgstore.XAssetComboInsert{
			CanonicalBuy: m.CanonicalBuy, CanonicalSell: m.CanonicalSell,
			VenueBuy: m.VenueBuy, VenueSell: m.VenueSell,
			MaxLongQty: m.MaxLongQty, MaxShortQty: m.MaxShortQty, MaxGrossUSD: m.MaxGrossUsd,
			NotionalUSD: m.NotionalUsd, Simulated: m.Simulated != nil && *m.Simulated, Note: m.Note,
		})
		if err != nil {
			if errors.Is(err, pgstore.ErrXAssetComboExists) {
				return fail("已在白名单（唯一键撞 canonical×venue 对）")
			}
			return fail("写白名单失败: %v", err)
		}
		resp.Ok = true
		_ = id
		return s.comboWriteFinish(ctx, resp)
	}
	// update 分支：行锁（store 内 tx）；先查在持仓知情。
	posIDs, openNotional, err := db.ListOpenPositionsForCombo(ctx, m.CanonicalBuy, m.CanonicalSell, m.VenueBuy, m.VenueSell)
	if err != nil {
		return fail("在持仓查询失败: %v（fail-closed 不改）", err)
	}
	if len(posIDs) > 0 {
		// B-020：caps 下调至 <在持名义→拒改（不论 confirm——锁死防线）。
		if m.MaxGrossUsd < openNotional || (m.NotionalUsd != nil && *m.NotionalUsd < openNotional) {
			return fail("在持 pos%v 名义 $%.2f 占用，新 caps 低于占用额将永远无法再开仓——需先平仓或设更大 caps", posIDs, openNotional)
		}
		if !m.Confirm {
			resp.NeedsConfirm = true
			resp.OpenPositionIds = posIDs
			resp.Error = fmt.Sprintf("该组合有在持仓位 %v，禁用/下调将停监控簿派生与保底哨兵（入场闸不动退出）——confirm=true 重发确认执行", posIDs)
			return connect.NewResponse(resp), nil
		}
	}
	ok, err := db.UpdateXAssetAllowCombo(ctx, &pgstore.XAssetComboAdminRow{
		ID: m.Id, MaxLongQty: m.MaxLongQty, MaxShortQty: m.MaxShortQty, MaxGrossUSD: m.MaxGrossUsd,
		NotionalUSD: m.NotionalUsd, Enabled: m.Enabled, Note: m.Note,
	})
	if err != nil {
		return fail("行锁更新失败: %v", err)
	}
	if !ok {
		return fail("combo #%d 不存在", m.Id)
	}
	resp.Ok = true
	return s.comboWriteFinish(ctx, resp)
}

// comboWriteFinish 写后热重载三链（同 approve ⑤；任一失败如实报错）。
func (s *Service) comboWriteFinish(ctx context.Context, resp *v1.UpsertXAssetAllowComboResponse) (*connect.Response[v1.UpsertXAssetAllowComboResponse], error) {
	s.TierKickAll(ctx) // D-478 S4：组合写→档位重算（恶化转移告警）
	if err := s.xassetReloader.ReloadXAssetAllowCombos(ctx); err != nil {
		resp.Ok = false
		resp.Error = "白名单已写入但引擎热重载失败（重启后生效）: " + err.Error()
		return connect.NewResponse(resp), nil
	}
	if err := s.refreshXAssetSnapshots(ctx); err != nil {
		resp.Ok = false
		resp.Error = "白名单已写入但 pipeline/手动链快照刷新失败（重启后生效）: " + err.Error()
		return connect.NewResponse(resp), nil
	}
	return connect.NewResponse(resp), nil
}

// PreviewXAssetComboRisk S3：加行前风险预览（S1 同款档案；入参 fail-closed 校验 B-016.4）。
func (s *Service) PreviewXAssetComboRisk(ctx context.Context, req *connect.Request[v1.PreviewXAssetComboRiskRequest]) (*connect.Response[v1.PreviewXAssetComboRiskResponse], error) {
	m := req.Msg
	resp := &v1.PreviewXAssetComboRiskResponse{}
	fail := func(format string, args ...any) (*connect.Response[v1.PreviewXAssetComboRiskResponse], error) {
		resp.Error = fmt.Sprintf(format, args...)
		return connect.NewResponse(resp), nil
	}
	if m.NotionalUsd <= 0 {
		return fail("notional_usd 必填且 >0")
	}
	if m.VenueBuy == "" || m.VenueSell == "" || m.CanonicalBuy == "" {
		return fail("venue/canonical 必填")
	}
	if !comboVenueOK(m.VenueBuy) || !comboVenueOK(m.VenueSell) {
		return fail("venue 后缀非法（只允许 :mt4/:mt5/:perp/:spot）")
	}
	cs := m.CanonicalSell
	if cs == "" {
		cs = m.CanonicalBuy
	}
	legs := []riskLegSpec{
		{m.VenueBuy, m.CanonicalBuy, "buy"},
		{m.VenueSell, cs, "sell"},
	}
	resp.Risk = s.graduationRiskReport(ctx, legs, m.NotionalUsd, "combo", nil)
	resp.Ok = true
	return connect.NewResponse(resp), nil
}
