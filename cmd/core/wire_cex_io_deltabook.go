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
// 退回㉓伤①：SumVenueIOExternal 扣窗内已落充提行，ICR/EXR 户残差≈0 自然不落）。
// 窗界=(from,to] 左开右闭（小项⑵：PnL 同界防相邻窗双扣）。
// 退回㉖伤①成熟度闸：cur.Ts>now−grace 的窗跳过（禁近窗抢跑——venue 充提记录
// 落 order 面可滞后 balance 帧约 48h 实测，近窗差分会与晚落 ticket 双计）。
// 退回㉖伤②前向宽限扣：ioSum 扣窗扩为 (prev.Ts, cur.Ts+grace]——晚落 ticket
// 归属其解释的余额窗；总量守恒（前向扣只在相邻窗间挪归属，Σdelta 行+Σticket 恒等，
// 多窗连环自平衡）。grace 值由调用方给（app_config 键，缺省 72h 覆盖实测+裕量）。
// 阈 |res|<0.01 skip（原语义）。任一窗 PnL/io 查败→整户 err fail-closed（退回㉓伤③）。
// 输出行 Status="inferred"（伤②）/ExternalID=mt5:delta:{curSnapID}（小项⑸照准）。
// alerts=逐窗落账叙事（伤④调用方聚合入 InsertAlertDedup）。
func deltaBookRows(snaps []pgstore.BalanceSnap, now time.Time, grace time.Duration,
	pnl func(from, to time.Time) (float64, error),
	ioSum func(from, to time.Time) (float64, error),
	ccy string, acctID int64, venue string,
) (rows []pgstore.VenueIO, alerts []string, err error) {
	if grace < 0 {
		grace = 0
	}
	maturityLine := now.Add(-grace) // now=判定时刻（调用方传 UTC——测试可注入锚点）
	for i := 1; i < len(snaps); i++ {
		prev, cur := snaps[i-1], snaps[i]
		if cur.Ts.After(maturityLine) {
			continue // 退回㉖伤①成熟度闸：近窗禁抢跑（等晚落记录成熟）
		}
		delta := cur.Balance - prev.Balance
		if delta == 0 {
			continue
		}
		p, err := pnl(prev.Ts, cur.Ts)
		if err != nil {
			return nil, nil, fmt.Errorf("pnl %s window %d: %w", venue, cur.ID, err)
		}
		io, err := ioSum(prev.Ts, cur.Ts.Add(grace)) // 伤②前向宽限扣：晚落 ticket 归属本窗
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
