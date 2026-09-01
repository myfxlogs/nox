// pipeline_cashflow.go —— D-189 P0-2：平仓现金流写入统一入口（显式错误处理 + per-leg/day 幂等）。
// 从 pipeline_legs_close.go 拆出（pipeline.go >300 行软警告）。
package exec

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// writeCloseCashflows 写入平仓 swap/commission 现金流（D-189 P0-2）。
// 统一入口：pipeline_legs_close.go 与 autoFlattenLEGGED 共用，保证显式错误处理 + 幂等。
// 任一查询/写入失败 → 返回 error（保持 CLOSING，禁止继续到 store.ClosePosition）；
// 已存在（HasCashflowForLegDay=true）→ 跳过（ResumeClose 重复入账防护）。
// fx 为计价币→USD 换算率（与 calcLegPnL 同口径）；amount 为 broker 返回的原始计价币值。
func (p *Pipeline) writeCloseCashflows(ctx context.Context, posID int64, legNo int16, venue string, swap, commission, fx float64) error {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if swap != 0 {
		if err := p.insertCashflowIdempotent(ctx, posID, legNo, "swap", venue, swap*fx, dayStart, now); err != nil {
			return fmt.Errorf("pos=%d leg=%d swap cashflow: %w", posID, legNo, err)
		}
	}
	if commission != 0 {
		// commission 入库为负值（成本），与 pipeline_legs_close.go 原口径一致。
		if err := p.insertCashflowIdempotent(ctx, posID, legNo, "commission", venue, -commission*fx, dayStart, now); err != nil {
			return fmt.Errorf("pos=%d leg=%d commission cashflow: %w", posID, legNo, err)
		}
	}
	return nil
}

// insertCashflowIdempotent 幂等写入单条现金流：先查 HasCashflowForLegDay，已存在跳过，未存在写入。
// 查询失败视为失败（不得当作不存在）；写入失败返回 error。生产路径不得绕过此函数直接调 store。
func (p *Pipeline) insertCashflowIdempotent(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, dayStart, ts time.Time) error {
	has, err := p.store.HasCashflowForLegDay(ctx, posID, legNo, kind, dayStart)
	if err != nil {
		// 查询失败 → fail-closed（不当作不存在）
		p.alertCashflowFailure(ctx, posID, legNo, kind, venue, fmt.Errorf("has-check query: %w", err))
		return fmt.Errorf("has-check query: %w", err)
	}
	if has {
		// 已存在 → 幂等跳过（ResumeClose 重复入账防护）
		return nil
	}
	if err := p.cashflowWriterOrStore().InsertCashflow(ctx, posID, legNo, kind, venue, amount, ts); err != nil {
		p.alertCashflowFailure(ctx, posID, legNo, kind, venue, fmt.Errorf("insert: %w", err))
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

// cashflowWriterOrStore 返回 cashflowWriter seam（D-189 P0-2），nil 时回退 p.store（零值 Pipeline 安全）。
// 返回 store 接口以兼容测试注入；生产路径不得绕过此函数直接调 p.store.InsertCashflow。
func (p *Pipeline) cashflowWriterOrStore() cashflowInserter {
	if p.cashflowWriter != nil {
		return cashflowInserterFunc(func(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error {
			return p.cashflowWriter(ctx, posID, legNo, kind, venue, amount, ts)
		})
	}
	return p.store
}

// alertCashflowFailure 写 critical alert + 日志（D-189 P0-2：现金流失败显式可见性）。
// alert 写入本身失败也必须 log.Printf（不递归创建告警）。
func (p *Pipeline) alertCashflowFailure(ctx context.Context, posID int64, legNo int16, kind, venue string, cause error) {
	msg := fmt.Sprintf("cashflow write failed position=%d leg=%d kind=%s venue=%s: %v", posID, legNo, kind, venue, cause)
	log.Printf("[exec] %s", msg)
	pid := posID
	if err := p.store.InsertAlert(ctx, &pid, "critical", msg); err != nil {
		log.Printf("[exec] cashflow alert insert also failed position=%d: %v", posID, err)
	}
}

// cashflowInserter 抽象 store.InsertCashflow 与 seam 函数（D-189 P0-2）。
type cashflowInserter interface {
	InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error
}

// cashflowInserterFunc 函数适配器（实现 cashflowInserter）。
type cashflowInserterFunc func(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error

func (f cashflowInserterFunc) InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error {
	return f(ctx, posID, legNo, kind, venue, amount, ts)
}

// compile-time: pgstore.Store 实现 cashflowInserter。
var _ cashflowInserter = (*pgstore.Store)(nil)
