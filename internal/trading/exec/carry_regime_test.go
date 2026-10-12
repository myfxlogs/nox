package exec

// 锚：FundingRegimeOK 纯函数 T1/T1b-T4 + CarryFundingRegimeCheck helper 锚。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// mkRows 构造升序采样点（时间间隔 5min，ts 以 base 起步）。
func mkRows(base time.Time, vals ...float64) []pgstore.FactPoint {
	rows := make([]pgstore.FactPoint, len(vals))
	for i, v := range vals {
		rows[i] = pgstore.FactPoint{Ts: base.Add(time.Duration(i) * 5 * time.Minute), Value: v}
	}
	return rows
}

var regimeBase = time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC)

// TestFundingRegimeOK_T1_485Replay D-328 T1 485 回放：买侧 perp（requiredSign=−1）收入腿
// （latest=−12 收入成立）但窗口含归零 → 拒。设计 §2 判据实证形态。
func TestFundingRegimeOK_T1_485Replay(t *testing.T) {
	rows := mkRows(regimeBase, -17, -4, 0, -12, 0, 0, 0, 0, 0, 0, 0, 0, -12) // n=13 ≥ 12
	gate, income, reason := FundingRegimeOK(rows, -1, 2.0, 12, false)
	if gate {
		t.Errorf("窗口含归零应拒绝, gate=true (%s)", reason)
	}
	if !income {
		t.Errorf("latest=-12 为收入腿, income=false")
	}
	if !containsAll(reason, "latest=-12", "max=0") {
		t.Errorf("reason 应带 latest/max 供日志, got %q", reason)
	}
}

// TestFundingRegimeOK_T1b_NonIncomeSkip T1b：latest 归零（非收入腿）→ 跳过不约束（防误杀支出腿）。
func TestFundingRegimeOK_T1b_NonIncomeSkip(t *testing.T) {
	rows := mkRows(regimeBase, -17, -4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0) // latest=0
	gate, income, reason := FundingRegimeOK(rows, -1, 2.0, 12, false)
	if !gate {
		t.Errorf("非收入腿应放行（跳过不约束）, gate=false (%s)", reason)
	}
	if income {
		t.Errorf("latest=0 非收入腿, income=true")
	}
	if !containsAll(reason, "non-income") {
		t.Errorf("reason 应标 non-income, got %q", reason)
	}
}

// TestFundingRegimeOK_T2_Pass T2：全程深负收入腿（min −25，含 floor 边界 −2 样本）→ 放行。
func TestFundingRegimeOK_T2_Pass(t *testing.T) {
	vals := []float64{-25, -20, -18, -15, -12, -11, -10, -10, -9, -9, -8.5, -8, -2} // 13 样本，末位 -2=floor 边界（≥ 通过；变异 <= 时转红）
	rows := mkRows(regimeBase, vals...)
	gate, income, reason := FundingRegimeOK(rows, -1, 2.0, 12, false)
	if !gate || !income {
		t.Errorf("全程深负应放行, gate=%v income=%v (%s)", gate, income, reason)
	}
}

// TestFundingRegimeOK_T3_SellSide T3 卖侧对称：requiredSign=+1，含 +0.5（<floor）→ 拒；全程 ≥+5 → 放。
func TestFundingRegimeOK_T3_SellSide(t *testing.T) {
	bad := mkRows(regimeBase, 25, 12, 8, 5.5, 5, 5, 5, 5, 5, 5, 5, 0.5) // 含 +0.5 < floor
	if gate, _, reason := FundingRegimeOK(bad, 1, 2.0, 12, false); gate {
		t.Errorf("含 +0.5 应拒绝, gate=true (%s)", reason)
	}
	good := mkRows(regimeBase, 25, 12, 8, 5.5, 5, 5, 5, 5, 5, 5, 5, 5)
	if gate, income, reason := FundingRegimeOK(good, 1, 2.0, 12, false); !gate || !income {
		t.Errorf("全程 ≥+5 应放行, gate=%v income=%v (%s)", gate, income, reason)
	}
}

// TestFundingRegimeOK_T4_FailClosed T4：样本不足 / 零样本 → 拒（fail-closed，未知 regime 不放行）。
func TestFundingRegimeOK_T4_FailClosed(t *testing.T) {
	rows := mkRows(regimeBase, -25, -20, -18, -15, -12) // n=5 < 12，全程深负仍拒
	gate, income, reason := FundingRegimeOK(rows, -1, 2.0, 12, false)
	if gate || income {
		t.Errorf("样本不足应拒绝, gate=%v income=%v (%s)", gate, income, reason)
	}
	if !containsAll(reason, "insufficient") {
		t.Errorf("reason 应标 insufficient, got %q", reason)
	}
	if gate, _, _ := FundingRegimeOK(nil, -1, 2.0, 12, false); gate {
		t.Error("零样本应拒绝")
	}
}

// fakeQuerier helper 级测试用：固定返回值或错误。
type fakeQuerier struct {
	rows                  []pgstore.FactPoint
	err                   error
	lastVenue, lastSymbol string
}

func (f *fakeQuerier) FundingPredWindow(_ context.Context, venue, symbol string, _ time.Time) ([]pgstore.FactPoint, error) {
	f.lastVenue, f.lastSymbol = venue, symbol
	return f.rows, f.err
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !stringContains(s, sub) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestCarryFundingRegimeCheck_Helper helper 锚：裸 venue 键折算 / perp×perp 支出腿豁免 /
// MT 腿不触发 / 查询错误 fail-closed。
func TestCarryFundingRegimeCheck_Helper(t *testing.T) {
	cfg := CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12}
	now := regimeBase.Add(time.Hour)
	healthy := mkRows(regimeBase, -25, -20, -18, -15, -12, -11, -10, -10, -9, -9, -8.5, -8)

	t.Run("裸venue与卖腿symbol折算", func(t *testing.T) {
		fq := &fakeQuerier{rows: healthy}
		ok, reason := CarryFundingRegimeCheck(context.Background(), fq, "gate:perp", "3001157:mt5", "CLUSDT", "", now, cfg)
		if !ok {
			t.Errorf("健康收入腿应放行, reason=%s", reason)
		}
		if fq.lastVenue != "gate" || fq.lastSymbol != "CLUSDT" {
			t.Errorf("fact 键应=裸 venue+买侧 canonical, got %s/%s", fq.lastVenue, fq.lastSymbol)
		}
	})

	t.Run("perp×perp支出腿豁免", func(t *testing.T) {
		// 买侧 XAUTUSDT@okx:perp 收入腿健康；卖侧 PAXGUSDT@gate:perp 付息腿（latest 正、含破 floor）→ 豁免不拒。
		fq := &fakeQuerier{rows: healthy}
		ok, reason := CarryFundingRegimeCheck(context.Background(), fq, "okx:perp", "gate:perp", "XAUTUSDT", "PAXGUSDT", now, cfg)
		if !ok {
			t.Errorf("perp×perp 付息腿应豁免, reason=%s", reason)
		}
		// 两腿各查一次：最后查的是卖侧（顺序 buy→sell）
		if fq.lastVenue != "gate" || fq.lastSymbol != "PAXGUSDT" {
			t.Errorf("卖腿应用 canonicalSell, got %s/%s", fq.lastVenue, fq.lastSymbol)
		}
	})

	t.Run("MT腿不触发", func(t *testing.T) {
		calls := 0
		fq := &fakeQuerier{rows: healthy}
		_ = fq
		// 双腿均 MT → 不发生任何查询（fakeQuerier 无法计数，用无 :perp 腿直接断言放行且零查询由实现保证）
		ok, reason := CarryFundingRegimeCheck(context.Background(), nopQuerier{calls: &calls}, "3001157:mt5", "52993526:mt4", "XAUUSD", "", now, cfg)
		if !ok {
			t.Errorf("双 MT 腿应放行, reason=%s", reason)
		}
		if calls != 0 {
			t.Errorf("无 :perp 腿不应查询, calls=%d", calls)
		}
	})

	t.Run("查询错误fail-closed", func(t *testing.T) {
		fq := &fakeQuerier{err: errors.New("db down")}
		ok, reason := CarryFundingRegimeCheck(context.Background(), fq, "gate:perp", "3001157:mt5", "CLUSDT", "", now, cfg)
		if ok {
			t.Error("查询错误应 fail-closed 拒绝")
		}
		if !containsAll(reason, "fail-closed", "db down") {
			t.Errorf("reason 应带 fail-closed 与错误, got %q", reason)
		}
	})
}

// nopQuerier 计数查询器（MT 腿零查询断言用）。
type nopQuerier struct{ calls *int }

func (n nopQuerier) FundingPredWindow(context.Context, string, string, time.Time) ([]pgstore.FactPoint, error) {
	*n.calls++
	return nil, fmt.Errorf("should not be called")
}
