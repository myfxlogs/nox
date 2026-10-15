// tryleg.go —— D-191：单腿申购执行（自 exec.go 拆分，纯移动零语义改）。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/platform/errclass"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// tryLeg 单腿：池余额×share→qty、spot 不足→活期赎回缺口→重读、候选档 strike 闸
// →最优、幂等→签名 POST→回执（R6 前置 redeem 步）。
func (e *Executor) tryLeg(ctx context.Context, l gate.DCILeg, share float64, tenorCap int, bound, spot, sigma float64, propID *int64) (string, error) {
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
			kb := e.kbSuffix(errclass.VenueRejectSignature(rerr.Error()))
			if venueErrKind(rerr) == "permission" {
				return "", e.gap(ctx, l, "permission", "earn_redeem",
					"请开通 binance 赚币活期赎回权限（%s 赎回败：%v）%s", l.QtyCcy, rerr, kb)
			}
			return "", e.gap(ctx, l, "runtime", "earn_redeem",
				"系统侧病灶：%s 活期赎回败：%v%s", l.QtyCcy, rerr, kb)
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
	// D-526③：选档目标=net(premium_ann−tail_cost_ann) 最优（APY 最优但
	// 尾损盖溢价的档如实拒；σ/界/尾损三要素随拒因可审计）。
	var q *pgstore.CexDCIQuote
	var sawStrikeOK bool
	var bestNet, bestTail, bestPrem float64
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
		tenor := tenorCap
		if qs[i].TenorDays != nil && *qs[i].TenorDays > 0 {
			tenor = *qs[i].TenorDays
		}
		prem := qs[i].APY / 100 // binance apy=%口径归一小数
		tail := gate.TailCostAnn(l.OptType, spot, *qs[i].Strike, sigma, tenor)
		if net := prem - tail; q == nil || net > bestNet {
			q, bestNet, bestTail, bestPrem = &qs[i], net, tail, prem
		}
	}
	if q == nil {
		if sawStrikeOK {
			return "", e.gap(ctx, l, "runtime", "dual_quote",
				"系统侧病灶：%s腿无可申购参数档（达标档缺 order_ref/min_amount——采集面未落）", l.Name)
		}
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"%s腿无达标档在售（spot %.0f，strike 界 %s）", l.Name, spot, l.BoundText(bound))
	}
	if bestNet <= 0 {
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"%s腿尾损盖溢价：net %.2f%%=prem %.2f%%−tail %.2f%%（σ=%.2f%% 界=%.0f strike=%.0f）——如实拒",
			l.Name, bestNet*100, bestPrem*100, bestTail*100, sigma*100, bound, *q.Strike)
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
	// D-255③：reqID 意图稳定键（腿+产品+交割日+尝试序）——金额浮点尾不入键
	// （原 %.8f deposit 入键：金额微差即换键破幂等，同意图反复打 venue）；
	// 尝试序=同意图前缀行数+1（proposal_id 无关——wheel 径 propID=nil 也推进，
	// 败单行不再撞键假「已受理」）。
	intent := fmt.Sprintf("dci|%s|%s|%s", l.Name, q.ProductRef, gate.DelivDay(q))
	// 同意图在途/已成（pending|filled|settled）→幂等挡重发「已受理」；
	// failed|rejected 不挡——续执天然走新尝试号。
	if open, oerr := e.Store.DCIOrderOpenIntent(ctx, intent); oerr == nil && open {
		return fmt.Sprintf("%s腿已受理（同意图在途——幂等防重）", l.Name), nil
	}
	attempt := 1
	if c, cerr := e.Store.DCIOrderIntentCount(ctx, intent); cerr == nil {
		attempt = c + 1
	}
	if propID != nil {
		if c, cerr := e.Store.DCIOrderLegCount(ctx, *propID, l.OptType, l.InvestCcy); cerr == nil && c+1 > attempt {
			attempt = c + 1 // 提案绑腿序兜底（历史行 reqID 无前缀时期）
		}
	}
	reqID := intent + fmt.Sprintf("|%d", attempt)
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
	// D-255①②：申购前查坑——active suppress/critical→零 venue 打面抑该腿
	// （rejected 行留痕+一次性 critical dedup 告警，不逐轮刷）；fix→参数映射
	// 自修复（如 deposit_decimals）再打面。坑表读败=fail-closed 拒单。
	pits, perr := e.Store.ListVenueRejects(ctx, "binance", "dci_subscribe")
	if perr != nil {
		e.finish(ctx, oid, "rejected", "坑表读败")
		return "", e.gap(ctx, l, "runtime", "dual_order",
			"系统侧病灶：%s腿申购前坑表读败（fail-closed 不打 venue）：%v", l.Name, perr)
	}
	for i := range pits {
		p := &pits[i]
		if !p.Active(now) {
			continue
		}
		if p.Action == "fix" {
			deposit = applyRejectFix(deposit, p.ActionParams)
			continue
		}
		// suppress/critical：零 venue 打面，行收口 rejected+一次性 critical。
		e.finish(ctx, oid, "rejected", "坑表抑单:"+p.ErrorSignature)
		if aerr := e.Store.InsertAlertDedup(ctx, "critical", fmt.Sprintf(
			"venue 坑表抑单 binance:dci_subscribe:%s（%s腿 hits=%d expires=%s）——同败因不重复打面",
			p.ErrorSignature, l.Name, p.Hits, pitExpiry(p)), 24*time.Hour); aerr != nil {
			log.Printf("[dciexec] 坑表抑单 alert: %v", aerr)
		}
		return "", fmt.Errorf("%s腿坑表抑单（%s 败因窗内——%s）", l.Name, p.ErrorSignature, pitExpiry(p))
	}
	body, err := e.post(ctx, q.ProductRef, orderRef, deposit, now)
	if isRefreshErr(err) { // 「refresh」类拒因→一次再刷新重提（第二次如实 gap）。
		if orderRef, rerr = e.refreshOrderRef(ctx, l, q.ProductRef); rerr == nil {
			body, err = e.post(ctx, q.ProductRef, orderRef, deposit, now)
		}
	}
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		// D-255①：败因自动落坑——二次触发零 venue 打面（suppress 窗 24h）；
		// refresh 类瞬态（orderId 过期已重提一次）不落坑——落 suppress 误封 24h。
		sig := errclass.VenueRejectSignature(err.Error())
		if !isRefreshErr(err) {
			if lerr := e.Store.LearnVenueReject(ctx, "binance", "dci_subscribe",
				sig, 24*time.Hour); lerr != nil {
				log.Printf("[dciexec] 坑表落行败: %v", lerr)
			}
		}
		kb := e.kbSuffix(sig) // 拒单消费闸：坑签名命中经验库→教训贴告警脸
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, l, "permission", "dual_order",
				"请开通 binance 赚币/双币申购权限（%s腿下单失败：%v）%s", l.Name, err, kb)
		}
		return "", e.gap(ctx, l, "runtime", "dual_order",
			"系统侧病灶：venue 拒单（%s腿：%v）%s", l.Name, err, kb)
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
