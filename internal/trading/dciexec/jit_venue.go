// jit_venue.go —— jit 划转执行件 venue 分派层（jit.go 拆出件，行数门禁）：
// 钱包名映射+三所划转写径（okx asset/transfer / gate wallet/transfers /
// binance sapi asset/transfer）+交易户可动读（okx unified / gate futures /
// binance fapi balance）。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"nox/internal/platform/httpx"
)

// jitFundingWallet earn 赎落钱包名（io 记账口径——okx 赎落 funding 户，
// binance/gate 赎落 spot 户）。
func jitFundingWallet(venue string) string {
	if venue == "okx" {
		return "funding"
	}
	return "spot"
}

// jitTradingWallet 腿池钱包名（venueType perp 域分派；spot/空型=免二跳回 ""——
// okx perp 腿池=unified 交易户 18、gate=futures、binance=UMFUTURE）。
func jitTradingWallet(venue, vt string) string {
	if vt != "perp" && vt != "unified" && vt != "futures" {
		return ""
	}
	return "trading"
}

// jitWalletTransfer 交易户↔资金/现货户划转写径（三所单函数分派——端点异而
// 语义同；venue 白名单已在前置闸，未知所到不了这）。
func (e *Executor) jitWalletTransfer(ctx context.Context, venue, ccy string, amt float64, from, to string) error {
	switch venue {
	case "okx":
		r, ok := e.earnRail("okx")
		if !ok {
			return fmt.Errorf("okx rail 缺席")
		}
		or, ok := r.(*okxEarnRail)
		if !ok {
			return fmt.Errorf("okx rail 注入面不支持划转写")
		}
		// okx 账户枚举：6=funding，18=trading（unified）。
		num := map[string]string{"funding": "6", "trading": "18"}
		body := fmt.Sprintf(`{"ccy":"%s","amt":"%s","from":"%s","to":"%s","type":"0"}`,
			ccy, strconv.FormatFloat(amt, 'f', 8, 64), num[from], num[to])
		_, err := or.req(ctx, http.MethodPost, "/api/v5/asset/transfer", body)
		return err
	case "gate":
		r, ok := e.earnRail("gate")
		if !ok {
			return fmt.Errorf("gate rail 缺席")
		}
		gr, ok := r.(*gateEarnRail)
		if !ok {
			return fmt.Errorf("gate rail 注入面不支持划转写")
		}
		// gate 账户枚举：spot↔futures（perp 池=USDT 结）。
		num := map[string]string{"spot": "spot", "trading": "futures"}
		body := fmt.Sprintf(`{"currency":"%s","from":"%s","to":"%s","amount":"%s"}`,
			ccy, num[from], num[to], strconv.FormatFloat(amt, 'f', 8, 64))
		_, err := gr.req(ctx, http.MethodPost, "/wallet/transfers", "", body)
		return err
	case "binance":
		// MAIN_UMFUTURE=spot→UM 合约、UMFUTURE_MAIN=回转（venue 枚举名直传）。
		dir := map[string]string{"spot:trading": "MAIN_UMFUTURE", "trading:spot": "UMFUTURE_MAIN"}
		typ, ok := dir[from+":"+to]
		if !ok {
			return fmt.Errorf("binance 划转方向 %s→%s 未枚举", from, to)
		}
		q := fmt.Sprintf("type=%s&asset=%s&amount=%s&timestamp=%d&recvWindow=5000",
			typ, ccy, strconv.FormatFloat(amt, 'f', 8, 64), e.nowOr().UnixMilli())
		url := fmt.Sprintf("%s/v1/asset/transfer?%s&signature=%s",
			strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-MBX-APIKEY", e.BnKey)
		resp, err := e.client().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("binance transfer HTTP %d: %s", resp.StatusCode, string(b))
		}
		return nil
	}
	return fmt.Errorf("jit transfer venue %s 未支", venue)
}

// tradingFree 交易户 USDT 可动实读（回转上限面）——测试注 TradingFreeFn；
// nil=venue 实读（okx unified availBal / gate futures accounts available /
// binance fapi v2 balance availableBalance）。
func (e *Executor) tradingFree(ctx context.Context, venue, vt string) (float64, error) {
	if e.TradingFreeFn != nil {
		return e.TradingFreeFn(ctx, venue, vt)
	}
	switch venue {
	case "okx":
		r, ok := e.earnRail("okx")
		if !ok {
			return 0, fmt.Errorf("okx rail 缺席")
		}
		or, ok := r.(*okxEarnRail)
		if !ok {
			return 0, fmt.Errorf("okx rail 注入面不支持可动读")
		}
		return or.unifiedAvail(ctx, jitCcy)
	case "gate":
		r, ok := e.earnRail("gate")
		if !ok {
			return 0, fmt.Errorf("gate rail 缺席")
		}
		gr, ok := r.(*gateEarnRail)
		if !ok {
			return 0, fmt.Errorf("gate rail 注入面不支持可动读")
		}
		b, err := gr.req(ctx, http.MethodGet, "/futures/usdt/accounts", "", "")
		if err != nil {
			return 0, err
		}
		var row struct {
			Available string `json:"available"`
		}
		if err := json.Unmarshal(b, &row); err != nil {
			return 0, fmt.Errorf("gate futures accounts 解析: %w", err)
		}
		return parseF(row.Available), nil
	case "binance":
		fapi := httpx.BinanceApiSegBase(e.APIBase, "fapi")
		q := fmt.Sprintf("timestamp=%d&recvWindow=5000", e.nowOr().UnixMilli())
		b, err := e.get(ctx, fmt.Sprintf("%s/v2/balance?%s&signature=%s",
			strings.TrimRight(fapi, "/"), q, sign(e.BnSec, q)), true)
		if err != nil {
			return 0, err
		}
		var rows []struct {
			Asset     string `json:"asset"`
			Available string `json:"availableBalance"`
		}
		if err := json.Unmarshal(b, &rows); err != nil {
			return 0, fmt.Errorf("binance fapi balance 解析: %w", err)
		}
		for _, r := range rows {
			if r.Asset == jitCcy {
				return parseF(r.Available), nil
			}
		}
		return 0, nil
	}
	return 0, fmt.Errorf("trading free venue %s 未支", venue)
}
