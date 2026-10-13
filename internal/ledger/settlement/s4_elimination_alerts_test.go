// 单测：结算时策略淘汰告警（runElimination → deps.InsertAlert）。
// 参照 d256a_revival_alert_test.go AlertSink spy 模式——用 fakeDeps.insertAlertMsgs 捕获告警。
package settlement

import (
	"context"
	"strings"
	"testing"

	"nox/internal/store/pgstore"
)

// elimYes 总是触发淘汰的 Eliminator（测试用）。
func elimYes(context.Context, int, float64, float64, float64, bool) (bool, string) {
	return true, "test elimination: avg<0"
}

// TestD268_S4_EliminationAlerts 触发淘汰 → deps.InsertAlert warn 含 [策略淘汰]。
func TestD268_S4_EliminationAlerts(t *testing.T) {
	d := &fakeDeps{
		getStat: &pgstore.StrategyStat{Strategy: "CARRY", Disabled: false},
		aggStat: pgstore.AggregateStat{Total: 10, Wins: 2, AvgPnl: -1.0, MedianPnl: -1.0},
	}
	p := &pgstore.Position{Type: "CARRY", Environment: "demo"}

	runElimination(context.Background(), d, p, elimYes, nil)

	if len(d.insertAlertMsgs) != 1 {
		t.Fatalf("elimination: want 1 alert, got %d: %v", len(d.insertAlertMsgs), d.insertAlertMsgs)
	}
	if !strings.HasPrefix(d.insertAlertMsgs[0], "warn:") {
		t.Errorf("level: want warn, got %s", d.insertAlertMsgs[0])
	}
	if !strings.Contains(d.insertAlertMsgs[0], "[策略淘汰]") {
		t.Errorf("msg: want [策略淘汰], got %s", d.insertAlertMsgs[0])
	}
	if !strings.Contains(d.insertAlertMsgs[0], "test elimination") {
		t.Errorf("msg: want reason 'test elimination', got %s", d.insertAlertMsgs[0])
	}
}

// TestD268_S4_NoEliminationNoAlert 不触发淘汰 → deps.InsertAlert 0 次。
func TestD268_S4_NoEliminationNoAlert(t *testing.T) {
	d := &fakeDeps{
		getStat: &pgstore.StrategyStat{Strategy: "CARRY", Disabled: false},
		aggStat: pgstore.AggregateStat{Total: 10, Wins: 8, AvgPnl: 1.0, MedianPnl: 1.0},
	}
	p := &pgstore.Position{Type: "CARRY", Environment: "demo"}

	runElimination(context.Background(), d, p, noElim, nil)

	if len(d.insertAlertMsgs) != 0 {
		t.Fatalf("no elimination: want 0 alerts, got %d: %v", len(d.insertAlertMsgs), d.insertAlertMsgs)
	}
}

// TestD268_S4_SpyCount 族内多策略禁用 → InsertAlert 恰 1 次（不按策略数重复）。
func TestD268_S4_SpyCount(t *testing.T) {
	d := &fakeDeps{
		getStat: &pgstore.StrategyStat{Strategy: "CARRY", Disabled: false},
		aggStat: pgstore.AggregateStat{Total: 10, Wins: 2, AvgPnl: -1.0, MedianPnl: -1.0},
	}
	p := &pgstore.Position{Type: "CARRY", Environment: "demo"}
	// family 返回 2 个同族策略（CARRY+SWAPARB），族内全禁但告警只 1 条
	family := func(strategy string) []string { return []string{"CARRY", "SWAPARB"} }

	runElimination(context.Background(), d, p, elimYes, family)

	if len(d.insertAlertMsgs) != 1 {
		t.Fatalf("family elimination: want 1 alert (not per-strategy), got %d: %v",
			len(d.insertAlertMsgs), d.insertAlertMsgs)
	}
	// 验证族内 2 个策略都被禁用
	if len(d.disabledStrats) != 2 {
		t.Errorf("disabled: want 2 strategies (CARRY+SWAPARB), got %d: %v",
			len(d.disabledStrats), d.disabledStrats)
	}
}

// hearDeps 听证观察面 spy（D-249①：裁定击杀前调 HearElimination，不翻裁定）。
type hearDeps struct {
	*fakeDeps
	heard []string // 捕获 (trigger, reason)
}

func (h *hearDeps) HearElimination(_ context.Context, trigger string, fam []string,
	total int, winRate, avgPnl, medianPnl float64, reason string) {
	h.heard = append(h.heard, trigger+"|"+reason)
}

// TestD249_HearingObserveOnly 钦定锚：听证钩被调但裁定结果与听证无关——
// disable 仍按判据函数结果落（LLM 零翻权）；听证钩返回什么不影响 DisableStrategy。
func TestD249_HearingObserveOnly(t *testing.T) {
	d := &hearDeps{fakeDeps: &fakeDeps{
		getStat: &pgstore.StrategyStat{Strategy: "CARRY"},
		aggStat: pgstore.AggregateStat{Total: 10, Wins: 2, AvgPnl: -1.0, MedianPnl: -1.0},
	}}
	p := &pgstore.Position{Type: "CARRY", Environment: "demo"}

	runElimination(context.Background(), d, p, elimYes, nil)

	if len(d.heard) != 1 || !strings.HasPrefix(d.heard[0], "CARRY|test elimination") {
		t.Fatalf("听证应在击杀前触发并带理由: %v", d.heard)
	}
	if d.disableStrategy == "" && len(d.disabledStrats) == 0 {
		t.Fatal("听证后淘汰仍应执行（裁定权在判据函数）")
	}
}

// TestD249_HearingFailOpen 听证钩 panic/耗时异常不外溢——runElimination 不因
// 观察面故障漏淘汰（钩内实现自封顶，此锚验证调用序：钩先于 DisableStrategy）。
func TestD249_HearingBeforeDisable(t *testing.T) {
	var order []string
	d := &hearDeps{fakeDeps: &fakeDeps{
		getStat: &pgstore.StrategyStat{Strategy: "CARRY"},
		aggStat: pgstore.AggregateStat{Total: 10},
	}}
	d2 := &hearOrderDeps{hearDeps: d, order: &order}
	p := &pgstore.Position{Type: "CARRY", Environment: "demo"}
	runElimination(context.Background(), d2, p, elimYes, nil)
	if len(order) != 2 || order[0] != "hear" || order[1] != "disable" {
		t.Fatalf("听证应先于禁用: %v", order)
	}
}

// hearOrderDeps 记录 hear/disable 调用序。
type hearOrderDeps struct {
	*hearDeps
	order *[]string
}

func (h *hearOrderDeps) HearElimination(ctx context.Context, trigger string, fam []string,
	total int, winRate, avgPnl, medianPnl float64, reason string) {
	*h.order = append(*h.order, "hear")
	h.hearDeps.HearElimination(ctx, trigger, fam, total, winRate, avgPnl, medianPnl, reason)
}
func (h *hearOrderDeps) DisableStrategy(ctx context.Context, s, r string) error {
	*h.order = append(*h.order, "disable")
	return h.hearDeps.DisableStrategy(ctx, s, r)
}
