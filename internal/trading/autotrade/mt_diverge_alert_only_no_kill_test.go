// d113_cexleg_test.go —— D-113 锚测：①MT 侧偏离只告警不杀（CFD 价不进 kill 判定，
// #509 型假偏离免疫）；②CEX 腿 vs crypto 共识锚后偏移破带=唯一 kill 路径；
// ③共识源 <2 → 不判（独苗歪价防呆）；④常驻价差被首观测锚吸收不误杀。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// A1：#509 型回放——仅 MT 腿偏离破带（CEX 腿贴共识），连续多轮也不杀，只落 warn。
func TestD113_MtDiverge_AlertOnlyNoKill(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	ctx := context.Background()
	d113PublishConsensus(t, e, 100.0) // perp 贴共识（cex dev≈0）
	d378bPublishQuotes(t, e, -59.0, 0)
	p := d377OpenPos(t, st, posID)
	e.checkExitCondition(ctx, p) // 锚定轮
	for i := 0; i < 3; i++ {     // dev=50>40 连续 3 轮（超 debounce）仍不杀
		if should, reason := e.checkExitCondition(ctx, p); should {
			t.Fatalf("MT 侧偏离不得平仓, got %q", reason)
		}
	}
	if got := d378bEvalCount(t, e, posID); got != 0 {
		t.Fatalf("MT 偏离不累积 breach, got %d", got)
	}
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE position_id=$1 AND level='warn' AND message LIKE '%仅告警不杀%'`, posID).Scan(&n); err != nil {
		t.Fatalf("alert query: %v", err)
	}
	if n != 1 {
		t.Fatalf("episode 应恰 1 条 warn（去重）, got %d", n)
	}
}

// A2：共识源 <2（只发 1 个）→ CEX 腿再偏也不判不杀（独苗歪价防呆）。
func TestD113_ConsensusSingleSource_NoJudge(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	ctx := context.Background()
	// 只发 1 个共识源
	d113PublishConsensusOnlyOne(t, e, 100.0)
	d378bPublishQuotes(t, e, -9.0, 0)
	p := d377OpenPos(t, st, posID)
	e.checkExitCondition(ctx, p)
	d113MoveCexLeg(t, e, 101.0) // perp 狂偏 ~95bps——无共识不可判
	for i := 0; i < 3; i++ {
		if should, reason := e.checkExitCondition(ctx, p); should {
			t.Fatalf("共识不足不得平仓, got %q", reason)
		}
	}
}

// A3：常驻价差吸收——共识簇偏离腿一截（perp−median 常驻 −50bps），首轮锚定后
// 偏移零变化不杀；再+60bps 突变才杀。
func TestD113_StandingSpreadAbsorbed(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	ctx := context.Background()
	d113PublishConsensus(t, e, 100.55) // 共识 100.55 vs perp 100.075 → 常驻 −47.5bps
	d378bPublishQuotes(t, e, -9.0, 0)
	p := d377OpenPos(t, st, posID)
	for i := 0; i < 3; i++ {
		if should, _ := e.checkExitCondition(ctx, p); should {
			t.Fatalf("常驻价差应被锚吸收不得杀, round %d", i)
		}
	}
	d113MoveCexLeg(t, e, 101.15) // 锚上再偏 ~60bps → 结构断裂
	var reason string
	var should bool
	for i := 0; i < 3 && !should; i++ {
		should, reason = e.checkExitCondition(ctx, p)
	}
	if !should || !strings.HasPrefix(reason, "disaster: cex-leg dev") {
		t.Fatalf("锚后破带应杀, got %v %q", should, reason)
	}
	var kind string
	if err := st.UpdatePositionExitKind(ctx, posID, pgstore.ExitKindFromReason(reason)); err != nil {
		t.Fatalf("write kind: %v", err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT exit_kind FROM positions WHERE id=$1`, posID).Scan(&kind); err != nil {
		t.Fatalf("read kind: %v", err)
	}
	if kind != "disaster" {
		t.Fatalf("exit_kind=%q want disaster", kind)
	}
}

// A4：生产装配形态回归——disasterBreach 已建但 cexBasisAnchor/mtDivergeFlag 为 nil
// （New() 曾只预建 disasterBreach：lazy-init 三表并初始化被跳过 → cexBasisAnchor
// 写即 panic「assignment to entry in nil map」，生产 posLoop 死实证）。各表独立
// 判 nil 后该构型不得再 panic。
func TestD113_PartialInitEngine_NoPanic(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	ctx := context.Background()
	// 复刻 New() 装配形态：disasterBreach 已建、D-113 两表未建（nil）。
	e.disasterBreach = make(map[int64]int)
	e.cexBasisAnchor = nil
	e.mtDivergeFlag = nil
	d113PublishConsensus(t, e, 100.0)
	d378bPublishQuotes(t, e, -9.0, 0)
	p := d377OpenPos(t, st, posID)
	for i := 0; i < 3; i++ { // 首观测锚定写 cexBasisAnchor——nil map 构型下不得 panic
		e.checkExitCondition(ctx, p)
	}
}

// d113PublishConsensusOnlyOne 单共识源（共识不足注入）。
func d113PublishConsensusOnlyOne(t *testing.T, e *Engine, mid float64) {
	t.Helper()
	e.quotes.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "spot",
		Bid: mid - 0.05, Ask: mid + 0.05, Time: time.Now()})
}
