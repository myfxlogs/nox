// wire_cex_io_deltabook.go —— D-602 差分落账核（退回㉓测试锚要求：差分核抽纯函数——
// snaps+pnl+ioSum 输入→rows/alerts 输出，IO 副作用全在调用方 mtIODeltaRound）。
package main

import (
	"fmt"
	"time"

	"nox/internal/store/pgstore"
)

// deltaBookWin 单窗差分判定输入/输出（纯函数可测位——退回㉓测试锚）。
type deltaBookWin struct {
	PrevBal, CurBal float64
	PrevTs, CurTs   time.Time
	CurSnapID       int64 // 幂等键 mt5:delta:{snap_id}（小项⑸照准：snap_id 型幂等）
}

// deltaBookRows 差分核：逐窗 res=Δbalance−PnL−已落 io（三解释项全扣防双计——
// 伤①：SumVenueIOExternal 扣窗内已落充提行，ICR/EXR 户残差≈0 自然不落）。
// 窗界=(from,to] 左开右闭（伤小项⑵：PnL/io 同界防相邻窗双扣）。
// 阈 |res|<0.01 skip（原语义：纯 PnL 入账/舍入噪声非外部流）。
// 任一窗 PnL 查败→整户 err fail-closed（伤③：查败 pnl=0 会把仓平盈亏记成入金）。
// 输出行 Status="inferred"（伤②）/ExternalID=mt5:delta:{curSnapID}（小项⑸）。
// alerts=逐窗落账叙事（伤④调用方聚合入 InsertAlertDedup）。
func deltaBookRows(snaps []pgstore.BalanceSnap,
	pnl func(from, to time.Time) (float64, error),
	ioSum func(from, to time.Time) (float64, error),
	ccy string, acctID int64, venue string,
) (rows []pgstore.VenueIO, alerts []string, err error) {
	for i := 1; i < len(snaps); i++ {
		prev, cur := snaps[i-1], snaps[i]
		delta := cur.Balance - prev.Balance
		if delta == 0 {
			continue
		}
		p, err := pnl(prev.Ts, cur.Ts)
		if err != nil {
			return nil, nil, fmt.Errorf("pnl %s window %d: %w", venue, cur.ID, err)
		}
		io, err := ioSum(prev.Ts, cur.Ts)
		if err != nil {
			return nil, nil, fmt.Errorf("io sum %s window %d: %w", venue, cur.ID, err)
		}
		res := delta - p - io
		if res > -0.01 && res < 0.01 {
			continue // 残差~0=已解释（PnL/既有 io/舍入）非外部流
		}
		dir := "deposit"
		amt := res
		if amt < 0 {
			dir, amt = "withdraw", -amt
		}
		rows = append(rows, pgstore.VenueIO{
			Venue: venue, ConfigID: acctID, Direction: dir, Ccy: ccy,
			Amount: amt, Status: "inferred",
			ExternalID: fmt.Sprintf("mt5:delta:%d", cur.ID), Ts: cur.Ts,
		})
		alerts = append(alerts, fmt.Sprintf("窗 %s→%s %s %.2f %s",
			prev.Ts.Format("01-02T15:04Z"), cur.Ts.Format("01-02T15:04Z"), dir, amt, ccy))
	}
	return rows, alerts, nil
}
