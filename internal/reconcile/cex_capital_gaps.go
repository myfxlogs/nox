// cex_capital_gaps.go —— D-546 S1：binance 资金全景双缺口采集（币本位/期权权益）。
// D-133 R1：理财面（simple-earn/account）摘除——业主令理财账户实有杂币「只统计 USDT」
// 不可达→earn 面整体不纳入系统。
// 只读全景面：进 CapitalMap 树，不进 sizing 基准（A3 基准不变锚——sizing 仍走 D-542 活账本流）。
// 端点 2026-09-28 官方文档逐一核实（设计稿草拟端点号按核实结果修正，施工前必核纪律）：
//
//	① GET {dapi}/v1/balance — [].asset/balance/availableBalance（币本位）
//	② GET {eapi}/v1/account — asset[].equity（期权权益；设计稿草拟 /eapi/v1/equity 无此端点，弃用）
//
// 签名链复用 binanceSign + X-MBX-APIKEY（零新客户端）；代理 base 派生同 /api 段替换。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/httpx"
)

// CoinMBalance 币本位单币余额（dapi/v1/balance 行）。
type CoinMBalance struct {
	Asset     string
	Balance   float64
	Available float64
}

// OptionsBalance 期权单币权益（eapi/v1/account asset[] 行；只取权益额，不收 greeks）。
type OptionsBalance struct {
	Asset     string
	Equity    float64
	Available float64
}

// CapitalGaps 双缺口全景面（一次 Fetch=2 REST；单面失败不拖垮其余面——Errors 如实留痕，
// 树面按 Errors 标注降级；全部失败才返回 error）。
type CapitalGaps struct {
	CoinM     []CoinMBalance
	Options   []OptionsBalance
	Errors    []string  // 各面失败原因（顺序=coinm/options）
	FetchedAt time.Time // 实拉时点
}

// BinanceCapitalGaps 双缺口实拉器（无状态；缓存归 dashboard 进程层 60s）。
type BinanceCapitalGaps struct {
	client           *http.Client
	key              string
	secret           string
	dapiURL, eapiURL string
}

// NewBinanceCapitalGaps 从 spot 风格 base 派生双 base（/api 段替换，earn 采集器同款约定；
// 代理形 https://cex.xxx/api 与直连形 https://api.binance.com 均覆盖）。
func NewBinanceCapitalGaps(spotBase, apiKey, apiSecret string) *BinanceCapitalGaps {
	return &BinanceCapitalGaps{
		client: httpx.Client(), key: apiKey, secret: apiSecret,
		dapiURL: httpx.BinanceApiSegBase(spotBase, "dapi"),
		eapiURL: httpx.BinanceApiSegBase(spotBase, "eapi"),
	}
}

// signedGet 签名 GET（timestamp+recvWindow+HMAC signature query + X-MBX-APIKEY 头）。
func (g *BinanceCapitalGaps) signedGet(ctx context.Context, base, path string) ([]byte, error) {
	query := fmt.Sprintf("timestamp=%d&recvWindow=5000", time.Now().UnixMilli())
	sign := binanceSign(g.secret, query)
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s%s?%s&signature=%s", base, path, query, sign), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", g.key)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// Fetch 双面实拉（顺序 coinm→options；单面败记 Errors 继续）。
func (g *BinanceCapitalGaps) Fetch(ctx context.Context) (*CapitalGaps, error) {
	out := &CapitalGaps{FetchedAt: time.Now()}
	failed := 0
	if err := g.fetchCoinM(ctx, out); err != nil {
		out.Errors = append(out.Errors, "币本位面: "+err.Error())
		failed++
	}
	if err := g.fetchOptions(ctx, out); err != nil {
		out.Errors = append(out.Errors, "期权面: "+err.Error())
		failed++
	}
	if failed == 2 {
		return out, fmt.Errorf("binance capital gaps: 双面全败: %s", strings.Join(out.Errors, "; "))
	}
	return out, nil
}

// fetchCoinM ①币本位面（dapi/v1/balance；零余额行不入树面）。
func (g *BinanceCapitalGaps) fetchCoinM(ctx context.Context, out *CapitalGaps) error {
	body, err := g.signedGet(ctx, g.dapiURL, "/v1/balance")
	if err != nil {
		return fmt.Errorf("dapi/v1/balance: %w", err)
	}
	var resp []struct {
		Asset            string `json:"asset"`
		Balance          string `json:"balance"`
		AvailableBalance string `json:"availableBalance"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	for _, r := range resp {
		bal, err := strconv.ParseFloat(r.Balance, 64)
		if err != nil {
			return fmt.Errorf("parse balance %q: %w", r.Balance, err)
		}
		if bal == 0 {
			continue
		}
		avail, err := strconv.ParseFloat(r.AvailableBalance, 64)
		if err != nil {
			return fmt.Errorf("parse availableBalance %q: %w", r.AvailableBalance, err)
		}
		out.CoinM = append(out.CoinM, CoinMBalance{Asset: r.Asset, Balance: bal, Available: avail})
	}
	return nil
}

// fetchOptions ②期权权益面（eapi/v1/account asset[]；零权益行不入树面）。
func (g *BinanceCapitalGaps) fetchOptions(ctx context.Context, out *CapitalGaps) error {
	body, err := g.signedGet(ctx, g.eapiURL, "/v1/account")
	if err != nil {
		return fmt.Errorf("eapi/v1/account: %w", err)
	}
	var resp struct {
		Asset []struct {
			Asset     string `json:"asset"`
			Equity    string `json:"equity"`
			Available string `json:"available"`
		} `json:"asset"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	for _, r := range resp.Asset {
		eq, err := strconv.ParseFloat(r.Equity, 64)
		if err != nil {
			return fmt.Errorf("parse equity %q: %w", r.Equity, err)
		}
		if eq == 0 {
			continue
		}
		avail, err := strconv.ParseFloat(r.Available, 64)
		if err != nil {
			return fmt.Errorf("parse available %q: %w", r.Available, err)
		}
		out.Options = append(out.Options, OptionsBalance{Asset: r.Asset, Equity: eq, Available: avail})
	}
	return nil
}
