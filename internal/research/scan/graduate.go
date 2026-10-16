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

	"nox/internal/store/pgstore"
)

// GradMinAgeDays 毕业最短验证天数（SSOT §1 写死常量不进 env——先跑稳再谈可调）。
const GradMinAgeDays = 5.0

// gradWindowExited 毕业判定候选的 exited 行回看窗口（与 store ListPaperForGraduation 的
// SQL 裁剪同值——SQL 单点裁剪，此处仅注释锚；判定仍以行为准防双源漂移）。
const gradWindowExited = 14 * 24 * time.Hour

// GraduationStore 毕业判定存储依赖（*pgstore.Store 原生实现；scan 不 import store 连接层）。
// pending 自动过期回查面（ListCarryGraduations+GetPaperSpotCarryByID+
// UpdateCarryGraduationDecision——store 既有方法，接口扩列零新 SQL）。
type GraduationStore interface {
	ListPaperForGraduation(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	InsertCarryGraduation(ctx context.Context, g *pgstore.CarryGraduation) (bool, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	ListCarryGraduations(ctx context.Context) ([]pgstore.CarryGraduation, error)
	GetPaperSpotCarryByID(ctx context.Context, id int64) (*pgstore.PaperSpotCarry, error)
	UpdateCarryGraduationDecision(ctx context.Context, id int64, status string, comboID *int64, note string) error
	// D-576：pending↔unqualified 重校验翻转+快照刷新（机修径，终态不动）。
	RevalidateCarryGraduation(ctx context.Context, id int64, status, note string,
		accrued float64, fundingAnn *float64, netAllIn *float64, ageDays float64, flagWarn *string) error
}

// StepCarryGraduation 每轮 carry 扫描后调用（wire_scan 同轮串行单写者）：
// 合格判据（双路径，D-403 S2/D-410 S5/D-468 §1 写死）——
//
//	active：age≥GradMinAgeDays ∧ accrued>0 ∧ low_streak==0（在轨持续盈利），
//	        收敛账=entry/last basis 现算（convergence_usd 列仅退出时写，active 不读列）；
//	exited：exit_reason∈{normalized,age_7d,adverse}（D-474a 加 adverse 排 data_lost）∧ accrued>0 ∧ 持仓时长≥GradMinAgeDays ∧ 在 14d 窗口
//	        （满龄正常退出且盈利=最强证据；promoted_demo/异常退出不算），收敛账=退出冻结列值。
//
// 缺账闸（D-468 §1 语义）：「该有而没有」=拒（est_fee NULL/exited 列 NULL/basis 缺）；
// 「尚未到产生时点」=按该时点口径现算（active 收敛）。demo 行=default 拒=人工域。
// 每合格行插建议（paper_id UNIQUE 幂等，同行二次扫描不重复）；newCount>0 发一条聚合 info。
// 每轮一行 scanned/qualified/new 拒绝遥测（零产出不再无声，D-468 S3）。
// flags：D-463 S2 KB→pipeline 投影——venue 段（×拆分含全串）任一 warn 命中 →
// 建议行 flag_warn=拼接 reason 短串透出（blocked 理论到不了此层=纸面已拦，不另处理）。
func StepCarryGraduation(ctx context.Context, st GraduationStore, now time.Time, flags map[string]pgstore.VenueSymbolFlag) (int, error) {
	rows, err := st.ListPaperForGraduation(ctx)
	if err != nil {
		return 0, fmt.Errorf("graduation: list paper: %w", err)
	}
	newCount := 0
	qualified := 0
	var labels []string
	for _, p := range rows {
		if !graduationQualified(p, now) {
			continue
		}
		qualified++
		net := netAllInUSD(p.AccruedUSD, p.EstRoundTripFeeUSD, effectiveConvergenceUSD(p))
		g := &pgstore.CarryGraduation{
			PaperID:        p.ID,
			VenueA:         p.VenueA,
			VenueB:         p.VenueB,
			VenueTypeA:     p.VenueTypeA, // D-555：type 列透传（数据模型；判据零改动=S3 锚）
			VenueTypeB:     p.VenueTypeB,
			Symbol:         p.Symbol,
			Structure:      p.Structure,
			MtVenue:        p.MtVenue,
			AgeDays:        graduationAgeDays(p, now),
			AccruedUSD:     p.AccruedUSD,
			LastFundingAnn: p.LastFundingAnn,
			NetAllInUSD:    &net, // D-410 S5/D-468 S1：建议行透出全账净额（qualified 行 effective 收敛非 nil 已达）
			FlagWarn:       graduationFlagWarn(p, flags),
		}
		inserted, err := st.InsertCarryGraduation(ctx, g)
		if err != nil {
			// 单行失败不炸整轮（同 paper 轨惯例），WP1 禁静默：日志留痕续跑。
			log.Printf("[graduation] paper#%d insert: %v", p.ID, err)
			continue
		}
		if inserted {
			newCount++
			label := p.VenuePair() + "×" + p.Symbol
			if g.FlagWarn != nil {
				label += "(" + *g.FlagWarn + ")" // D-463 S2：红旗透出到告警文案
			}
			labels = append(labels, label)
		}
	}
	if newCount > 0 {
		msg := fmt.Sprintf("[毕业链] %d 个纸面行验证达标待授权: %s", newCount, strings.Join(labels, ", "))
		// 待授权=待业主决策事件——升 warn 级搭 alerts→feishu 扇出（info 级被推送闸过滤）。
		if err := st.InsertAlert(ctx, nil, "warn", msg); err != nil {
			return newCount, fmt.Errorf("graduation alert: %w", err)
		}
	}
	// D-576③：pending/unqualified 定期重校验——衰减行摘牌转 unqualified
	// （approve 守卫 status='pending' 天然拦），引擎复活同行翻回 pending；
	// 聚合 warn 各至多一条。
	unp, rev, rerr := revalidatePendingGraduations(ctx, st, now, flags)
	if rerr != nil {
		return newCount, rerr
	}
	if unp > 0 {
		if err := st.InsertAlert(ctx, nil, "warn",
			fmt.Sprintf("[毕业链] %d 条 pending 建议引擎衰减自动摘牌（status=unqualified，复活可回牌）", unp)); err != nil {
			return newCount, fmt.Errorf("graduation unpending alert: %w", err)
		}
	}
	if rev > 0 {
		if err := st.InsertAlert(ctx, nil, "info",
			fmt.Sprintf("[毕业链] %d 条 unqualified 建议引擎复活翻回 pending", rev)); err != nil {
			return newCount, fmt.Errorf("graduation revive alert: %w", err)
		}
	}
	log.Printf("[graduation] scanned=%d qualified=%d new=%d", len(rows), qualified, newCount)
	return newCount, nil
}

// effectiveConvergenceUSD D-468 S1：行的收敛账 effective 值——exited=退出冻结列值；
// active=entry/last basis 现算。同一公式（paperConvergenceUSD）同一语义只差时间锚：
// 退出冻结=退出时点 last，现算=当前 last_basis（accrue 每轮在更新）。helper 单点
// 防「判据读一套、建议行透出另一套」的公式漂移。
func effectiveConvergenceUSD(p pgstore.PaperSpotCarry) *float64 {
	if p.Status != "active" {
		return p.ConvergenceUSD
	}
	return paperConvergenceUSD(paperBasisDir(p.Side, p.Structure), p.EntryBasisBps, p.LastBasisBps, p.QtyUSD)
}

// graduationQualified 单行合格判定（S6 合格矩阵的全分支源头）。
// 盈利判据从「carry 在跑」升「all-in 净额为正」；D-468 §1 缺账重定义：
// 「该有而没有」=拒（est_fee NULL=开仓未记账、exited 收敛列 NULL=退出未冻结、active
// basis 缺=现算不可得，KB validation-baseline-empty：首批毕业行不得由失真口径产生）；
// 「尚未到产生时点」=现算（active 收敛——原「读列 NULL 即拒」自 D-410 起把 active
// 毕业路径打成死代码）。netAllIn = accrued − est_fee + effective_convergence ≤ 0 →
// 拒（KB carry-rate-is-not-expectancy：速率再高不算数）。
func graduationQualified(p pgstore.PaperSpotCarry, now time.Time) bool {
	ok, _ := GraduationQualifiedReason(p, now)
	return ok
}

// GraduationQualifiedReason D-474b S2：毕业合格判定导出带拒因（approve 复核闸复用——
// 单判据源禁第二判据）。qualified=true 时 reason 空；否则 reason=拒因短串。
func GraduationQualifiedReason(p pgstore.PaperSpotCarry, now time.Time) (bool, string) {
	conv := effectiveConvergenceUSD(p)
	if p.EstRoundTripFeeUSD == nil || conv == nil {
		return false, "缺账（est_fee/basis NULL）" // 缺账不可毕业
	}
	if netAllInUSD(p.AccruedUSD, p.EstRoundTripFeeUSD, conv) <= 0 {
		return false, "全账净额非正" // 全账未盈利=零证据
	}
	switch p.Status {
	case "active":
		if now.Sub(p.EntryTs).Hours() < GradMinAgeDays*24 {
			return false, "验证天数不足"
		}
		if p.LowStreak != 0 {
			return false, "低息衰减在轨（low_streak 未清零）"
		}
		// 引擎存活检（D-576/业主令 10-16 双洞实证修②）：息差引擎现值率须达
		// D-030 净年化门槛（8%）——浮盈顶包的死引擎行不得挂 pending（WTI
		// #1-3 accrued 微薄+conv 已回吐+funding_ann≈0 仍显幽灵利润的病灶）。
		// 率字段为原生量口径（10.95=10.95%）；NULL=无存活率证据如实拒进不造假。
		if p.LastFundingAnn == nil || *p.LastFundingAnn < 8.0 {
			return false, "引擎熄（last_funding_ann 缺或<8%）"
		}
		return true, ""
	case "exited":
		if p.ExitTs == nil {
			return false, "退出时刻缺失"
		}
		// S3/S4：adverse 行保留毕业资格（净额仍正则逆向止血证据成立）；
		// data_lost（失联截断）不在合格集=不得毕业。
		if p.ExitReason != "normalized" && p.ExitReason != "age_7d" && p.ExitReason != "adverse" {
			return false, "退因不合格（" + p.ExitReason + "）"
		}
		if p.ExitTs.Before(now.Add(-gradWindowExited)) {
			return false, "退出超 14d 证据窗"
		}
		if p.ExitTs.Sub(p.EntryTs).Hours() < GradMinAgeDays*24 {
			return false, "持仓时长不足"
		}
		return true, ""
	default:
		return false, "demo 行=人工域不毕业" // demo 行=人工域不自动毕业
	}
}

// graduationAgeDays 建议时点证据快照：active=已验证时长；exited=持仓时长（进出场区间）。
func graduationAgeDays(p pgstore.PaperSpotCarry, now time.Time) float64 {
	if p.Status == "exited" && p.ExitTs != nil {
		return p.ExitTs.Sub(p.EntryTs).Hours() / 24
	}
	return now.Sub(p.EntryTs).Hours() / 24
}

// FlagVenueSet D-463返修：venue 字段腿级展开（配对串+A/B 臂+mtVenue）——
// blocked 闸与 warn 透出同一口径，腿级旗标在组合串上不致哑火。
// 入参改配对双列直读（venue_b=”=单臂行），配对串展开职责收敛到本函数。
func FlagVenueSet(venueA, venueB string, mtVenue *string) []string {
	pair := venueA
	if venueB != "" {
		pair = venueA + "×" + venueB
	}
	venues := []string{pair, venueA}
	if venueB != "" {
		venues = append(venues, venueB)
	}
	if mtVenue != nil && *mtVenue != "" {
		venues = append(venues, *mtVenue)
	}
	return venues
}

// blockedFlagHit D-463返修：腿级展开任一 venue×symbol 命中 blocked → 返回该旗
// （nil=无旗/未命中）。纸面三轨入口共用——KB 旗标以单腿 venue 落键，组合串必须展开查。
func blockedFlagHit(venueA, venueB, symbol string, mtVenue *string, flags map[string]pgstore.VenueSymbolFlag) *pgstore.VenueSymbolFlag {
	if len(flags) == 0 {
		return nil
	}
	for _, v := range FlagVenueSet(venueA, venueB, mtVenue) {
		if f, ok := flags[pgstore.FlagKey(v, symbol)]; ok && f.Level == "blocked" {
			return &f
		}
	}
	return nil
}

// graduationFlagWarn D-463 S2：行 venue 段（×拆分含全串）+MtVenue 任一 warn 旗标
// 命中 → "⚠"+reason 拼接串（多命中分号连；NULL=无旗）。blocked 不到此层（纸面已拦）。
func graduationFlagWarn(p pgstore.PaperSpotCarry, flags map[string]pgstore.VenueSymbolFlag) *string {
	if len(flags) == 0 {
		return nil
	}
	var reasons []string
	seen := map[string]bool{}
	for _, v := range FlagVenueSet(p.VenueA, p.VenueB, p.MtVenue) {
		if f, ok := flags[pgstore.FlagKey(v, p.Symbol)]; ok && f.Level == "warn" && !seen[f.Reason] {
			reasons = append(reasons, "⚠"+f.Reason)
			seen[f.Reason] = true
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	s := strings.Join(reasons, "; ")
	return &s
}
