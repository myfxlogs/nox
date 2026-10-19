// jit.go —— venue 内 JIT 资金归集/回转（划转执行件）：
// JitSweep=armed 缺资判定点前置自动归集（earn 桶赎缺口额→交易户划转→原路继续
// sizing；earn 不足/赎回败=调用方落业主请求箱）；JitReturnForPosition=结算事件
// 后回转（平仓腿保证金释放→交易户→funding/spot→earn 回申，D-286 不眠闭环）。
// 净额追踪=fin_venue_io 账（external_id jit-swp:/jit-ret: 前缀差分）——挪多少
// 回多少，绝不扫荡他仓保证金。venue 白名单={binance,okx,gate}（earn rail 面）。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"math"

	"nox/internal/data/keydomain"
)

const jitCcy = "USDT"

// jitVenues earn rail 可达所白名单（venue 内划转执行面——其余裸名如实拒）。
var jitVenues = map[string]bool{"binance": true, "okx": true, "gate": true}

// JitErrKind venue 败因→请求箱 kind 映射（autotrade 落箱分类用）——
// 401/403/权限字样=permission（业主授权面），其余=capital（缺资面）。
func JitErrKind(err error) string {
	if err != nil && venueErrKind(err) == "permission" {
		return "permission"
	}
	return "capital"
}

// JitSweep 缺资归集：venue earn 桶赎 shortfall（缺口额非全额）→交易户划转。
// 闸序：venue 白名单→rail→活期在册足（<shortfall=err 调用方落箱）→逐仓赎→
// 交易户划转（spot 腿免二跳）→io 记账。返回 err=如实败因（permission 可判）。
func (e *Executor) JitSweep(ctx context.Context, venueKey string, shortfall float64) error {
	venue, vt := keydomain.BareOf(venueKey), keydomain.TypeOf(venueKey)
	if !jitVenues[venue] || vt == "" {
		return fmt.Errorf("jit venue 白名单外/未分型 %s（如实不归集）", venueKey)
	}
	if shortfall < 1e-6 || math.IsNaN(shortfall) || math.IsInf(shortfall, 0) {
		return fmt.Errorf("jit 缺口额非法 %.4f（fail-closed）", shortfall)
	}
	rail, ok := e.earnRail(venue)
	if !ok {
		return fmt.Errorf("%s earn rail 缺席（凭据行未配——credential 面）", venue)
	}
	positions, err := rail.Positions(ctx, jitCcy)
	if err != nil {
		return fmt.Errorf("%s earn 在册读败: %w", venue, err)
	}
	var avail float64
	for _, p := range positions {
		avail += p.Amount
	}
	if avail < shortfall {
		return fmt.Errorf("%s earn 桶 %.2f<缺口 %.2f（不足归集——落箱面）", venue, avail, shortfall)
	}
	nonce := e.nowOr().UnixNano()
	need := shortfall
	var redeemed float64 // 已赎在 funding/spot 的资金——后段败须回补 earn（半程残留病灶修）
	for _, p := range positions {
		if need <= 1e-8 {
			break
		}
		amt := math.Min(need, p.Amount)
		if _, err := rail.Redeem(ctx, p.ProductID, amt); err != nil {
			e.jitSweepRevert(ctx, venue, redeemed, nonce)
			return fmt.Errorf("%s earn 赎回 %s %.4f 败: %w", venue, p.ProductID, amt, err)
		}
		e.ioTransfer(ctx, venue, jitCcy, amt, "earn", jitFundingWallet(venue),
			fmt.Sprintf("jit-swp:%s:%d:red", venue, nonce), "")
		redeemed += amt
		need -= amt
	}
	// 二跳：funding/spot→交易户（spot 腿赎回即达——免跳）。
	if to := jitTradingWallet(venue, vt); to != "" {
		if err := e.jitDoTransfer(ctx, venue, jitCcy, shortfall, jitFundingWallet(venue), to); err != nil {
			e.jitSweepRevert(ctx, venue, redeemed, nonce) // 赎成+二跳败=残滞留回补（无回补=钱晾在 funding/spot 实证坑）
			return fmt.Errorf("%s %s→%s 划转 %.4f 败: %w", venue, jitFundingWallet(venue), to, shortfall, err)
		}
		e.ioTransfer(ctx, venue, jitCcy, shortfall, jitFundingWallet(venue), to,
			fmt.Sprintf("jit-swp:%s:%d:trx", venue, nonce), "")
	}
	log.Printf("[dciexec] jit sweep %s %.4f %s earn→%s 成", venueKey, shortfall, jitCcy, jitTradingWallet(venue, vt))
	return nil
}

// JitReturnForPosition 结算事件回转（PostSettle 钩面=func(ctx,posID)）：
// 腿 venue 集 → jit 净额 outstanding>0 的所 → 交易户→funding/spot→earn 回申。
// 败因 best-effort 告警不阻塞结算（结算已落账不回滚——钩纪律同 PostSettler）。
func (e *Executor) JitReturnForPosition(ctx context.Context, posID int64) {
	legs, err := e.Store.ListPositionLegs(ctx, posID)
	if err != nil {
		log.Printf("[dciexec] jit return pos=%d 腿读败: %v", posID, err)
		return
	}
	done := map[string]bool{}
	for _, l := range legs {
		venue, vt := keydomain.BareOf(l.Venue), keydomain.TypeOf(l.Venue)
		if !jitVenues[venue] || done[venue] {
			continue
		}
		done[venue] = true
		if err := e.jitReturn(ctx, venue, vt); err != nil {
			log.Printf("[dciexec] jit return pos=%d %s 败: %v", posID, venue, err)
			if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
				"JIT 回转 %s 败（pos=%d）: %v——资金滞留交易户如实告", venue, posID, err)); aerr != nil {
				log.Printf("[dciexec] jit return alert: %v", aerr)
			}
		}
	}
}

// jitReturn 单所回转：净额 outstanding=min(交易户可动)→划转回 funding/spot→earn 回申。
func (e *Executor) jitReturn(ctx context.Context, venue, vt string) error {
	outstanding, err := e.Store.JitNetOutstanding(ctx, venue)
	if err != nil {
		return fmt.Errorf("jit 净额读败: %w", err)
	}
	if outstanding <= 1e-6 {
		return nil
	}
	rail, ok := e.earnRail(venue)
	if !ok {
		return fmt.Errorf("%s earn rail 缺席", venue)
	}
	free, err := e.tradingFree(ctx, venue, vt)
	if err != nil {
		return fmt.Errorf("%s 交易户可动读败: %w", venue, err)
	}
	amt := math.Min(outstanding, free)
	if amt <= 1e-6 {
		return nil // 交易户无可动=无金可回（如实静默——下仓结算再试）
	}
	nonce := e.nowOr().UnixNano()
	if from := jitTradingWallet(venue, vt); from != "" {
		if err := e.jitDoTransfer(ctx, venue, jitCcy, amt, from, jitFundingWallet(venue)); err != nil {
			return fmt.Errorf("%s %s→%s 回转 %.4f 败: %w", venue, from, jitFundingWallet(venue), amt, err)
		}
		e.ioTransfer(ctx, venue, jitCcy, amt, from, jitFundingWallet(venue),
			fmt.Sprintf("jit-ret:%s:%d:trx", venue, nonce), "")
	}
	// earn 回申：产品面首个可购档（gate/okx rail Subscribe 参面固定 USDT 族；
	// binance 需真 productId——无可购=资金留 funding/spot 如实 warn）。
	prods, perr := rail.Products(ctx, jitCcy)
	if perr != nil {
		return fmt.Errorf("%s earn 产品面读败（金已回 %s 未申）: %w", venue, jitFundingWallet(venue), perr)
	}
	var pid string
	for _, p := range prods {
		if p.CanPurchase {
			pid = p.ProductID
			break
		}
	}
	if pid == "" {
		return fmt.Errorf("%s earn 无可购档（金已回 %s 未申）", venue, jitFundingWallet(venue))
	}
	body, err := rail.Subscribe(ctx, pid, amt)
	if err != nil {
		return fmt.Errorf("%s earn 回申 %.4f 败: %w", venue, amt, err)
	}
	e.ioTransfer(ctx, venue, jitCcy, amt, jitFundingWallet(venue), "earn",
		fmt.Sprintf("jit-ret:%s:%d:sub", venue, nonce), body)
	log.Printf("[dciexec] jit return %s %.4f %s→earn 成（outstanding %.4f）", venue, amt, jitCcy, outstanding)
	return nil
}

// jitSweepRevert 半程残留回补：赎回成+后段败→已赎金额重申回 earn 桶
// （滞留 funding/spot=钱晾外面实证坑——本次靠 autoexec 顺手回申购才归位）。
// 回补败/无可购档=残留如实 warn（告警携金额 venue 可追，下拍 jitReturn 面可续）。
func (e *Executor) jitSweepRevert(ctx context.Context, venue string, amt float64, nonce int64) {
	if amt <= 1e-6 {
		return
	}
	fail := func(why error) {
		log.Printf("[dciexec] jit sweep revert %s %.4f 败: %v", venue, amt, why)
		if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"JIT 归集半程残留：%s 赎回 %.4f 成+后段败——回补 earn 败（金滞 %s，下拍/手工处置）: %v",
			venue, amt, jitFundingWallet(venue), why)); aerr != nil {
			log.Printf("[dciexec] jit revert alert: %v", aerr)
		}
	}
	rail, ok := e.earnRail(venue)
	if !ok {
		fail(fmt.Errorf("earn rail 缺席"))
		return
	}
	prods, err := rail.Products(ctx, jitCcy)
	if err != nil {
		fail(fmt.Errorf("earn 产品面读败: %w", err))
		return
	}
	var pid string
	for _, pr := range prods {
		if pr.CanPurchase {
			pid = pr.ProductID
			break
		}
	}
	if pid == "" {
		fail(fmt.Errorf("earn 无可购档"))
		return
	}
	body, err := rail.Subscribe(ctx, pid, amt)
	if err != nil {
		fail(fmt.Errorf("earn 回补申购 %.4f 败: %w", amt, err))
		return
	}
	e.ioTransfer(ctx, venue, jitCcy, amt, jitFundingWallet(venue), "earn",
		fmt.Sprintf("jit-swp:%s:%d:undo", venue, nonce), body)
	log.Printf("[dciexec] jit sweep revert %s %.4f →earn 回补成", venue, amt)
}

// jitDoTransfer 划转缝（测试注 JitTransferFn 替真发；nil=jitWalletTransfer 实件）。
func (e *Executor) jitDoTransfer(ctx context.Context, venue, ccy string, amt float64, from, to string) error {
	if e.JitTransferFn != nil {
		return e.JitTransferFn(ctx, venue, ccy, amt, from, to)
	}
	return e.jitWalletTransfer(ctx, venue, ccy, amt, from, to)
}
