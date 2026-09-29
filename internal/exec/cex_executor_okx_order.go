// cex_executor_okx_order.go —— OKX 下单主体（D-566 自 cex_executor.go 拆出消 >300 线）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// placeOKX 下单：POST /api/v5/trade/order（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret,
// ts+method+path+body))）。spot tdMode=cash；perp tdMode=cross（代码保留，本次不验证）。
func (e *CEXExecutor) placeOKX(ctx context.Context, o Order) (Fill, error) {
	side := "buy"
	if o.Side == "SELL" {
		side = "sell"
	}
	tdMode := "cash" // spot
	instID := o.Symbol
	if e.venueType == "perp" {
		tdMode = "cross"
		if !strings.HasSuffix(instID, "-SWAP") {
			instID = instID + "-SWAP" // OKX perp instId 形如 BTC-USDT-SWAP
		}
		// D-564 S6 U8/U9：posMode 判定时刻实读（非 long_short_mode 拒）+instruments.state 闸。
		if err := e.okxPosModeCheck(ctx); err != nil {
			return Fill{}, err
		}
		if err := e.okxStateCheck(ctx, instID); err != nil {
			return Fill{}, err
		}
	}
	var sz string // perp/spot 两分支各自赋值（D-516：初值 dead write 清 staticcheck）
	var bodyStr string
	var ctVal float64 // D-125：okx perp 合约面值（perp 分支赋值，Fill 处用）
	if e.venueType == "perp" {
		// D-125：okx swap sz 单位=张数，o.Qty 是基础币数量 → sz=qty/ctVal。
		// 如 TRX-USDT-SWAP ctVal=1000 → 2964 TRX = 2.964 张。不换算则 2964 张=2964000 TRX，
		// 名义价值~$100万，保证金需求远超账户余额 → 51008 Insufficient margin 拒单 → LEGGED。
		var lotSz, maxMktSz float64
		var err error
		ctVal, lotSz, maxMktSz, err = e.okxContractVal(ctx, instID)
		if err != nil {
			return Fill{}, fmt.Errorf("okx contract spec: %w", err)
		}
		szContracts := o.Qty / ctVal
		// sz 必须是 lotSz 的倍数（okx 51121 Order quantity must be a multiple of the lot size）。
		// 向下取整到 lotSz 倍数（不开过量仓位），如 2.964/0.01=296.4 → 296×0.01=2.96 张。
		lots := math.Floor(szContracts / lotSz)
		szContracts = lots * lotSz
		if szContracts < lotSz { // 最小 1 个 lot
			return Fill{}, fmt.Errorf("okx qty %.6f / ctVal %.6f = %.6f 张 < lotSz %.6f（最小张数）", o.Qty, ctVal, o.Qty/ctVal, lotSz)
		}
		// D-566 S1 市价单上限闸：张数 vs maxMktSz 同域比较（KB ctval-qty-conversion）；
		// 0=无闸直通（instruments 缺席非证据）。
		if maxMktSz > 0 && szContracts > maxMktSz {
			return Fill{}, fmt.Errorf("okx qty %.4f 张 > maxMktSz %.4f（市价单笔上限）fail-closed", szContracts, maxMktSz)
		}
		sz = strconv.FormatFloat(szContracts, 'f', -1, 64)
		// swap/futures 必须带 posSide（D-059 实施修正② 实测 51000）。本账户 posMode=long_short_mode
		// （account/config 实测）。开仓传空 → buy=long / sell=short 派生；平仓由调用方显式传
		// PositionSide=原持仓侧（BUY 开 long → SELL 平 + posSide=long 才减 long，D-059 实测
		// SELL+posSide=short 会新开空仓）。net_mode 账户需改 posSide=net。
		// D-108 P2 T1：平仓补 reduceOnly=true（防开反向敞口）。
		// D-120 修复：开仓（PositionSide=""）不带 reduceOnly——开仓带 reduceOnly 会被拒
		// （Binance -2022 ReduceOnly rejected / OKX 资金计算异常 Insufficient margin）。
		posSide := o.PositionSide
		if posSide == "" {
			posSide = "long"
			if side == "sell" {
				posSide = "short"
			}
		}
		isClose := o.PositionSide != ""
		// D-195-B §4.3：client_order_id 透传（OKX 支持 clOrdId，32 字符限制）。
		clOrdID := ""
		if o.ClientOrderID != "" {
			clOrdID = fmt.Sprintf(`,"clOrdId":%q`, o.ClientOrderID)
		}
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"posSide":%q,"reduceOnly":%t%s}`,
			instID, tdMode, side, sz, posSide, isClose, clOrdID)
	} else {
		// spot 市价单：缺省买方向 sz=报价币金额（0.0001 当 0.0001 USDT → 51020 最小金额，实测）。
		// 显式 tgtCcy=base_ccy 固定 sz=基础币数量，与 exec.Order.Qty 契约一致。
		// D-473（KB okx-perp-ctval-qty-conversion 同型）：spot sz 同样须 lotSz 倍数
		// 且 ≥minSz——取不到/不足 → fail-closed 拒单（不指望 venue 报错兜底）。
		lotSz, minSz, maxMktAmt, err := e.okxSpotLotSz(ctx, instID)
		if err != nil {
			return Fill{}, fmt.Errorf("okx spot spec: %w", err)
		}
		qty := math.Floor(o.Qty/lotSz) * lotSz
		if qty < minSz || qty < lotSz {
			return Fill{}, fmt.Errorf("okx spot qty %.6f → lotSz 地板 %.6f < minSz %.6f（最小量）", o.Qty, qty, minSz)
		}
		// D-566 S1 spot 名义上限闸：maxMktAmt>0 时才拉公开 ticker 折算（0=无闸直通零开销）；
		// 取价失败 fail-closed 不猜价（与 spec 取败同语义）。
		if maxMktAmt > 0 {
			tkr, terr := e.get(ctx, e.baseURL+"/api/v5/market/ticker?instId="+url.QueryEscape(instID), nil)
			if terr != nil {
				return Fill{}, fmt.Errorf("okx spot ticker %s 取价失败（maxMktAmt 闸需现价）fail-closed: %w", instID, terr)
			}
			var tj struct {
				Data []struct {
					Last string `json:"last"`
				} `json:"data"`
			}
			if jerr := json.Unmarshal(tkr, &tj); jerr != nil || len(tj.Data) == 0 {
				return Fill{}, fmt.Errorf("okx spot ticker %s: len=%d err=%v fail-closed", instID, len(tj.Data), jerr)
			}
			last, lerr := f64z(tj.Data[0].Last)
			if lerr != nil || last <= 0 {
				return Fill{}, fmt.Errorf("okx spot ticker %s last=%q invalid fail-closed", instID, tj.Data[0].Last)
			}
			if qty*last > maxMktAmt {
				return Fill{}, fmt.Errorf("okx spot qty %.6f × last %.4f = 名义 %.4f > maxMktAmt %.4f（市价单笔上限）fail-closed", qty, last, qty*last, maxMktAmt)
			}
		}
		sz = strconv.FormatFloat(qty, 'f', -1, 64)
		// D-195-B §4.3：client_order_id 透传（OKX 支持 clOrdId）。
		clOrdID := ""
		if o.ClientOrderID != "" {
			clOrdID = fmt.Sprintf(`,"clOrdId":%q`, o.ClientOrderID)
		}
		// D-566 S2：banAmend=true——余额不足时整笔拒绝而非静默减量（审计 F2：静默减量=双腿漂移）。
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"tgtCcy":"base_ccy","banAmend":true%s}`,
			instID, tdMode, side, sz, clOrdID)
	}

	path := "/api/v5/trade/order"
	// D-566 S3：expTime=UTC now+15s（下单请求有时效，cancel 不带）；50102 走类型化错误判定——
	// 校时一次 + 同 body 重签重发一次（body 不动，时间戳/签名全新；无重试循环）。
	send := func() ([]byte, error) {
		ts := e.okxNowISO()
		headers := http.Header{
			"OK-ACCESS-KEY":        {e.apiKey},
			"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"POST"+path+bodyStr)},
			"OK-ACCESS-TIMESTAMP":  {ts},
			"OK-ACCESS-PASSPHRASE": {e.passphrase},
			"Content-Type":         {"application/json"},
			"expTime":              {strconv.FormatInt(time.Now().Add(15*time.Second).UnixMilli(), 10)}, // 官方契约=毫秒 Unix 时间戳
		}
		return e.post(ctx, e.baseURL+path, bodyStr, headers)
	}
	body, err := send()
	if okxIs50102(err, body) {
		if serr := e.okxSyncClock(ctx); serr != nil {
			e.alertCritical(o, fmt.Sprintf("okx 订单校时失败（错误码 50102）持仓#%d %s：%v", o.PosID, o.Symbol, serr))
			return Fill{}, fmt.Errorf("okx order 50102 校时失败: %w", serr)
		}
		body, err = send() // 一次性重发（再遇 50102 落错误上抛）
	}
	if err != nil {
		e.alertCritical(o, fmt.Sprintf("okx 平仓单被交易所拒绝（持仓#%d %s %s）：%v", o.PosID, o.Symbol, side, err))
		return Fill{}, fmt.Errorf("okx order: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID     string `json:"ordId"`
			SCode     string `json:"sCode"`
			SMsg      string `json:"sMsg"`
			AvgPx     string `json:"avgPx"`
			AccFillSz string `json:"accFillSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Fill{}, fmt.Errorf("okx order json: %w", err)
	}
	if resp.Code != "0" {
		// code=1「All operations failed」是包装错误，真实原因在 data[0].sCode/sMsg（D-059 实测）。
		detail := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			detail = fmt.Sprintf("%s (sCode=%s sMsg=%s)", resp.Msg, resp.Data[0].SCode, resp.Data[0].SMsg)
		}
		e.alertCritical(o, fmt.Sprintf("okx 平仓单被交易所拒绝（持仓#%d 错误码 %s）：%s", o.PosID, resp.Code, detail))
		return Fill{}, fmt.Errorf("okx order: code=%s msg=%s", resp.Code, detail)
	}
	if len(resp.Data) == 0 {
		return Fill{}, fmt.Errorf("okx order: empty data")
	}
	d := resp.Data[0]
	if d.SCode != "" && d.SCode != "0" {
		return Fill{}, fmt.Errorf("okx order: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}
	qty, err := f64z(d.AccFillSz)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order %s accFillSz=%q: %w", d.OrdID, d.AccFillSz, err)
	}
	avgPx, err := f64z(d.AvgPx)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order %s avgPx=%q: %w", d.OrdID, d.AvgPx, err)
	}
	// D-125：okx perp accFillSz 是张数，转回基础币数量 = 张数 × ctVal。
	if e.venueType == "perp" {
		qty *= ctVal
	}
	if qty <= 0 || avgPx <= 0 {
		// 市价单 POST 响应可能先于撮合返回（accFillSz=0/avgPx 空，D-059 实施修正② 实测：
		// 订单实际成交但响应无成交数据）→ 退避轮询 ≥3s；窗尽 cancel+终查双证；
		// 仍不可判 → ErrFillUnconfirmed（D-421：受理未确认≠确定未成交，禁映 FAILED）。
		return e.okxConfirmFill(ctx, o, instID, d.OrdID, ctVal)
	}
	feeUSD := e.okxOrderFeeUSD(ctx, instID, d.OrdID) // D-391 S2：补查 fee（fail-open 0→NULL）
	return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, FeeUSD: feeUSD, Ts: time.Now()}, nil
}
