// okx_dci.go —— okx DCI 执行径：sfp/dcd quote→trade 两段式（venue 实证
// 10-14 探针：quote→trade→order-status 链路通，扣款户=funding）。
// 资金户不足→补划 18→6 短差（earn rail 收口票同件）；quote validUntil 过期
// 如实败不重提；trade 限速 2/60s 天然慢拍。binance 径零触碰。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nox/internal/platform/errclass"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// tryLegOKX okx 单腿：合并余额×share→qty、达标档选取→幂等→funding 短差划转
// →dcd/quote→dcd/trade→order-status 回执→fin_dci_order venue=okx 同形落账。
func (e *Executor) tryLegOKX(ctx context.Context, l gate.DCILeg, share float64, tenorCap int, bound, spot float64, propID *int64) (string, error) {
	cred, ok := e.EarnCreds["okx"]
	if !ok || cred.Key == "" {
		return "", e.gap(ctx, l, "credential", "okx:dual",
			"请配置 okx 凭据（DCI 申购写径）")
	}
	rail := &okxEarnRail{e: e, c: cred}
	u, err := rail.unifiedAvail(ctx, l.QtyCcy)
	if err != nil {
		return "", e.gap(ctx, l, "runtime", "okx_balance",
			"系统侧病灶：okx unified %s 余额读败：%v", l.QtyCcy, err)
	}
	f, err := rail.fundingAvail(ctx, l.QtyCcy)
	if err != nil {
		return "", e.gap(ctx, l, "runtime", "okx_balance",
			"系统侧病灶：okx funding %s 余额读败：%v", l.QtyCcy, err)
	}
	qty := (u + f) * share
	if qty <= 0 {
		return fmt.Sprintf("%s腿跳过（okx %s 可动=0）", l.Name, l.QtyCcy), nil
	}
	qs, err := e.Store.ListDCIQuotes(ctx, "okx", "BTC", l.OptType, l.InvestCcy, tenorCap, bound)
	if err != nil {
		return "", err
	}
	var q *pgstore.CexDCIQuote
	for i := range qs {
		if !l.StrikePass(&qs[i], bound) || qs[i].ProductRef == "" {
			continue
		}
		q = &qs[i]
		break
	}
	if q == nil {
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"%s腿 okx 无达标档在售（spot %.0f，strike 界 %s）", l.Name, spot, l.BoundText(bound))
	}
	now := e.Now()
	// 幂等键同 binance 径：腿+产品+交割日+尝试序（金额浮点尾不入键）。
	intent := fmt.Sprintf("dci|okx|%s|%s|%s", l.Name, q.ProductRef, gate.DelivDay(q))
	if open, oerr := e.Store.DCIOrderOpenIntent(ctx, intent); oerr == nil && open {
		return fmt.Sprintf("%s腿已受理（同意图在途——幂等防重）", l.Name), nil
	}
	attempt := 1
	if c, cerr := e.Store.DCIOrderIntentCount(ctx, intent); cerr == nil {
		attempt = c + 1
	}
	if propID != nil {
		if c, cerr := e.Store.DCIOrderLegCount(ctx, *propID, l.OptType, l.InvestCcy); cerr == nil && c+1 > attempt {
			attempt = c + 1
		}
	}
	reqID := intent + fmt.Sprintf("|%d", attempt)
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: reqID, Venue: "okx", TemplateID: l.Tmpl,
		ProductRef: q.ProductRef, Underlying: "BTC", OptType: l.OptType, Strike: q.Strike,
		InvestCcy: q.InvestCcy, Qty: qty, TenorDays: q.TenorDays, ProposalID: propID})
	if err != nil {
		return "", err
	}
	if !created {
		return fmt.Sprintf("%s腿已受理（request_id 防重）", l.Name), nil
	}
	// funding 短差补划 18→6（sfp/dcd 扣款户=funding 探针实锤）；unified 无款
	// fail-closed 不试 venue（划转败/短差缺款均如实拒）。
	if needed := qty - f; needed > 0 {
		mv := needed
		if u < mv {
			mv = u
		}
		if mv <= 0 {
			e.finish(ctx, oid, "failed", "funding 短差且 unified 无款可划")
			return "", e.gap(ctx, l, "holding", l.QtyCcy,
				"okx 可动不足：qty %.6f、funding %.6f、unified %.6f", qty, f, u)
		}
		tb := fmt.Sprintf(`{"ccy":"%s","amt":"%s","from":"18","to":"6","type":"0"}`,
			l.QtyCcy, strconv.FormatFloat(mv, 'f', 8, 64))
		if _, terr := rail.req(ctx, http.MethodPost, "/api/v5/asset/transfer", tb); terr != nil {
			e.finish(ctx, oid, "failed", terr.Error())
			return "", e.gap(ctx, l, "runtime", "okx_transfer",
				"系统侧病灶：okx 18→6 划转败（%s腿不试申购）：%v", l.Name, terr)
		}
	}
	body, err := okxDCITrade(ctx, rail, q.ProductRef, qty, l.InvestCcy, e.Now())
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		sig := errclass.VenueRejectSignature(err.Error())
		if lerr := e.Store.LearnVenueReject(ctx, "okx", "dci_trade", sig, 24*time.Hour); lerr != nil {
			log.Printf("[dciexec] okx 坑表落行败: %v", lerr)
		}
		kb := e.kbSuffix(sig)
		return "", e.gap(ctx, l, "runtime", "dual_order",
			"系统侧病灶：venue 拒单（%s腿 okx：%v）%s", l.Name, err, kb)
	}
	e.finish(ctx, oid, "filled", body)
	strike := 0.0
	if q.Strike != nil {
		strike = *q.Strike
	}
	note := fmt.Sprintf("DCI %s 申购回执：okx %s strike %.0f tenor %dd qty %.6f %s（单号 %s）",
		l.Name, q.ProductRef, strike, gate.DCITenor(q), qty, l.QtyCcy, reqID)
	opened := now
	if _, err := e.Store.InsertPersonalPosition(ctx, l.QtyCcy, "other", l.QtyCcy+"USDT",
		note, &qty, &opened); err != nil {
		return "", fmt.Errorf("回执入账: %w", err)
	}
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"DCI %s 申购成交：okx %.6f %s strike %.0f apy %.4g（单号 %s）",
		l.Name, qty, l.QtyCcy, strike, q.APY, reqID)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s腿申购已提交并入账：okx %.6f %s strike %.0f（%.4g 原生口径）",
		l.Name, qty, l.QtyCcy, strike, q.APY), nil
}

// okxDCITrade quote→trade 两段式+order-status 回执（官方文档参数形：
// quote{productId,notionalSz,notionalCcy}→{quoteId,validUntil(ms)}；
// trade{quoteId}→data[{ordId,...}]；status GET order-status?ordId=）。
// validUntil 过期如实败不重提（报价值已死，重报价=另一次撮合决定）。
func okxDCITrade(ctx context.Context, rail *okxEarnRail, productID string, qty float64, notionalCcy string, now time.Time) (string, error) {
	qb := fmt.Sprintf(`{"productId":"%s","notionalSz":"%s","notionalCcy":"%s"}`,
		productID, strconv.FormatFloat(qty, 'f', 8, 64), notionalCcy)
	qraw, err := rail.req(ctx, http.MethodPost, "/api/v5/finance/sfp/dcd/quote", qb)
	if err != nil {
		return "", fmt.Errorf("dcd quote: %w", err)
	}
	var qrows []struct {
		QuoteID    string `json:"quoteId"`
		ValidUntil string `json:"validUntil"`
	}
	if err := json.Unmarshal(qraw, &qrows); err != nil || len(qrows) == 0 || qrows[0].QuoteID == "" {
		return "", fmt.Errorf("dcd quote 回执解析（quoteId 缺）: %v %s", err, string(qraw))
	}
	if ms, perr := strconv.ParseInt(qrows[0].ValidUntil, 10, 64); perr == nil &&
		!now.Before(time.UnixMilli(ms)) {
		return "", fmt.Errorf("dcd 报价已过期（validUntil=%s 不重用）", qrows[0].ValidUntil)
	}
	tb := fmt.Sprintf(`{"quoteId":"%s"}`, qrows[0].QuoteID)
	traw, err := rail.req(ctx, http.MethodPost, "/api/v5/finance/sfp/dcd/trade", tb)
	if err != nil {
		return "", fmt.Errorf("dcd trade: %w", err)
	}
	// order-status 回执拼接（ordId 缺=回执原文如实留——不为读单臆造参）。
	var trows []struct {
		OrdID string `json:"ordId"`
	}
	if json.Unmarshal(traw, &trows) == nil && len(trows) > 0 && trows[0].OrdID != "" {
		if sraw, serr := rail.req(ctx, http.MethodGet,
			"/api/v5/finance/sfp/dcd/order-status?ordId="+trows[0].OrdID, ""); serr == nil {
			return strings.Join([]string{string(traw), string(sraw)}, "|status:"), nil
		}
	}
	return string(traw), nil
}
