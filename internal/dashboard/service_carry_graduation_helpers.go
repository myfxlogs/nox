// service_carry_graduation_helpers.go D-403 S3：毕业链 approve/list 的解析与报告 helpers
// （自 service_carry_graduation.go 拆出，check-lines 行数治理——软警告 >300 零警告口径）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/exec"
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

// graduationVenueGaps 建议 venue 缺口：cfd=组合两腿 venue_key；非 cfd=venue 段裸名。
// pending 列表即时算与 approve 同函数复用（当前态信息，不落库）。
func (s *Service) graduationVenueGaps(ctx context.Context, g *pgstore.CarryGraduation) []string {
	if !isCfdStructure(g.Structure) {
		return nonCfdVenueGaps(g.Venue, s.autoTradeAllowVenues)
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
	parts := strings.Split(g.Venue, "×")
	cexVenueKey := parts[len(parts)-1] + ":perp"
	return venueGapsOf(pair.MtVenueKey, cexVenueKey, s.autoTradeAllowVenues)
}

// venueGapsOf 两腿 venue_key 对 env 快照的缺口（顺序稳定：mt 腿在前）。
func venueGapsOf(mtVenueKey, cexVenueKey string, allow map[string]bool) []string {
	var gaps []string
	for _, vk := range []string{mtVenueKey, cexVenueKey} {
		if !allow[vk] {
			gaps = append(gaps, vk)
		}
	}
	return gaps
}

// nonCfdVenueGaps 非 cfd 结构的 venue 段缺口（裸名口径：× 分段任一不在 env 快照即报）。
func nonCfdVenueGaps(paperVenue string, allow map[string]bool) []string {
	var gaps []string
	for _, seg := range strings.Split(paperVenue, "×") {
		if seg == "" {
			continue
		}
		if !allow[seg] && !allow[seg+":perp"] && !allow[seg+":spot"] {
			gaps = append(gaps, seg)
		}
	}
	return gaps
}

// nonCfdLinkReport 非 cfd 结构 approve 的链路体检报告文本（不写白名单）。
func nonCfdLinkReport(structure string, gaps []string) string {
	if len(gaps) == 0 {
		return fmt.Sprintf("链路体检：该结构（%s）无组合白名单闸；venue env 齐，detector/执行路径已可达（perp_perp/spot_carry 的 detector 覆盖差异=Phase B 另议）。", structure)
	}
	return fmt.Sprintf("链路体检：该结构（%s）无组合白名单闸；venue env 缺口：%s（需业主补录 ARB_AUTOTRADE_ALLOW_VENUES+重启后链路可达）。", structure, strings.Join(gaps, ", "))
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
