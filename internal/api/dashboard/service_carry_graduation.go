// service_carry_graduation.go D-403 S3：验证毕业链 RPC——建议列表/业主授权/拒绝。
// 授权=原子完成「写 xasset 白名单(simulated=false) + engine 热重载 + AutoMap 兜底 +
// venue env 缺口报告」；非 cfd 结构无组合闸，approve=链路体检不写白名单（SSOT §1 边界）。
// venue 级白名单已表化 autotrade_allow_rules（D-443）：approve 活读报告缺口，补行仍业主 SQL 手工域。
// 解析/缺口/体检 helpers 在 service_carry_graduation_helpers.go（check-lines 行数治理）。
package dashboard

import (
	"arb/internal/data/keydomain"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/research/scan"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// XAssetReloader 白名单热重载最小接口（D-195-A 先例：dashboard→autotrade 接口注入防
// 循环依赖）。nil=未接线 → approve fail-closed 返错（重载不了=链路假通）。
type XAssetReloader interface {
	ReloadXAssetAllowCombos(context.Context) error
}

// graduationStore 存储窄接口与 gradDB 取用见 service_graduation_store.go（D-542 析出控行数）。

// SetXAssetReloader D-403 S3：热重载器接线（setup：engine 先构造，后接本 setter）。
func (s *Service) SetXAssetReloader(r XAssetReloader) { s.xassetReloader = r }

// SetAllowVenuesSource D-443 S4：venue 白名单活读 getter 注入（装配源=autotrade_allow_rules
// 表查询闭包；approve/列表每次调用取表实时态，非启动快照）。
func (s *Service) SetAllowVenuesSource(fn func(ctx context.Context) (map[string]bool, error)) {
	s.allowVenuesSource = fn
}

// allowVenuesNow venue 白名单活读（error/nil 源 → warn+空集保守=全缺口报告，fail-closed 方向）。
func (s *Service) allowVenuesNow(ctx context.Context) map[string]bool {
	if s.allowVenuesSource == nil {
		return map[string]bool{}
	}
	m, err := s.allowVenuesSource(ctx)
	if err != nil {
		log.Printf("[graduation] allow venues source: %v (空集保守=全缺口)", err)
		return map[string]bool{}
	}
	return m
}

// ListCarryGraduations 建议列表：pending 行 venue_gaps 即时算（当前态信息不落库）；
// decided 行回读 decided_note 内的缺口快照。
func (s *Service) ListCarryGraduations(ctx context.Context, req *connect.Request[v1.ListCarryGraduationsRequest]) (*connect.Response[v1.ListCarryGraduationsResponse], error) {
	rows, err := s.gradDB().ListCarryGraduations(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.CarryGraduationRow, 0, len(rows))
	for i := range rows {
		g := &rows[i]
		row := &v1.CarryGraduationRow{
			Id: g.ID, PaperId: g.PaperID, Venue: gradVenueDisplay(g), Symbol: g.Symbol,
			Structure: g.Structure, MtVenue: g.MtVenue,
			AgeDays: g.AgeDays, AccruedUsd: g.AccruedUSD, LastFundingAnn: g.LastFundingAnn,
			NetAllInUsd: g.NetAllInUSD, // D-410 S5：全账净额透出（缺账行 NULL 诚实缺）
			FlagWarn:    g.FlagWarn,    // D-463 S3：warn 旗标透出毕业卡
			Status:      g.Status,
			CreatedAt:   g.CreatedAt.Format(time.RFC3339),
			ComboId:     g.ComboID,
		}
		if g.DecidedAt != nil {
			row.DecidedAt = g.DecidedAt.Format(time.RFC3339)
		}
		gaps, userNote := gradParseGaps(g.DecidedNote)
		row.DecidedNote = userNote
		if g.Status == "pending" {
			s.fillPendingExtras(ctx, g, row) // venue_gaps+观测+档案+入场窗即时算（helpers 拆分）
		} else {
			row.VenueGaps = gaps
		}
		out = append(out, row)
	}
	return connect.NewResponse(&v1.ListCarryGraduationsResponse{Rows: out}), nil
}

// ApproveCarryGraduation 业主授权（SSOT S3 流程 ①-⑦）：pending 守卫 → canonical 双解析 →
// 按 structure 方向写 combo（simulated=false）→ engine 热重载 → AutoMap 兜底 → venue gap 报告 →
// 建议落账+info 告警。域内错误走 in-band ok=false（前端可展示），不抛 connect 错误。
func (s *Service) ApproveCarryGraduation(ctx context.Context, req *connect.Request[v1.ApproveCarryGraduationRequest]) (*connect.Response[v1.ApproveCarryGraduationResponse], error) {
	resp := &v1.ApproveCarryGraduationResponse{}
	g := req.Msg
	fail := func(format string, args ...any) (*connect.Response[v1.ApproveCarryGraduationResponse], error) {
		resp.Ok = false
		resp.Error = fmt.Sprintf(format, args...)
		return connect.NewResponse(resp), nil
	}
	// ① D-490 模式分流校验：资金分配（pct/usd 任一）与显式四字段互斥；显式模式走原校验（语义在 allocModeInputs）。
	funding, modeErr := allocModeInputs(g)
	if modeErr != "" {
		return fail("%s", modeErr)
	}
	db := s.gradDB()
	grad, err := db.GetCarryGraduation(ctx, g.GraduationId)
	if err != nil {
		return fail("建议 #%d 不存在: %v", g.GraduationId, err)
	}
	if grad.Status != "pending" {
		return fail("建议 #%d status=%s 非 pending（已裁决不可重复授权）", grad.ID, grad.Status)
	}
	paper, err := db.GetPaperSpotCarryByID(ctx, grad.PaperID)
	if err != nil {
		return fail("纸面行 #%d 读取失败: %v", grad.PaperID, err)
	}
	userNote := strings.TrimSpace(g.Note)

	// D-471 S2 全结构硬前置（B-013）：approve 前重读 paper 当前态，GraduationQualifiedReason
	// 单判据源现算——证据衰减行 in-band 拒批（冻结值不作数）；非 cfd=复检+只记账不写白名单。
	if ok, reason := scan.GraduationQualifiedReason(*paper, time.Now()); !ok {
		return fail("证据已衰减：%s（冻结值不作数，建议行仍留 pending）", reason)
	}

	// D-475a S3 flag 复核（分叉前双盖 cfd/非 cfd）：候选 venue 集=纸面 venue 组合串+
	// '×'拆段+mtVenue（scan.FlagVenueSet 单口径——禁复制第二判据）。blocked→拒批；
	// warn→放行但 decided_note 追加 flag_warn 前缀（业主知情）。
	flags, err := db.ListActiveFlags(ctx)
	if err != nil {
		return fail("旗标活读失败: %v（fail-closed 不批钱）", err)
	}
	for _, v := range scan.FlagVenueSet(grad.VenueA, grad.VenueB, grad.MtVenue) {
		if f, ok := flags[pgstore.FlagKey(v, grad.Symbol)]; ok {
			if f.Level == "blocked" {
				return fail("venue %s 命中 blocked flag: %s（先解旗再批准）", v, f.Reason)
			}
			if f.Level == "warn" {
				userNote = "flag_warn:" + f.Reason + " " + userNote
			}
		}
		if f, ok := flags[pgstore.FlagKey(v, "*")]; ok && f.Level == "blocked" {
			return fail("venue %s 命中 blocked 通配旗: %s（先解旗再批准）", v, f.Reason)
		}
	}

	if !isCfdStructure(grad.Structure) {
		// 非 cfd 分支拆 helpers（D-488 起 spot_carry 也有组合闸——本文件行数治理）。
		return s.approveNonCfd(ctx, resp, db, g, grad, paper, userNote)
	}

	// cfd 结构：mt_venue 必在（paper_cfd_carry 开仓即落列）
	if grad.MtVenue == nil || *grad.MtVenue == "" {
		return fail("cfd 建议 #%d mt_venue 缺失（数据不一致，拒绝授权）", grad.ID)
	}
	mtVenue := *grad.MtVenue
	// ② canonical 双解析（单一 resolver：scan.ResolveGraduationCanonicals，与 detector hint 同函数）
	platform, err := s.mtPlatformFor(ctx, mtVenue)
	if err != nil {
		return fail("%v", err)
	}
	// D-474b S4：platform!=1 → MT4 已退役（D-470），不产 canonical、不写实盘白名单；
	// 建议行留 pending 自然过期（S3 stale）——平台退役不写实盘授权。
	if platform != 1 {
		return fail("venue %s platform=%d 拒绝写白名单（MT4 已退役，D-470）", mtVenue, platform)
	}
	mappings, err := db.ListSymbolMappings(ctx, mtVenue)
	if err != nil {
		return fail("broker_symbol_map 读取失败: %v", err)
	}
	// cex 腿 venue=配对 B 臂（D-498 直读 venue_b，不再拆组合串）
	cexVenue := paper.VenueB
	if cexVenue == "" {
		return fail("paper venue 配对 B 臂缺失: %q", paper.VenuePair())
	}
	equiv, eqErr := db.ListAssetEquiv(ctx)
	if eqErr != nil { // D-043：equiv 读失败降级 legacy 解析（class 键行 resolver 自会 fail-closed）
		log.Printf("[graduation] asset_equiv read: %v（降级 legacy 解析）", eqErr)
	}
	pair, err := scan.ResolveGraduationCanonicals(grad.Symbol, mtVenue, cexVenue, platform, symbolMapEntries(mappings), equiv)
	if err != nil {
		return fail("%v（需先在 Symbol 映射页补映射或点「自动映射全部账号」）", err)
	}
	cexVenueKey := keydomain.Join(cexVenue, "perp")
	// ③ 按 structure 方向定买卖腿（canonical_buy/venue_buy 永远=买腿，cap 跟腿走；D-490 折算先定方向）。
	canonBuy, canonSell := pair.MtCanonical, pair.CexCanonical
	venueBuy, venueSell := pair.MtVenueKey, cexVenueKey
	if grad.Structure == "short_cfd_long_perp" { // buy=CEX sell=MT（三轮自审计方向反转修）
		canonBuy, canonSell = pair.CexCanonical, pair.MtCanonical
		venueBuy, venueSell = cexVenueKey, pair.MtVenueKey
	} else if grad.Structure != "long_cfd_short_perp" {
		return fail("未知 cfd structure %q", grad.Structure)
	}
	// D-490 S3：资金分配模式→服务端折算四字段（快照固化绝对值）；显式模式=请求原值。
	longQty, shortQty, grossUSD := g.MaxLongQty, g.MaxShortQty, g.MaxGrossUsd
	notionalUSD := g.NotionalUsd
	if funding {
		var estr string
		longQty, shortQty, grossUSD, notionalUSD, userNote, estr = s.allocApply(ctx, resp, venueBuy, venueSell, canonBuy, canonSell, g, userNote)
		if estr != "" {
			return fail("%s", estr)
		}
	}
	// D-471 S2：复核闸已上移至 isCfdStructure 分叉前（全结构硬前置，B-013）——
	// cfd 分支不再重复判（同判据源 scan.GraduationQualifiedReason）。
	// D-475a S3 敞口闸（G5）：三态语义封装 service_venue_exposure.go exposureCapGate。
	capWarns, capErr := s.exposureCapGate(ctx, []exposureLeg{
		{VenueKey: venueBuy, Canonical: canonBuy},
		{VenueKey: venueSell, Canonical: canonSell, SellLeg: true},
	}, grossUSD, notionalUSD)
	if capErr != "" {
		return fail("%s", capErr)
	}
	// D-490 dry_run：试算回显零副作用（不写库/不落账/不重载），也不依赖 reloader 接线（前端试算逐键触发）。
	if g.DryRun {
		s.gradDryRunResp(ctx, resp, venueBuy, venueSell, capWarns, funding, longQty, shortQty, grossUSD, notionalUSD)
		return connect.NewResponse(resp), nil
	}
	// fail-closed 前置：重载器未接线=写表也假通（approve 后引擎仍旧名单）
	if s.xassetReloader == nil {
		return fail("重载器未接线（SetXAssetReloader 缺装配），fail-closed 拒绝授权")
	}
	combo := &pgstore.XAssetComboInsert{
		CanonicalBuy: canonBuy, CanonicalSell: canonSell,
		VenueBuy: venueBuy, VenueSell: venueSell,
		MaxLongQty:  longQty,  // 买腿上限（MT 腿=手数、CEX 腿=张数，跟 venue_buy 走）
		MaxShortQty: shortQty, // 卖腿上限
		MaxGrossUSD: grossUSD,
		NotionalUSD: notionalUSD,
		Simulated:   false, // 真仓白名单；venue env 门仍是实际真钱闸（双层不矛盾）
		Note:        "D-403毕业 paper#" + strconv.FormatInt(paper.ID, 10),
	}
	comboID, err := db.InsertXAssetAllowCombo(ctx, combo)
	if err != nil {
		if errors.Is(err, pgstore.ErrXAssetComboExists) {
			return fail("已在白名单（唯一键撞 canonical×venue 对，无需重复授权）")
		}
		return fail("写白名单失败: %v", err)
	}
	resp.ComboId = &comboID
	// ④ 热重载三链扇出（R2）：engine 经既有 reloader（幂等自载）+ pipeline/自快照
	// rows 单查一次→共享映射→两处 atomic Store。任一失败=该链持旧名单直至重启，如实报错不落账。
	if err := s.xassetReloader.ReloadXAssetAllowCombos(ctx); err != nil {
		return fail("白名单已写入但引擎热重载失败（重启后生效）: %v", err)
	}
	if err := s.refreshXAssetSnapshots(ctx); err != nil {
		return fail("白名单已写入但 pipeline/手动链快照刷新失败（重启后生效）: %v", err)
	}
	// ⑤ AutoMap 兜底（幂等+conflict 不自动应用；多为幂等空跑）
	summary, automapErr := s.autoMapBrokerSummary(ctx, mtVenue)
	if automapErr != nil {
		summary = "automap 失败: " + automapErr.Error()
	}
	resp.AutomapSummary = summary
	// ⑥ venue 缺口报告（两腿 venue_key 对 autotrade_allow_rules 表活读，D-443）
	gaps := venueGapsOf(venueBuy, venueSell, s.allowVenuesNow(ctx))
	resp.VenueGapKeys = gaps                   // D-492：纯 key 列表供前端补开钮
	resp.VenueGaps = append(gaps, capWarns...) // D-475a S3：敞口闸未设 venue 如实透出
	// ⑦ 落账 approved+combo_id+note（含 gap 快照前缀）+info 告警
	if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "approved", &comboID, gradNoteWithGaps(userNote, gaps)); err != nil {
		return fail("落账失败: %v", err)
	}
	s.insertGraduationAlert(ctx, grad, "已授权", fmt.Sprintf("combo#%d %s|%s %s×%s", comboID, combo.CanonicalBuy, combo.CanonicalSell, combo.VenueBuy, combo.VenueSell))
	resp.Ok = true
	return connect.NewResponse(resp), nil
}
