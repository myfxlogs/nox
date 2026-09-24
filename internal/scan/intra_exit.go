// intra_exit.go —— D-425 日内轨出场管理（止损后段：信号出/统平仓写库）。
package scan

import (
	"context"
	"fmt"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

func (e *IntraEngine) signalExit(ctx context.Context, t pgstore.IntraTrade, now time.Time, rep *IntraStepReport) bool {
	ticks, err := e.st.TicksWindow(ctx, t.Venue, t.Symbol, now.Add(-40*time.Minute))
	if err != nil || len(ticks) < 15 {
		return false
	}
	mean, sd := meanStd(ticks[:len(ticks)-1])
	if sd <= 0 {
		return false
	}
	last := ticks[len(ticks)-1]
	z := (midOf(last.Bid, last.Ask) - mean) / sd
	if (t.Side == "long" && z >= 0) || (t.Side == "short" && z <= 0) {
		if err := e.closeTrade(ctx, t, now, "signal", rep); err != nil {
			rep.Notes = append(rep.Notes, "signal exit error: "+err.Error())
			return false
		}
		rep.SignalXExit++
		return true
	}
	return false
}

// closeTrade 出场落账（滑点=出场价对止损/信号参考价的穿口 bps；pnl 含双侧 taker 费）。
func (e *IntraEngine) closeTrade(ctx context.Context, t pgstore.IntraTrade, now time.Time, reason string, rep *IntraStepReport) error {
	ticks, err := e.st.TicksWindow(ctx, t.Venue, t.Symbol, now.Add(-10*time.Minute))
	if err != nil {
		return err
	}
	if len(ticks) == 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("trade#%d close skip：无 fresh tick（下轮重试）", t.ID))
		return nil
	}
	last := ticks[len(ticks)-1]
	execPx := exitPx(t.Side, last, reason == "stop")
	var pnl float64
	if t.Side == "long" {
		pnl = (execPx - t.EntryPx) * t.Qty
	} else {
		pnl = (t.EntryPx - execPx) * t.Qty
	}
	pnl -= t.NotionalUSD * IntraTakerFeeBps / 10000
	pnl -= execPx * t.Qty * IntraTakerFeeBps / 10000
	ref := t.StopPx
	if reason != "stop" {
		ref = t.EntryPx
	}
	var slip float64
	if ref > 0 {
		slip = math.Abs(execPx-ref) / ref * 10000
	}
	if err := e.st.CloseIntraTrade(ctx, t.ID, execPx, reason, slip, pnl); err != nil {
		return err
	}
	return nil
}

// ReplayGate D-425 回放门（§1 双读之一·上线前）：历史 ticks dry-run 重放（零落库）；P100=最差单订单亏损/保证金
// （含双侧 taker 费）阈值 0.10=保证金 10%（R1：10x 下=1% 价格带，>1% 案例拒）；runtime 门升 v2 真金前置硬条件（复审 §3）。
// n=重放成交笔数（样本不足=门不可判，返回 ok=false 由调用方如实呈报）。
