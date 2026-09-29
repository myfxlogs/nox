// service_carry_graduation_helpers.go D-403 S3：毕业链 approve/list 的解析与报告 helpers
// （自 service_carry_graduation.go 拆出，check-lines 行数治理——软警告 >300 零警告口径）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"
	"arb/internal/scan"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// gradGapsPrefix venue gap 快照在 decided_note 的前缀（decided 行回读快照用——SSOT S3
// 「decided_note 或新列均可，选简单者」裁定：不加列，note 前缀标记）。
const gradGapsPrefix = "[gaps:"

func gradNoteWithGaps(note string, gaps []string) string {
	if len(gaps) == 0 {
		return note
	}
	return gradGapsPrefix + strings.Join(gaps, "|") + "] " + note
}

func gradParseGaps(decidedNote string) (gaps []string, userNote string) {
	if strings.HasPrefix(decidedNote, gradGapsPrefix) {
		if end := strings.Index(decidedNote, "] "); end > 0 {
			return strings.Split(decidedNote[len(gradGapsPrefix):end], "|"), strings.TrimSpace(decidedNote[end+2:])
		}
	}
	return nil, decidedNote
}

// isCfdStructure cfd 结构判定（与 scan.cfdStructures 同族；本地副本防 scan 私有依赖）。
func isCfdStructure(structure string) bool {
	return structure == "long_cfd_short_perp" || structure == "short_cfd_long_perp"
}

// symbolMapEntries store 映射行 → resolver 投影（approve 与 hint 适配同形转换）。
func symbolMapEntries(mappings []pgstore.SymbolMapping) []scan.SymbolMapEntry {
	out := make([]scan.SymbolMapEntry, 0, len(mappings))
	for _, m := range mappings {
		out = append(out, scan.SymbolMapEntry{Canonical: m.Canonical, IsPrimary: m.IsPrimary})
	}
	return out
}

// mtPlatformFor MT 账号 platform 查询（0=mt4/1=mt5）；账号不在册=fail-closed 拒绝授权。
func (s *Service) mtPlatformFor(ctx context.Context, mtVenue string) (int, error) {
	accounts, err := s.gradDB().ListBrokerAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("broker_accounts 读取失败: %w", err)
	}
	for _, a := range accounts {
		if a.Venue == mtVenue {
			return a.Platform, nil
		}
	}
	return 0, fmt.Errorf("MT 账号 %s 不在 broker_accounts（已删户？）", mtVenue)
}

// graduationVenueGaps 建议 venue 缺口（venue_key 全形口径）：cfd=组合两腿 key；
// 非 cfd=venue ×分段按结构映 key（D-492 R2：spot_carry 双腿 :spot+:perp；
// perp_perp 双腿 :perp；数字 seg=MT 账号补探 :mt5）。pending 即时算与 approve
// 同函数复用（当前态信息，不落库）。
func (s *Service) graduationVenueGaps(ctx context.Context, g *pgstore.CarryGraduation) []string {
	if !isCfdStructure(g.Structure) {
		return nonCfdGapKeys(g.Structure, g.VenueA, g.VenueB, s.allowVenuesNow(ctx))
	}
	if g.MtVenue == nil || *g.MtVenue == "" {
		return []string{"mt_venue 缺失（数据不一致）"}
	}
	mtVenue := *g.MtVenue
	platform, err := s.mtPlatformFor(ctx, mtVenue)
	if err != nil {
		return []string{err.Error()}
	}
	mappings, err := s.gradDB().ListSymbolMappings(ctx, mtVenue)
	if err != nil {
		return []string{"broker_symbol_map 读取失败: " + err.Error()}
	}
	pair, err := scan.ResolveGraduationCanonicals(g.Symbol, mtVenue, platform, symbolMapEntries(mappings))
	if err != nil {
		return []string{"symbol 映射缺失: " + err.Error()}
	}
	cexVenueKey := keydomain.Join(g.VenueB, "perp") // D-498：CEX 臂直读 venue_b
	return venueGapsOf(pair.MtVenueKey, cexVenueKey, s.allowVenuesNow(ctx))
}

// venueGapsOf 两腿 venue_key 对白名单集的缺口（顺序稳定：mt 腿在前；空键跳过）。
func venueGapsOf(mtVenueKey, cexVenueKey string, allow map[string]bool) []string {
	var gaps []string
	for _, vk := range []string{mtVenueKey, cexVenueKey} {
		if vk != "" && !allow[vk] {
			gaps = append(gaps, vk)
		}
	}
	return gaps
}

// numericVenueRe D-492 R2：venue 数字段=MT 账号主名（seg:mt5 键补探）。
var numericVenueRe = regexp.MustCompile(`^[0-9]+$`)

// nonCfdGapKeys 非 cfd 结构的 venue 缺口（venue_key 口径——前端补开钮直接可用）：
// 数字 seg=MT 账号段 → seg:mt5；spot_carry=同所双腿 → seg:spot+seg:perp；
// 其余（perp_perp）双腿恒 :perp。
func nonCfdGapKeys(structure, venueA, venueB string, allow map[string]bool) []string {
	var gaps []string
	for _, seg := range []string{venueA, venueB} { // D-498：直读配对双列不再拆组合串
		if seg == "" {
			continue
		}
		switch {
		case numericVenueRe.MatchString(seg):
			gaps = append(gaps, venueGapsOf(keydomain.Join(seg, "mt5"), "", allow)...)
		case structure == "spot_carry":
			gaps = append(gaps, venueGapsOf(keydomain.Join(seg, "spot"), keydomain.Join(seg, "perp"), allow)...)
		default:
			gaps = append(gaps, venueGapsOf(keydomain.Join(seg, "perp"), "", allow)...)
		}
	}
	return gaps
}

// nonCfdLinkReport 非 cfd 结构 approve 的链路体检报告文本（不写白名单）。
func nonCfdLinkReport(structure string, gaps []string) string {
	if len(gaps) == 0 {
		return fmt.Sprintf("链路体检：该结构（%s）无组合白名单闸；venue 白名单齐，detector/执行路径已可达（perp_perp/spot_carry 的 detector 覆盖差异=Phase B 另议）。", structure)
	}
	return fmt.Sprintf("链路体检：该结构（%s）无组合白名单闸；venue 白名单缺口：%s（需业主 autotrade_allow_rules 补行（≤60s 热生效）后链路可达，D-443）。", structure, strings.Join(gaps, ", "))
}

// autoMapBrokerSummary AutoMap 兜底（默认真身=s.AutoMapBroker RPC；autoMapFn 测试替身 seam）。
func (s *Service) autoMapBrokerSummary(ctx context.Context, venue string) (string, error) {
	if s.autoMapFn != nil {
		return s.autoMapFn(ctx, venue)
	}
	resp, err := s.AutoMapBroker(ctx, connect.NewRequest(&v1.AutoMapBrokerRequest{Venue: venue}))
	if err != nil {
		return "", err
	}
	conflicts := 0
	for _, r := range resp.Msg.GetResults() {
		if r.GetStatus() == "conflict" {
			conflicts++
		}
	}
	return fmt.Sprintf("applied=%d skipped=%d conflicts=%d", resp.Msg.GetApplied(), resp.Msg.GetSkipped(), conflicts), nil
}

// fillPendingExtras D-471/D-475a：pending 行即时算区块——venue 缺口+质量杆观测+
// 风险档案+入场窗口（行数 ≤O(百) 可承受；B-015 增长后须按 (venue,symbol) 缓存
// 窗口结果一轮——事件驱动非轮询）。decided 行回读 decided_note 快照不重算。
func (s *Service) fillPendingExtras(ctx context.Context, g *pgstore.CarryGraduation, row *v1.CarryGraduationRow) {
	row.VenueGaps = s.graduationVenueGaps(ctx, g)
	s.graduationObservability(ctx, g, row) // D-475a S5：质量杆观测即时算
	if paper, err := s.gradDB().GetPaperSpotCarryByID(ctx, g.PaperID); err == nil {
		row.Risk = s.gradDossier(ctx, g, paper)
		row.EntryWindow = s.entryWindowOf(ctx, g, paper)
	} else {
		row.EntryWindow = "closed:paper 行读取失败"
	}
}

// refreshXAssetSnapshots D-403 R2：approve 三链扇出的 pipeline/手动链快照刷新——
// rows 单查一次→exec.XAssetSpecsFromRows 单源映射（D-367 剔除规则随源）→两处 atomic Store；
// engine 链由 xassetReloader 自 reload（幂等），本函数只管另外两链。
func (s *Service) refreshXAssetSnapshots(ctx context.Context) error {
	if s.xassetEnv == "" {
		return errors.New("xasset environment 未接线（SetXAssetAllowEnvironment 缺装配）")
	}
	rows, err := s.gradDB().LoadXAssetAllowCombos(ctx, s.xassetEnv)
	if err != nil {
		return fmt.Errorf("load xasset rows: %w", err)
	}
	specs, dropped := exec.XAssetSpecsFromRows(rows)
	if s.pipeline != nil {
		s.pipeline.ReloadXAssetComboAllow(specs)
	}
	s.SetXAssetComboAllow(specs)
	log.Printf("[whitelist] xasset fan-out: rows=%d dropped=%d（pipeline+手动链快照已刷新）", len(specs), dropped)
	return nil
}

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
		capWarns, capErr := s.exposureCapGate(ctx, []string{venueSpot, venuePerp}, grossUSD, notionalUSD)
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
