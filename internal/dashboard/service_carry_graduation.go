// service_carry_graduation.go D-403 S3：验证毕业链 RPC——建议列表/业主授权/拒绝。
// 授权=原子完成「写 xasset 白名单(simulated=false) + engine 热重载 + AutoMap 兜底 +
// venue env 缺口报告」；非 cfd 结构无组合闸，approve=链路体检不写白名单（SSOT §1 边界）。
// venue 级白名单已表化 autotrade_allow_rules（D-443）：approve 活读报告缺口，补行仍业主 SQL 手工域。
// 解析/缺口/体检 helpers 在 service_carry_graduation_helpers.go（check-lines 行数治理）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/scan"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// XAssetReloader 白名单热重载最小接口（D-195-A 先例：dashboard→autotrade 接口注入防
// 循环依赖）。nil=未接线 → approve fail-closed 返错（重载不了=链路假通）。
type XAssetReloader interface {
	ReloadXAssetAllowCombos(context.Context) error
}

// graduationStore approve/list 的存储依赖窄接口（*pgstore.Store 原生满足；测试 fake 替身）。
type graduationStore interface {
	GetCarryGraduation(ctx context.Context, id int64) (*pgstore.CarryGraduation, error)
	GetPaperSpotCarryByID(ctx context.Context, id int64) (*pgstore.PaperSpotCarry, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	ListBrokerAccounts(ctx context.Context) ([]pgstore.BrokerAccount, error)
	InsertXAssetAllowCombo(ctx context.Context, r *pgstore.XAssetComboInsert) (int64, error)
	UpdateCarryGraduationDecision(ctx context.Context, id int64, status string, comboID *int64, note string) error
	LoadXAssetAllowCombos(ctx context.Context, environment string) ([]pgstore.XAssetAllowRow, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	ListCarryGraduations(ctx context.Context) ([]pgstore.CarryGraduation, error)
}

// gradDB 存储取用：测试可注入 gradStore 替身，生产回落 s.store（单一真源）。
func (s *Service) gradDB() graduationStore {
	if s.gradStore != nil {
		return s.gradStore
	}
	return s.store
}

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
			Id: g.ID, PaperId: g.PaperID, Venue: g.Venue, Symbol: g.Symbol,
			Structure: g.Structure, MtVenue: g.MtVenue,
			AgeDays: g.AgeDays, AccruedUsd: g.AccruedUSD, LastFundingAnn: g.LastFundingAnn,
			NetAllInUsd: g.NetAllInUSD, // D-410 S5：全账净额透出（缺账行 NULL 诚实缺）
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
			row.VenueGaps = s.graduationVenueGaps(ctx, g)
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
	// ① caps 必填>0（禁默认=防盲签）；notional 显式传禁非正（D-367 加载期会剔除=假授权）
	if g.MaxLongQty <= 0 || g.MaxShortQty <= 0 || g.MaxGrossUsd <= 0 {
		return fail("三 cap 必填且 >0（max_long_qty/max_short_qty/max_gross_usd，跟 venue_buy/venue_sell 腿走）")
	}
	if g.NotionalUsd != nil && *g.NotionalUsd <= 0 {
		return fail("notional_usd 显式传值禁非正（D-367 加载期会整行剔除=假授权）")
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

	if !isCfdStructure(grad.Structure) {
		// 非 cfd（perp_perp/spot_carry）：无组合白名单闸，approve=链路体检不写白名单（不越权）。
		gaps := s.graduationVenueGaps(ctx, grad)
		resp.VenueGaps = gaps
		resp.LinkReport = nonCfdLinkReport(grad.Structure, gaps)
		if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "approved", nil, gradNoteWithGaps(userNote, gaps)); err != nil {
			return fail("落账失败: %v", err)
		}
		s.insertGraduationAlert(ctx, grad, "已授权", "")
		resp.Ok = true
		return connect.NewResponse(resp), nil
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
	mappings, err := db.ListSymbolMappings(ctx, mtVenue)
	if err != nil {
		return fail("broker_symbol_map 读取失败: %v", err)
	}
	pair, err := scan.ResolveGraduationCanonicals(grad.Symbol, mtVenue, platform, symbolMapEntries(mappings))
	if err != nil {
		return fail("%v（需先在 Symbol 映射页补映射或点「自动映射全部账号」）", err)
	}
	// cex 腿 venue 取 paper venue 组合串尾段（D-374 venue=mt×cex）
	parts := strings.Split(paper.Venue, "×")
	cexVenue := parts[len(parts)-1]
	if cexVenue == "" {
		return fail("paper venue 组合串异常: %q", paper.Venue)
	}
	cexVenueKey := cexVenue + ":perp"
	// ③ 按 structure 方向写 combo：canonical_buy/venue_buy 永远=买腿（cap 跟腿走）
	combo := &pgstore.XAssetComboInsert{
		MaxLongQty:  g.MaxLongQty,  // 买腿上限（MT 腿=手数、CEX 腿=张数，跟 venue_buy 走）
		MaxShortQty: g.MaxShortQty, // 卖腿上限
		MaxGrossUSD: g.MaxGrossUsd,
		NotionalUSD: g.NotionalUsd,
		Simulated:   false, // 真仓白名单；venue env 门仍是实际真钱闸（双层不矛盾）
		Note:        "D-403毕业 paper#" + strconv.FormatInt(paper.ID, 10),
	}
	switch grad.Structure {
	case "long_cfd_short_perp": // buy=MT sell=CEX
		combo.CanonicalBuy, combo.CanonicalSell = pair.MtCanonical, pair.CexCanonical
		combo.VenueBuy, combo.VenueSell = pair.MtVenueKey, cexVenueKey
	case "short_cfd_long_perp": // buy=CEX sell=MT（三轮自审计方向反转修）
		combo.CanonicalBuy, combo.CanonicalSell = pair.CexCanonical, pair.MtCanonical
		combo.VenueBuy, combo.VenueSell = cexVenueKey, pair.MtVenueKey
	default:
		return fail("未知 cfd structure %q", grad.Structure)
	}
	// fail-closed 前置：重载器未接线=写表也假通（approve 后引擎仍旧名单）
	if s.xassetReloader == nil {
		return fail("重载器未接线（SetXAssetReloader 缺装配），fail-closed 拒绝授权")
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
	gaps := venueGapsOf(pair.MtVenueKey, cexVenueKey, s.allowVenuesNow(ctx))
	resp.VenueGaps = gaps
	// ⑦ 落账 approved+combo_id+note（含 gap 快照前缀）+info 告警
	if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "approved", &comboID, gradNoteWithGaps(userNote, gaps)); err != nil {
		return fail("落账失败: %v", err)
	}
	s.insertGraduationAlert(ctx, grad, "已授权", fmt.Sprintf("combo#%d %s|%s %s×%s", comboID, combo.CanonicalBuy, combo.CanonicalSell, combo.VenueBuy, combo.VenueSell))
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// RejectCarryGraduation pending→rejected（不可逆；note 留业主裁决记录）。
func (s *Service) RejectCarryGraduation(ctx context.Context, req *connect.Request[v1.RejectCarryGraduationRequest]) (*connect.Response[v1.RejectCarryGraduationResponse], error) {
	resp := &v1.RejectCarryGraduationResponse{}
	g := req.Msg
	db := s.gradDB()
	grad, err := db.GetCarryGraduation(ctx, g.GraduationId)
	if err != nil {
		resp.Error = fmt.Sprintf("建议 #%d 不存在: %v", g.GraduationId, err)
		return connect.NewResponse(resp), nil
	}
	if grad.Status != "pending" {
		resp.Error = fmt.Sprintf("建议 #%d status=%s 非 pending", grad.ID, grad.Status)
		return connect.NewResponse(resp), nil
	}
	if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "rejected", nil, strings.TrimSpace(g.Note)); err != nil {
		resp.Error = fmt.Sprintf("落账失败: %v", err)
		return connect.NewResponse(resp), nil
	}
	s.insertGraduationAlert(ctx, grad, "已拒绝", "")
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// insertGraduationAlert 毕业裁决 info 告警（feishu 投递走既有 pusher 链）。
func (s *Service) insertGraduationAlert(ctx context.Context, g *pgstore.CarryGraduation, action, detail string) {
	msg := fmt.Sprintf("[毕业链] %s×%s %s %s", g.Venue, g.Symbol, g.Structure, action)
	if detail != "" {
		msg += "：" + detail
	}
	if err := s.gradDB().InsertAlert(ctx, nil, "info", msg); err != nil {
		log.Printf("[graduation] alert: %v", err)
	}
}
