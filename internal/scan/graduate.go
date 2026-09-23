// graduate.go D-403 S2：验证毕业链判定器——纸面行验证达标 → 产生授权建议（carry_graduations 行）
// +聚合 info 告警一条（非逐行刷屏）。毕业=「扫描→验证→实盘」链的授权建议源头；
// 授权动作在 dashboard service approve（业主显式三 cap），本文件只产出建议零授权。
package scan

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// GradMinAgeDays 毕业最短验证天数（SSOT §1 写死常量不进 env——先跑稳再谈可调）。
const GradMinAgeDays = 5.0

// gradWindowExited 毕业判定候选的 exited 行回看窗口（与 store ListPaperForGraduation 的
// SQL 裁剪同值——SQL 单点裁剪，此处仅注释锚；判定仍以行为准防双源漂移）。
const gradWindowExited = 14 * 24 * time.Hour

// GraduationStore 毕业判定存储依赖（*pgstore.Store 原生实现；scan 不 import store 连接层）。
type GraduationStore interface {
	ListPaperForGraduation(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	InsertCarryGraduation(ctx context.Context, g *pgstore.CarryGraduation) (bool, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// StepCarryGraduation 每轮 carry 扫描后调用（wire_scan 同轮串行单写者）：
// 合格判据（双路径，SSOT S2 写死）——
//
//	active：age≥GradMinAgeDays ∧ accrued>0 ∧ low_streak==0（在轨持续盈利）；
//	exited：exit_reason∈{normalized,age_7d} ∧ accrued>0 ∧ 持仓时长≥GradMinAgeDays ∧ 在 14d 窗口
//	（满龄正常退出且盈利=最强证据；promoted_demo/异常退出不算）。
//
// 每合格行插建议（paper_id UNIQUE 幂等，同行二次扫描不重复）；newCount>0 发一条聚合 info。
func StepCarryGraduation(ctx context.Context, st GraduationStore, now time.Time) (int, error) {
	rows, err := st.ListPaperForGraduation(ctx)
	if err != nil {
		return 0, fmt.Errorf("graduation: list paper: %w", err)
	}
	newCount := 0
	var labels []string
	for _, p := range rows {
		if !graduationQualified(p, now) {
			continue
		}
		net := netAllInUSD(p.AccruedUSD, p.EstRoundTripFeeUSD, p.ConvergenceUSD)
		g := &pgstore.CarryGraduation{
			PaperID:        p.ID,
			Venue:          p.Venue,
			Symbol:         p.Symbol,
			Structure:      p.Structure,
			MtVenue:        p.MtVenue,
			AgeDays:        graduationAgeDays(p, now),
			AccruedUSD:     p.AccruedUSD,
			LastFundingAnn: p.LastFundingAnn,
			NetAllInUSD:    &net, // D-410 S5：建议行透出全账净额（qualified 行双列非 NULL 已达）
		}
		inserted, err := st.InsertCarryGraduation(ctx, g)
		if err != nil {
			// 单行失败不炸整轮（同 paper 轨惯例），WP1 禁静默：日志留痕续跑。
			log.Printf("[graduation] paper#%d insert: %v", p.ID, err)
			continue
		}
		if inserted {
			newCount++
			labels = append(labels, p.Venue+"×"+p.Symbol)
		}
	}
	if newCount > 0 {
		msg := fmt.Sprintf("[毕业链] %d 个纸面行验证达标待授权: %s", newCount, strings.Join(labels, ", "))
		if err := st.InsertAlert(ctx, nil, "info", msg); err != nil {
			return newCount, fmt.Errorf("graduation alert: %w", err)
		}
	}
	return newCount, nil
}

// graduationQualified 单行合格判定（S6 合格矩阵的全分支源头）。
// D-410 S5：盈利判据从「carry 在跑」升「all-in 净额为正」——est_fee/convergence 任一
// NULL → 拒（缺账不可毕业，KB validation-baseline-empty：首批毕业行不得由失真口径
// 产生）；netAllIn = accrued − est_fee + convergence ≤ 0 → 拒。
func graduationQualified(p pgstore.PaperSpotCarry, now time.Time) bool {
	if p.EstRoundTripFeeUSD == nil || p.ConvergenceUSD == nil {
		return false // 缺账不可毕业（展示侧 net 照算+partial 标注不隐瞒）
	}
	if netAllInUSD(p.AccruedUSD, p.EstRoundTripFeeUSD, p.ConvergenceUSD) <= 0 {
		return false // 全账未盈利=零证据（KB carry-rate-is-not-expectancy：速率再高不算数）
	}
	switch p.Status {
	case "active":
		return now.Sub(p.EntryTs).Hours() >= GradMinAgeDays*24 && p.LowStreak == 0
	case "exited":
		if p.ExitTs == nil {
			return false
		}
		if p.ExitReason != "normalized" && p.ExitReason != "age_7d" {
			return false
		}
		if p.ExitTs.Before(now.Add(-gradWindowExited)) {
			return false // 14d 窗口外=陈旧证据不进建议
		}
		return p.ExitTs.Sub(p.EntryTs).Hours() >= GradMinAgeDays*24
	default:
		return false // demo 行=人工域不自动毕业
	}
}

// graduationAgeDays 建议时点证据快照：active=已验证时长；exited=持仓时长（进出场区间）。
func graduationAgeDays(p pgstore.PaperSpotCarry, now time.Time) float64 {
	if p.Status == "exited" && p.ExitTs != nil {
		return p.ExitTs.Sub(p.EntryTs).Hours() / 24
	}
	return now.Sub(p.EntryTs).Hours() / 24
}

// SymbolMapEntry broker_symbol_map 行的解析投影（调用方自 store.ListSymbolMappings 转换，
// scan 不反向依赖 dashboard 装配层类型）。
type SymbolMapEntry struct {
	Canonical string
	IsPrimary bool
}

// ResolvedCfdPair canonical 双解析产物（S3 approve 写组合/S4 detector hint 构造共用）。
type ResolvedCfdPair struct {
	MtCanonical  string // broker_symbol_map canonical（detector 形，如 XAUUSD）
	MtVenueKey   string // "{mt_venue}:{mt4|mt5}"
	CexCanonical string // symbol+"USDT"（XAU→XAUUSDT/BTC→BTCUSDT/US30→US30USDT 实证规则）
}

// ResolveGraduationCanonicals D-403 S3/S4：canonical 双解析**单一 resolver**（KB
// cex-canonical-fallback-from-symbol：canonical 三套命名空间并存是全案最脆缝，禁两处各写）。
// mt 侧 broker_symbol_map 查 NormalizeJoinKey(canonical)==symbol（is_primary 优先否则任一行）；
// 映射缺失=error fail-closed 不臆造。cex venue key 由调用方注入（approve 取 paper venue 组合串、
// hint 取 universe 行 cex_venue——resolver 不经手 venue 归属，只钉 canonical 命名与 join 规范化单源）。
func ResolveGraduationCanonicals(symbol, mtVenue string, platform int, mappings []SymbolMapEntry) (ResolvedCfdPair, error) {
	if symbol == "" || mtVenue == "" {
		return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: empty symbol/mtVenue")
	}
	mtCanon := ""
	primaryCanon := ""
	for _, m := range mappings {
		if NormalizeJoinKey(m.Canonical) == symbol {
			if primaryCanon == "" {
				primaryCanon = m.Canonical
			}
			if m.IsPrimary {
				mtCanon = m.Canonical
				break
			}
		}
	}
	if mtCanon == "" {
		if primaryCanon != "" {
			mtCanon = primaryCanon
		} else {
			return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: no broker_symbol_map row venue=%s symbol=%s（fail-closed 不臆造）", mtVenue, symbol)
		}
	}
	pt := "mt4"
	if platform == 1 {
		pt = "mt5"
	}
	return ResolvedCfdPair{
		MtCanonical:  mtCanon,
		MtVenueKey:   mtVenue + ":" + pt,
		CexCanonical: symbol + "USDT",
	}, nil
}
