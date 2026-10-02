// pipeline_cashflow.go —— D-189 P0-2：平仓现金流写入统一入口（显式错误处理 + per-leg/day 幂等）。
// D-193-A 扩包：迁 InsertCashflowFull（决策 8），填事实身份字段（决策 1-7）。
// 从 pipeline_legs_close.go 拆出（pipeline.go >300 行软警告）。
package exec

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// writeCloseCashflows 写入平仓 swap/commission 现金流（D-189 P0-2；D-422 feeUSD 合并）。
// 统一入口：pipeline_legs_close.go 与 autoFlattenLEGGED 共用，保证显式错误处理 + 幂等。
// 任一查询/写入失败 → 返回 error（保持 CLOSING，禁止继续到 store.ClosePosition）；
// 已存在（HasCashflowForLegDay=true）→ 跳过（ResumeClose 重复入账防护）。
// fx 为计价币→USD 换算率（与 calcLegPnL 同口径）；swap/commission 为 broker 返回的原始计价币值；
// feeUSD 为 CEX 实采 USD 费（已是 USD，不做 fx 换算）——commissionUSD=commission*fx+feeUSD
// 合并为单条 kind=commission 现金流（防双口径/幂等键互吞，D-422 §1.3）。
// positiveFeePtr D-422：正费用 → *float64；≤0 → NULL（不写 0 冒充无费，同 persistOpenLegs 口径）。
func positiveFeePtr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// closeLegFeeUSD D-599 S1：CLOSE 腿 fee_usd 三源——Fill.FeeUSD（CEX 实采 USD）优先，
// 缺省回落 fill.Commission（MT5/历史回填计价币口径，×fx→USD 取绝对值，同
// persistOpenLegs r.Commission<0→-r.Commission 惯例）。无值 → NULL 不写 0。
func closeLegFeeUSD(fill Fill, fx float64) *float64 {
	if fill.FeeUSD > 0 {
		return &fill.FeeUSD
	}
	if fill.Commission != 0 {
		if f := math.Abs(fill.Commission) * fx; f > 0 {
			return &f
		}
	}
	return nil
}

func (p *Pipeline) writeCloseCashflows(ctx context.Context, posID int64, leg pgstore.PositionLeg, swap, commission, feeUSD, fx float64) error {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if swap != 0 {
		// D-425：fill/hp 实采值 → quality='actual'。
		if err := p.insertCashflowIdempotent(ctx, posID, leg.LegNo, "swap", leg.Venue, swap*fx, "actual", dayStart, now, ""); err != nil {
			return fmt.Errorf("pos=%d leg=%d swap cashflow: %w", posID, leg.LegNo, err)
		}
	}
	// D-013 ①：commission 收敛腿键行（event_key=leg:<id>）——腿落库时 CreatePositionLeg
	// 已自动写入，此处对存量腿/历史值补写（幂等）。金额=commission*fx+feeUSD（与
	// 腿 fee_usd 同式；显式传值仅供历史回填 hp 值，零值→nil 由腿 fee_usd 派生）。
	if leg.ID != 0 {
		var amt *float64
		if commissionUSD := commission*fx + feeUSD; commissionUSD != 0 {
			amt = &commissionUSD
		}
		if err := p.store.EnsureLegCommissionCashflow(ctx, leg, amt); err != nil {
			return fmt.Errorf("pos=%d leg=%d commission cashflow: %w", posID, leg.LegNo, err)
		}
	}
	return nil
}

// insertCashflowIdempotent 幂等写入单条现金流：先查 HasCashflowByEventKey（event_key 精确匹配），
// 已存在跳过，未存在写入。查询失败视为失败（不得当作不存在）；写入失败返回 error。
// D-422 R1：幂等键改 event_key 粒度——腿日粗粒度会让同日同腿 OPEN 费与 CLOSE 佣金互吞；
// dedupTag 非空时 event_key 追加 ":<tag>" 区分同源并发写入（OPEN 费传 "open"）。
// D-425：quality 由调用方按证据等级传入（fill/hp 实采='actual'，公式回退='estimated'）。
// 生产路径不得绕过此函数直接调 store。
func (p *Pipeline) insertCashflowIdempotent(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, quality string, dayStart, ts time.Time, dedupTag string) error {
	// D-193-A 扩包（决策 1-7）：构造 Cashflow 填事实身份字段。
	// event_key=exec:<posID>:<legNo>:<kind>:<occurred_at YYYY-MM-DD>[:<dedupTag>]（决策 1 + R1 tag）
	// quality=调用方传入（D-425）；source='exec'（决策 4）
	// occurred_at=ts（平仓成交时刻，决策 5）；received_ts=now（写入时间）
	// account_id=venue 前缀数字（决策 6）；external_event_id/raw_*=NULL（公式路径不造假，决策 2/7）
	now := time.Now().UTC()
	dayStr := dayStart.Format("2006-01-02")
	eventKey := fmt.Sprintf("exec:%d:%d:%s:%s", posID, legNo, kind, dayStr)
	if dedupTag != "" {
		eventKey += ":" + dedupTag
	}
	has, err := p.store.HasCashflowByEventKey(ctx, posID, eventKey)
	if err != nil {
		// 查询失败 → fail-closed（不当作不存在）
		p.alertCashflowFailure(ctx, posID, legNo, kind, venue, fmt.Errorf("has-check query: %w", err))
		return fmt.Errorf("has-check query: %w", err)
	}
	if has {
		// 已存在 → 幂等跳过（ResumeClose 重复入账防护）
		return nil
	}
	src := "exec"
	cf := &pgstore.Cashflow{
		PositionID: posID,
		LegNo:      &legNo,
		Kind:       kind,
		Venue:      venue,
		AmountUSD:  amount,
		Ts:         ts,
		EventKey:   &eventKey,
		Source:     &src,
		Quality:    &quality,
		OccurredAt: &ts,
		ReceivedTs: &now,
		AccountID:  parseVenueAccountIDExec(venue),
	}
	if _, err := p.cashflowWriterOrStore().InsertCashflowFull(ctx, cf); err != nil {
		p.alertCashflowFailure(ctx, posID, legNo, kind, venue, fmt.Errorf("insert: %w", err))
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

// parseVenueAccountIDExec D-193-A 扩包（决策 6）：从 venue key 前缀解析 account_id。
// venue 格式如 "52993526:mt5" → 52993526；解析失败→nil（不猜）。
func parseVenueAccountIDExec(venue string) *int64 {
	// 复用 swapledger 同逻辑；独立函数避免跨包依赖。
	k := keydomain.Parse(venue)
	if k.Type == "" {
		return nil
	}
	var id int64
	if _, err := fmt.Sscanf(k.Venue, "%d", &id); err != nil {
		return nil
	}
	return &id
}

// cashflowWriterOrStore 返回 cashflowWriter seam（D-189 P0-2），nil 时回退 p.store（零值 Pipeline 安全）。
// D-193-A 扩包（决策 8）：接口签名改 Full 形态。
// 返回 store 接口以兼容测试注入；生产路径不得绕过此函数直接调 p.store.InsertCashflowFull。
func (p *Pipeline) cashflowWriterOrStore() cashflowInserter {
	if p.cashflowWriter != nil {
		return cashflowInserterFunc(func(ctx context.Context, c *pgstore.Cashflow) (bool, error) {
			return p.cashflowWriter(ctx, c)
		})
	}
	return p.store
}

// alertCashflowFailure 写 critical alert + 日志（D-189 P0-2：现金流失败显式可见性）。
// alert 写入本身失败也必须 log.Printf（不递归创建告警）。
func (p *Pipeline) alertCashflowFailure(ctx context.Context, posID int64, legNo int16, kind, venue string, cause error) {
	msg := fmt.Sprintf("持仓 #%d 第 %d 腿资金流水入账失败（%s %s）：%v", posID, legNo, kind, venue, cause)
	log.Printf("[exec] %s", msg)
	if err := p.store.InsertPositionAlert(ctx, posID, "critical", msg); err != nil {
		log.Printf("[exec] cashflow alert insert also failed position=%d: %v", posID, err)
	}
}

// cashflowInserter 抽象 store.InsertCashflowFull 与 seam 函数（D-189 P0-2 → D-193-A 扩包决策 8 改签名）。
type cashflowInserter interface {
	InsertCashflowFull(ctx context.Context, c *pgstore.Cashflow) (won bool, err error)
}

// cashflowInserterFunc 函数适配器（实现 cashflowInserter）。
type cashflowInserterFunc func(ctx context.Context, c *pgstore.Cashflow) (won bool, err error)

func (f cashflowInserterFunc) InsertCashflowFull(ctx context.Context, c *pgstore.Cashflow) (won bool, err error) {
	return f(ctx, c)
}

// compile-time: pgstore.Store 实现 cashflowInserter。
var _ cashflowInserter = (*pgstore.Store)(nil)
