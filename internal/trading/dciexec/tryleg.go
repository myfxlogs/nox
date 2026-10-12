// tryleg.go —— D-191：单腿申购执行（自 exec.go 拆分，纯移动零语义改）。
package dciexec

import (
	"arb/internal/risk/gate"
	"context"
	"fmt"
	"strings"

	"arb/internal/store/pgstore"
)

// tryLeg 单腿：池余额×share→qty、spot 不足→活期赎回缺口→重读、候选档 strike 闸
// →最优、幂等→签名 POST→回执（R6 前置 redeem 步）。
func (e *Executor) tryLeg(ctx context.Context, l gate.DCILeg, share float64, tenorCap int, bound, spot float64, propID *int64) (string, error) {
	bal, err := e.poolBalance(ctx, l.QtyCcy)
	if err != nil {
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, l, "permission", "earn_position",
				"请开通 binance 赚币子账查询权限（%s 池余额读败：%v）", l.QtyCcy, err)
		}
		return "", e.gap(ctx, l, "runtime", "earn_position",
			"系统侧病灶：%s 池余额读败：%v", l.QtyCcy, err)
	}
	qty := bal * share
	if qty <= 0 {
		return fmt.Sprintf("%s腿跳过（%s 可动=0）", l.Name, l.QtyCcy), nil
	}
	// 前置赎回步：现货可动不足→赚币活期赎回缺口→重读现货（单次步零重试）。
	spotFree, err := e.freeBalance(ctx, l.QtyCcy)
	if err != nil {
		return "", fmt.Errorf("%s 现货余额读败：%w", l.QtyCcy, err)
	}
	if spotFree < qty*(1-1e-6) { // D-203②a：尘埃不阻塞（≥qty×(1-1e-6) 判足量零赎回）
		redeemed, notes, rerr := e.redeemShortfall(ctx, l.QtyCcy, qty-spotFree)
		if rerr != nil {
			if venueErrKind(rerr) == "permission" {
				return "", e.gap(ctx, l, "permission", "earn_redeem",
					"请开通 binance 赚币活期赎回权限（%s 赎回败：%v）", l.QtyCcy, rerr)
			}
			return "", e.gap(ctx, l, "runtime", "earn_redeem",
				"系统侧病灶：%s 活期赎回败：%v", l.QtyCcy, rerr)
		}
		if redeemed > 0 {
			if spotFree, err = e.freeBalance(ctx, l.QtyCcy); err != nil {
				return "", fmt.Errorf("%s 赎回后现货重读败：%w", l.QtyCcy, err)
			}
		}
		if spotFree < qty {
			skipTxt := ""
			if len(notes) > 0 {
				skipTxt = "；" + strings.Join(notes, "；")
			}
			return "", e.gap(ctx, l, "holding", l.QtyCcy,
				"%s 现货可动不足（池算 qty %.6f、赎回后现货 %.6f%s）", l.QtyCcy, qty, spotFree, skipTxt)
		}
	}
	qs, err := e.Store.ListDCIQuotes(ctx, "binance", "BTC", l.OptType, l.InvestCcy, tenorCap, bound)
	if err != nil {
		return "", err
	}
	var q *pgstore.CexDCIQuote
	var sawStrikeOK bool
	for i := range qs {
		if !l.StrikePass(&qs[i], bound) {
			continue
		}
		sawStrikeOK = true
		if qs[i].OrderRef == "" {
			continue // R10③：order_ref 缺行跳次档（申购 orderId 参源）
		}
		if qs[i].MinAmount == nil {
			continue // R12：min_amount 缺行跳次档（floor 位真源缺=不可发参）
		}
		q = &qs[i]
		break
	}
	if q == nil {
		if sawStrikeOK {
			return "", e.gap(ctx, l, "runtime", "dual_quote",
				"系统侧病灶：%s腿无可申购参数档（达标档缺 order_ref/min_amount——采集面未落）", l.Name)
		}
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"%s腿无达标档在售（spot %.0f，strike 界 %s）", l.Name, spot, l.BoundText(bound))
	}
	// R11/R12：depositAmount floor 至 min_amount 小数位；界外 fail-closed
	// 如实败（不补不截——补到 min=多交、截到 max=少投，皆改提案语义）。
	deposit := floorTo(qty, decOf(*q.MinAmount))
	if deposit < *q.MinAmount || (q.MaxAmount != nil && deposit > *q.MaxAmount) {
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"系统侧病灶：%s腿 depositAmount %v 界外（qty %.8f floor 至 dec%d；档 min %v max %v——不补不截如实败）",
			l.Name, deposit, qty, decOf(*q.MinAmount), amtTxt(q.MinAmount), amtTxt(q.MaxAmount))
	}
	now := e.Now()
	// 返修②：reqID 尾加尝试序号 n（同 proposal_id 该腿订单行数+1）——
	// 败单行不再 ON CONFLICT 吞续执、二次 go 同市况不再假「已受理」。
	attempt := 1
	if propID != nil {
		if c, cerr := e.Store.DCIOrderLegCount(ctx, *propID, l.OptType, l.InvestCcy); cerr == nil {
			attempt = c + 1
		}
	}
	reqID := fmt.Sprintf("dci|%s|%s|%s|%.8f|%d", l.Name, q.ProductRef, gate.DelivDay(q), deposit, attempt)
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: reqID, Venue: "binance", TemplateID: l.Tmpl,
		ProductRef: q.ProductRef, Underlying: "BTC", OptType: l.OptType, Strike: q.Strike,
		InvestCcy: q.InvestCcy, Qty: deposit, TenorDays: q.TenorDays, ProposalID: propID})
	if err != nil {
		return "", err
	}
	if !created {
		return fmt.Sprintf("%s腿已受理（request_id 防重）", l.Name), nil
	}
	// R14：申购前实时重取 orderId+canPurchase（DB order_ref 降格候选证据）。
	orderRef, rerr := e.refreshOrderRef(ctx, l, q.ProductRef)
	if rerr != nil {
		e.finish(ctx, oid, "failed", rerr.Error())
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"系统侧病灶：%s腿产品已下架（%s 重取：%v）", l.Name, q.ProductRef, rerr)
	}
	body, err := e.post(ctx, q.ProductRef, orderRef, deposit, now)
	if isRefreshErr(err) { // 「refresh」类拒因→一次再刷新重提（第二次如实 gap）。
		if orderRef, rerr = e.refreshOrderRef(ctx, l, q.ProductRef); rerr == nil {
			body, err = e.post(ctx, q.ProductRef, orderRef, deposit, now)
		}
	}
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, l, "permission", "dual_order",
				"请开通 binance 赚币/双币申购权限（%s腿下单失败：%v）", l.Name, err)
		}
		return "", e.gap(ctx, l, "runtime", "dual_order",
			"系统侧病灶：venue 拒单（%s腿：%v）", l.Name, err)
	}
	e.finish(ctx, oid, "filled", body)
	strike := 0.0
	if q.Strike != nil {
		strike = *q.Strike
	}
	note := fmt.Sprintf("DCI %s 申购回执：binance %s strike %.0f tenor %dd qty %.6f %s（单号 %s）",
		l.Name, q.ProductRef, strike, gate.DCITenor(q), qty, l.QtyCcy, reqID)
	opened := now
	asset := l.QtyCcy
	if _, err := e.Store.InsertPersonalPosition(ctx, asset, "other", asset+"USDT",
		note, &qty, &opened); err != nil {
		return "", fmt.Errorf("回执入账: %w", err)
	}
	// 成交终局回执升 warn——info 级被 feishu 滤噪闸拦截不推，
	// go/续执成交与败因回执必达飞书（级闸与 gap 推送径不动）。
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"DCI %s 申购成交：%.6f %s strike %.0f apy %.4g（单号 %s）",
		l.Name, qty, l.QtyCcy, strike, q.APY, reqID)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s腿申购已提交并入账：%.6f %s strike %.0f（%.4g 原生口径）",
		l.Name, qty, l.QtyCcy, strike, q.APY), nil
}
