// jit_okx_earn.go —— okx 活期+earn 双向 JIT 回腿（设计案照准落地件）。
// 纯事件驱动零定时器：balance_and_position 推送/WS 重连/boot/settle 四源即拍。
// 钉三件：①Subscribe per-venue 单飞锁（并发余额事件双过闲额判定=超归集险防）
// ②armed 缺资径=JitSweep 既有赎回链（flexible 赎回非瞬到=降级时延语义在案）
// ③闲额=unified availBal+funding 户双源（FreeBalance 复用——SG 自划转实证
// 单读 unified 假贫修）；调资序=活期→交易户内部划转第一顺位→earn 赎回第二。
// 禁线：定时轮询件零造、收单方不合格如实报闲、armed 保证金优先权高于 earn 占用。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// jitOkxEarnMu per-venue 单飞锁（钉①——并发余额事件双过闲额判定=超归集险防；
// 幂等键兜写不兜判，判定段须串行）。
var jitOkxEarnMu sync.Mutex

// sweepExcess 留备式可扫额（sweepable=free−armedReserve−floor——同函数单源
// 供判定件+测试双态断言）。
func sweepExcess(free, reserve float64) float64 { return free - reserve - jitOkxIdleFloorUSD }

// jitOkxReserveKey armed 保证金预留键（返修⑤钦定留备式——sweepable=
// free−armedReserve−floor：预留头备即时开单+超出照常归集=优先权与
// 钱不眠同立；枚举式缺资闸根除〔armed-enabled-无仓位恒真=归集永停死胎修〕）。
const jitOkxReserveKey = "NOX_OKX_MARGIN_RESERVE_USD"
const jitOkxReserveDefaultUSD = 500.0

// jitOkxIdleFloorUSD okx 活期归集地板（binance $100 同域）。
const jitOkxIdleFloorUSD = 100.0

// JitOkxEarnEvent 事件即拍入口（四源调用面同径——account push/重连/boot/settle）。
// 读败 fail-closed 不判定；armed 缺资竞争停（armed 保证金优先权高于 earn 占用）。
func (e *Executor) JitOkxEarnEvent(ctx context.Context) {
	if !jitOkxEarnMu.TryLock() { // 并发事件并拍=去重（判定段单飞锁）
		return
	}
	defer jitOkxEarnMu.Unlock()
	if err := e.jitOkxEarnSweep(ctx); err != nil {
		log.Printf("[dciexec] okx earn jit 判败: %v", err)
	}
}

// jitOkxEarnSweep 闲额判定→归集执行（评估闸不合格如实报闲——如实非静默）。
func (e *Executor) jitOkxEarnSweep(ctx context.Context) error {
	rail, ok := e.earnRail("okx")
	if !ok {
		return fmt.Errorf("okx earn rail 缺席")
	}
	// 留备式预留（返修⑤钦定式）：armed 保证金头备=NOX_OKX_MARGIN_RESERVE_USD
	// 读败=保守默认 500（宁多备不欠保证金=fail-closed 守）。
	reserve := jitOkxReserveDefaultUSD
	if rv, rok, rerr := e.Store.AppConfigInt64(ctx, jitOkxReserveKey); rerr == nil && rok && rv > 0 {
		reserve = float64(rv)
	}
	// 混源钉（返修⑥钦定式）：free = unified〔okx_event 快照行须鲜〕+
	// fundingAvail〔asset/balances REST=钦定保留面〕——快照行不鲜=funding
	// 单边也不判 fail-closed（单读 unified 报 0 假贫病灶防复返）。
	snaps, serr := e.Store.LatestVenueAccountSnapshots(ctx)
	if serr != nil {
		return fmt.Errorf("okx 快照读败 fail-closed: %w", serr)
	}
	var unified float64
	var ts time.Time
	found := false
	for _, sn := range snaps {
		if sn.Venue == "okx" && sn.VenueType == "unified" && sn.Currency == jitCcy &&
			sn.EquityRaw != nil {
			if sn.FreeMarginRaw != nil {
				unified = *sn.FreeMarginRaw
			} else {
				unified = *sn.EquityRaw // free_margin 不采=equity 回退同 REST 口径
			}
			ts = sn.FetchedAt
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("okx_event 快照行缺 fail-closed 不扫")
	}
	if time.Since(ts) > 5*time.Minute {
		return fmt.Errorf("okx_event 快照陈 %.0fs>300s fail-closed 不扫（funding 单边不判）", time.Since(ts).Seconds())
	}
	r, ok := rail.(*okxEarnRail)
	if !ok {
		return fmt.Errorf("okx earn rail 类型不符")
	}
	funding, ferr := r.fundingAvail(ctx, jitCcy) // 钦定保留面 REST 读
	if ferr != nil {
		return fmt.Errorf("okx funding 余额读败 fail-closed: %w", ferr)
	}
	free := unified + funding            // 混源合成=unified〔WS 鲜〕+funding〔REST 钦定保留面〕
	excess := sweepExcess(free, reserve) // sweepable=free−armedReserve−floor（同函数单源）
	log.Printf("[dciexec] okx earn jit free %.4f reserve %.4f excess %.4f", free, reserve, excess)
	if excess < jitIdleMinSweepUSD {
		return nil // 闲钱未超闸=如实静默（下一事件再判）
	}
	prods, perr := rail.Products(ctx, jitCcy)
	if perr != nil {
		return fmt.Errorf("okx earn 产品面读败（金未动）: %w", perr)
	}
	var pid string
	for _, p := range prods {
		if p.CanPurchase {
			pid = p.ProductID
			break
		}
	}
	if pid == "" {
		return fmt.Errorf("okx earn 无合格收单方（金未动——如实报闲非造单）")
	}
	nonce := e.nowOr().UnixNano()
	if _, err := rail.Subscribe(ctx, pid, excess); err != nil {
		if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"JIT okx earn 归集败: %v——闲钱滞留活期如实告", err)); aerr != nil {
			log.Printf("[dciexec] okx earn jit alert: %v", aerr)
		}
		return fmt.Errorf("okx earn subscribe: %w", err)
	}
	// io 记账幂等 jit-earn-okx 前缀（bn 模式同构）。
	e.ioTransfer(ctx, "okx", jitCcy, excess, "funding", "earn",
		fmt.Sprintf("jit-earn-okx:%d:sub", nonce),
		fmt.Sprintf("okx earn subscribe %s %.4f", pid, excess))
	return nil
}

// BalanceEventHook 余额事件即拍钩（buildDCIExecutor 装配套持——nil=不判定）。
var BalanceEventHook func(context.Context)

// EmitBalanceEvent balance_and_position 推送即拍（nil 安全——私有流 events→executor 缝）。
func EmitBalanceEvent(ctx context.Context) {
	if BalanceEventHook != nil {
		BalanceEventHook(ctx)
	}
}
