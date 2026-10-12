package reconcile

// d208_test.go —— D-208②：CLOSING 半平裸奔 warn→critical 升级径锚。

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func d208Pos(stuckFor time.Duration) *pgstore.Position {
	t0 := time.Now().Add(-stuckFor)
	return &pgstore.Position{ID: 208, Status: "CLOSING", ClosingStartedAt: &t0}
}

func d208Legs() map[int64][]*pgstore.PositionLeg {
	// 腿0 已平（CLOSE FILLED）、腿1 裸奔（OPEN FILLED 零 CLOSE 行）。
	return map[int64][]*pgstore.PositionLeg{
		208: {
			{LegNo: 0, Phase: "OPEN", Venue: "okx:perp", Symbol: "BTC-USDT-SWAP", Status: "FILLED", Qty: 10},
			{LegNo: 0, Phase: "CLOSE", Venue: "okx:perp", Symbol: "BTC-USDT-SWAP", Status: "FILLED", Qty: 10},
			{LegNo: 1, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XAUUSDm", Status: "FILLED", Qty: 0.01},
		},
	}
}

func TestD208_NakedEscalation(t *testing.T) {
	fs := newFakeStore()
	fs.legs = d208Legs()
	fs.cfg = map[string]int64{"ARB_CLOSING_NAKED_WARN_SEC": 1800, "ARB_CLOSING_NAKED_CRIT_SEC": 7200}
	r := &Reconciler{store: fs, nakedAlertHour: map[int64]int64{}}
	now := time.Now()

	// 31min 裸奔（warn 阈过 crit 阈未到）→ warn 一条含裸腿明细。
	r.closingNakedCheck(context.Background(), d208Pos(31*time.Minute), now)
	if len(fs.alerts) != 1 || fs.alerts[0].level != "warn" {
		t.Fatalf("warn 级一条对拍败: %+v", fs.alerts)
	}
	if !strings.Contains(fs.alerts[0].msg, "3001157:mt5") || !strings.Contains(fs.alerts[0].msg, "XAUUSDm") {
		t.Fatalf("裸腿明细（venue/symbol）须在文: %q", fs.alerts[0].msg)
	}
	if !strings.Contains(fs.alerts[0].msg, "31m") && !strings.Contains(fs.alerts[0].msg, "已裸") {
		t.Fatalf("裸时长须在文: %q", fs.alerts[0].msg)
	}
	// 同小时桶重判→抑频零新增。
	r.closingNakedCheck(context.Background(), d208Pos(40*time.Minute), now)
	if len(fs.alerts) != 1 {
		t.Fatalf("warn 小时桶抑频败: n=%d", len(fs.alerts))
	}
	// 3h（crit 阈过）→ critical（#505 类场景轨迹锚）。
	r.closingNakedCheck(context.Background(), d208Pos(3*time.Hour), now)
	var crit bool
	for _, a := range fs.alerts {
		if a.level == "critical" && strings.Contains(a.msg, "半平裸奔") {
			crit = true
		}
	}
	if !crit {
		t.Fatalf("crit 阈超应升 critical: %+v", fs.alerts)
	}
}

// 阈值未配→降级不判（行缺不误报）；全腿已平→非半平不告警。
func TestD208_NakedSkips(t *testing.T) {
	fs := newFakeStore()
	fs.legs = d208Legs()
	r := &Reconciler{store: fs, nakedAlertHour: map[int64]int64{}}
	r.closingNakedCheck(context.Background(), d208Pos(10*time.Hour), time.Now())
	if len(fs.alerts) != 0 {
		t.Fatalf("阈值未配应降级零告警: %+v", fs.alerts)
	}
	// 全腿已平 → 无裸腿不告警。
	fs.cfg = map[string]int64{"ARB_CLOSING_NAKED_WARN_SEC": 60}
	fs.legs[208] = append(fs.legs[208], &pgstore.PositionLeg{
		LegNo: 1, Phase: "CLOSE", Venue: "3001157:mt5", Symbol: "XAUUSDm", Status: "FILLED"})
	r.closingNakedCheck(context.Background(), d208Pos(10*time.Hour), time.Now())
	if len(fs.alerts) != 0 {
		t.Fatalf("全平应零告警: %+v", fs.alerts)
	}
}
