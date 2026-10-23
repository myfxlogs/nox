// jit_idle.go —— perp 闲钱常态化扫（jit.go 拆出，行数闸）。JitReturn 只管
// 「平仓释放回转」——常态化面本件补；min_free 闸口径常数见 jit.go 同包域。
package dciexec

import (
	"context"
	"fmt"
	"log"
)

// JitIdleSweep perp 闲钱常态化扫（JitReturn 只管「平仓释放回转」——
// 常态化面本件补）：venue×perp 户 free_margin 超 min_free=MMR×buffer+floor
// 的超额段划转 funding→earn 回申。binance 先行（jitIdleVenues 域）。
// 禁线：MMR/snapshot 读缺 fail-closed 不扫（宁可少留不可断保证金）；
// ioTransfer 幂等键 jit-idle-auto 前缀；armed 缺资面优先（JitSweep 序位
// 不动——本件只动超缓冲闲钱，不触 armed 腿占用金）。
func (e *Executor) JitIdleSweep(ctx context.Context) {
	for _, venue := range []string{"binance"} { // 先行面三所推广位
		if err := e.jitIdleSweepVenue(ctx, venue, "perp"); err != nil {
			log.Printf("[dciexec] jit idle sweep %s 败: %v", venue, err)
			if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
				"JIT 闲钱扫 %s 败: %v——闲钱滞留交易户如实告", venue, err)); aerr != nil {
				log.Printf("[dciexec] jit idle sweep alert: %v", aerr)
			}
		}
	}
}

func (e *Executor) jitIdleSweepVenue(ctx context.Context, venue, vt string) error {
	// min_free 闸件——返修⑦：buffer_multiple 全谱 NULL 死字段弃用
	// （SG 产验 24h 恒 NULL 实证）。分判：空仓〔equity≈free_margin=零在仓
	// 腿〕MMR 真值=0→minFree=$100 地板；有仓〔equity>free〕=used margin
	// 真源（equity−free=在仓占用金——保守源同 buffer 域宁多留）。
	snaps, err := e.Store.LatestVenueAccountSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("%s snapshot 读败 fail-closed: %w", venue, err)
	}
	var equity, snapFree float64
	found := false
	for _, sn := range snaps {
		if sn.Venue == venue && sn.VenueType == vt && sn.Currency == jitCcy &&
			sn.EquityRaw != nil && sn.FreeMarginRaw != nil {
			equity, snapFree = *sn.EquityRaw, *sn.FreeMarginRaw
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s:%s 快照行缺 fail-closed 不扫", venue, vt)
	}
	free, err := e.tradingFree(ctx, venue, vt)
	if err != nil {
		return fmt.Errorf("%s 交易户可动读败: %w", venue, err)
	}
	used := equity - snapFree // 在仓占用金=MMR 保守近似（used≥MMR 常成立）
	if used < 0 {
		used = 0
	}
	minFree := used*jitIdleBufferFactor + jitIdleFloorUSD
	excess := free - minFree
	if excess < jitIdleMinSweepUSD {
		return nil // 闲钱未超闸=如实静默（下拍再试）
	}
	rail, ok := e.earnRail(venue)
	if !ok {
		return fmt.Errorf("%s earn rail 缺席", venue)
	}
	prods, perr := rail.Products(ctx, jitCcy)
	if perr != nil {
		return fmt.Errorf("%s earn 产品面读败（金未动）: %w", venue, perr)
	}
	var pid string
	for _, p := range prods {
		if p.CanPurchase {
			pid = p.ProductID
			break
		}
	}
	if pid == "" {
		return fmt.Errorf("%s earn 无可购档（金未动）", venue)
	}
	nonce := e.nowOr().UnixNano()
	// 划转 perp→funding 面（binance rail 接 earn=USDT-M perp 转出进 spot 域申
	// ——jitTradingWallet 面同 jitReturn 径）。
	from := jitTradingWallet(venue, vt)
	if from == "" {
		return fmt.Errorf("%s:%s 划转源钱包缺席", venue, vt)
	}
	if err := e.jitDoTransfer(ctx, venue, jitCcy, excess, from, jitFundingWallet(venue)); err != nil {
		return fmt.Errorf("%s %s→%s 闲钱划转 %.4f 败: %w", venue, from, jitFundingWallet(venue), excess, err)
	}
	e.ioTransfer(ctx, venue, jitCcy, excess, from, jitFundingWallet(venue),
		fmt.Sprintf("jit-idle-auto:%s:%d:trx", venue, nonce), "")
	body, err := rail.Subscribe(ctx, pid, excess)
	if err != nil {
		return fmt.Errorf("%s earn 闲钱回申 %.4f 败（金已回 %s 未申）: %w",
			venue, excess, jitFundingWallet(venue), err)
	}
	e.ioTransfer(ctx, venue, jitCcy, excess, jitFundingWallet(venue), "earn",
		fmt.Sprintf("jit-idle-auto:%s:%d:sub", venue, nonce), body)
	log.Printf("[dciexec] jit idle sweep %s %.4f %s→earn 成（free %.4f minFree %.4f used %.4f）",
		venue, excess, jitCcy, free, minFree, used)
	return nil
}
