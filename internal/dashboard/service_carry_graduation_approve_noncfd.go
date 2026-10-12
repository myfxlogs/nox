// service_carry_graduation_approve_noncfd.go — approveNonCfd 分支（D-043 复审自 helpers 拆出，300 行门禁）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// approveNonCfd 非 cfd 结构授权分支（D-488 自 service_carry_graduation.go 拆出）：
// spot_carry=组合写真金白名单（业主语义「授权即放行」；D-473 起 FUNDING_HEDGE 执行链全通，
// 旧版只体检不写=假授权）；perp_perp 无组合闸=链路体检不写白名单（不越权）。
func (s *Service) approveNonCfd(ctx context.Context, resp *v1.ApproveCarryGraduationResponse,
	db graduationStore, g *v1.ApproveCarryGraduationRequest,
	grad *pgstore.CarryGraduation, paper *pgstore.PaperSpotCarry, userNote string,
) (*connect.Response[v1.ApproveCarryGraduationResponse], error) {
	fail := func(format string, args ...any) (*connect.Response[v1.ApproveCarryGraduationResponse], error) {
		resp.Ok = false
		resp.Error = fmt.Sprintf(format, args...)
		return connect.NewResponse(resp), nil
	}
	funding := g.FundingPct != nil || g.FundingUsd != nil
	if grad.Structure == "spot_carry" || grad.Structure == "long_spot_short_perp" {
		// D-488：spot_carry 授权=组合进真金白名单（业主语义「授权即放行」；D-473 起
		// FUNDING_HEDGE 执行链全通——旧版只体检不写白名单=假授权缺陷修复）。
		// 方向定死=现货买×合约空（正 funding 收息形态）；canonical 双腿同值
		//（engine FUNDING_HEDGE 闸 sellCanonical 回退 canonical 的匹配口径）。
		venueSpot, venuePerp := keydomain.Join(grad.VenueA, "spot"), keydomain.Join(grad.VenueA, "perp")
		if grad.Structure == "long_spot_short_perp" {
			var estr string // D-555 S4：双CEX 腿键取行数据合成（禁 canonical 反推）
			if venueSpot, venuePerp, estr = cexCexLegKeys(grad, paper); estr != "" {
				return fail("%s", estr)
			}
		}
		// D-490：同所双池（现货池现金×永续池保证金）资金分配折算——与 cfd 分支同模型。
		longQty, shortQty, grossUSD := g.MaxLongQty, g.MaxShortQty, g.MaxGrossUsd
		notionalUSD := g.NotionalUsd
		if funding {
			var estr string
			longQty, shortQty, grossUSD, notionalUSD, userNote, estr = s.allocApply(ctx, resp, venueSpot, venuePerp, grad.Symbol, grad.Symbol, g, userNote)
			if estr != "" {
				return fail("%s", estr)
			}
		}
		capLegs := []exposureLeg{{VenueKey: venueSpot, Canonical: grad.Symbol}, {VenueKey: venuePerp, Canonical: grad.Symbol, SellLeg: true}}
		capWarns, capErr := s.exposureCapGate(ctx, capLegs, grossUSD, notionalUSD)
		if capErr != "" {
			return fail("%s", capErr)
		}
		// D-490 dry_run：试算回显零副作用（不写库/不落账/不重载），不依赖 reloader 接线（前端试算逐键触发）。
		if g.DryRun {
			s.gradDryRunResp(ctx, resp, venueSpot, venuePerp, capWarns, funding, longQty, shortQty, grossUSD, notionalUSD)
			return connect.NewResponse(resp), nil
		}
		if s.xassetReloader == nil {
			return fail("重载器未接线（SetXAssetReloader 缺装配），fail-closed 拒绝授权")
		}
		combo := &pgstore.XAssetComboInsert{
			CanonicalBuy: grad.Symbol, CanonicalSell: grad.Symbol,
			VenueBuy: venueSpot, VenueSell: venuePerp,
			MaxLongQty: longQty, MaxShortQty: shortQty,
			MaxGrossUSD: grossUSD, NotionalUSD: notionalUSD,
			Simulated: false,
			Note:      "D-403毕业 paper#" + strconv.FormatInt(paper.ID, 10),
		}
		comboID, err := db.InsertXAssetAllowCombo(ctx, combo)
		if err != nil {
			if errors.Is(err, pgstore.ErrXAssetComboExists) {
				return fail("已在白名单（唯一键撞 canonical×venue 对，无需重复授权）")
			}
			return fail("写白名单失败: %v", err)
		}
		resp.ComboId = &comboID
		if err := s.xassetReloader.ReloadXAssetAllowCombos(ctx); err != nil {
			return fail("白名单已写入但引擎热重载失败（重启后生效）: %v", err)
		}
		if err := s.refreshXAssetSnapshots(ctx); err != nil {
			return fail("白名单已写入但 pipeline/手动链快照刷新失败（重启后生效）: %v", err)
		}
		resp.VenueGapKeys = venueGapsOf(venueSpot, venuePerp, s.allowVenuesNow(ctx)) // D-492：纯 key 列表
		gaps := append(resp.VenueGapKeys, capWarns...)
		resp.VenueGaps = gaps
		if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "approved", &comboID, gradNoteWithGaps(userNote, gaps)); err != nil {
			return fail("落账失败: %v", err)
		}
		s.insertGraduationAlert(ctx, grad, "已授权", fmt.Sprintf("combo#%d %s %s×%s", comboID, grad.Symbol, venueSpot, venuePerp))
		resp.Ok = true
		return connect.NewResponse(resp), nil
	}
	// D-555 S4：结构白名单闸——非已知非 cfd 结构 fail-closed（PERP_FUNDING 等未开通
	// 结构不得经体检放行径落 approved）。
	if grad.Structure != "perp_perp" {
		return fail("structure=%q 非可批结构（fail-closed 拒授）", grad.Structure)
	}
	// perp_perp：无组合白名单闸（PERP_FUNDING 走 venue/canonical 规则域），approve=链路体检不写白名单（不越权）。
	// D-490：该结构无 sizing 写库面——资金分配输入不适用（显式上限仍按旧校验走）。
	if funding {
		return fail("perp_perp 无组合闸，资金分配模式不适用——请用显式上限（qty/名义直填）")
	}
	gaps := s.graduationVenueGaps(ctx, grad)
	resp.VenueGaps = gaps
	resp.LinkReport = nonCfdLinkReport(grad.Structure, gaps)
	resp.VenueGapKeys = gaps // D-492 R2：graduationVenueGaps 已 venue_key 全形——直接透出供补开钮
	// D-490 dry_run：体检回显不落账不发告警（零状态变更）。
	if g.DryRun {
		fillExplicitEst(resp, g.MaxLongQty, g.MaxShortQty, g.MaxGrossUsd, g.NotionalUsd)
		resp.Ok = true
		return connect.NewResponse(resp), nil
	}
	if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "approved", nil, gradNoteWithGaps(userNote, gaps)); err != nil {
		return fail("落账失败: %v", err)
	}
	s.insertGraduationAlert(ctx, grad, "已授权", "")
	resp.Ok = true
	return connect.NewResponse(resp), nil
}
