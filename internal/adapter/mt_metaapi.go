// Package adapter 的 mt_metaapi.go 实现 MetaApi 只读备胎骨架（D-146 S2）。
//
// 背景：mtapi.io 服务商级 SPOF（入口冗余已有 mt5grpc1-4，服务商整体故障无备）。
// 第一版只做「行情+账户+持仓的只读备份通道」——mtapi.io 故障时至少看得见实盘仓位
// （可见性优先于可操作性）。下单切换（状态同步/幂等复杂度）留二期。
//
// 装配开关 ARB_MT_FAILOVER_METAAPI（默认关）；联调等业主 token（注册 metaapi.cloud →
// API token 入 .env ARB_METAAPI_TOKEN → 3 实盘账户注册进 MetaApi）。
// 故障时 dashboard 的 broker 持仓视图回落备胎数据源（仅 3 实盘账户，demo 8 家不接省订阅费）。
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrMetaApiNotConfigured 未配置 token（开关关/空 token）——调用方据此跳过备胎。
var ErrMetaApiNotConfigured = errors.New("metaapi: not configured (token empty or failover disabled)")

// MetaApiAccountSummary 备胎账户摘要（与 reconcile.AccountInfo 同构字段，便于将来回落接入）。
type MetaApiAccountSummary struct {
	Balance    float64
	Equity     float64
	Margin     float64
	FreeMargin float64
	Credit     float64
	Profit     float64
	Leverage   float64
	Currency   string
}

// MetaApiOrder 备胎持仓记录（与 reconcile.VenueOrder 同构字段）。
type MetaApiOrder struct {
	Ticket    string
	Symbol    string
	Side      string // BUY / SELL
	Volume    float64
	OpenPrice float64
	Profit    float64
	Swap      float64
	OpenedAt  time.Time
}

// MetaApiQuote 备胎行情快照。
type MetaApiQuote struct {
	Symbol string
	Bid    float64
	Ask    float64
	Time   time.Time
}

// MetaApiAdapter MetaApi.cloud REST 只读备胎客户端（D-146 S2，骨架级，默认关）。
//
// 与 MT5Adapter 的查询面同构（AccountSummary/持仓/行情快照），但走 REST 而非 gRPC。
// 下单切换留二期——本结构体只实现只读方法，不实现 PlaceOrder/CloseOrder。
type MetaApiAdapter struct {
	venue   string
	token   string
	baseURL string // 默认 https://api.metaapi.cloud；测试覆写
	client  *http.Client
}

// NewMetaApiAdapter 创建 MetaApi 备胎适配器。token 空 = 未配置（Enabled()=false）。
func NewMetaApiAdapter(venue, token string) *MetaApiAdapter {
	return &MetaApiAdapter{
		venue:   venue,
		token:   token,
		baseURL: "https://api.metaapi.cloud",
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled 是否启用（token 非空即视为配置就绪；装配开关由 main 在创建前判断）。
func (a *MetaApiAdapter) Enabled() bool { return a.token != "" }

// Venue 返回场所标识。
func (a *MetaApiAdapter) Venue() string { return a.venue }

// AccountSummary 查询账户摘要（GET /users/{accountId}/accountInformation）。
// accountId 当前用 venue 占位——联调时业主提供 MetaApi 注册的 accountId 映射。
func (a *MetaApiAdapter) AccountSummary(ctx context.Context) (*MetaApiAccountSummary, error) {
	if !a.Enabled() {
		return nil, ErrMetaApiNotConfigured
	}
	var raw struct {
		Balance    float64 `json:"balance"`
		Equity     float64 `json:"equity"`
		Margin     float64 `json:"margin"`
		FreeMargin float64 `json:"freeMargin"`
		Credit     float64 `json:"credit"`
		Profit     float64 `json:"profit"`
		Leverage   float64 `json:"leverage"`
		Currency   string  `json:"currency"`
	}
	if err := a.getJSON(ctx, "/users/"+a.venue+"/accountInformation", &raw); err != nil {
		return nil, fmt.Errorf("metaapi account summary: %w", err)
	}
	return &MetaApiAccountSummary{
		Balance:    raw.Balance,
		Equity:     raw.Equity,
		Margin:     raw.Margin,
		FreeMargin: raw.FreeMargin,
		Credit:     raw.Credit,
		Profit:     raw.Profit,
		Leverage:   raw.Leverage,
		Currency:   raw.Currency,
	}, nil
}

// OpenedOrders 查询持仓列表（GET /users/{accountId}/positions）。
func (a *MetaApiAdapter) OpenedOrders(ctx context.Context) ([]MetaApiOrder, error) {
	if !a.Enabled() {
		return nil, ErrMetaApiNotConfigured
	}
	var raw []struct {
		ID        string  `json:"id"`
		Symbol    string  `json:"symbol"`
		Type      string  `json:"type"`
		Volume    float64 `json:"volume"`
		OpenPrice float64 `json:"openPrice"`
		Profit    float64 `json:"profit"`
		Swap      float64 `json:"swap"`
		OpenTime  string  `json:"openTime"`
	}
	if err := a.getJSON(ctx, "/users/"+a.venue+"/positions", &raw); err != nil {
		return nil, fmt.Errorf("metaapi opened orders: %w", err)
	}
	out := make([]MetaApiOrder, 0, len(raw))
	for _, p := range raw {
		side := "BUY"
		if strings.Contains(p.Type, "SELL") {
			side = "SELL"
		}
		var openedAt time.Time
		if t, err := time.Parse(time.RFC3339, p.OpenTime); err == nil {
			openedAt = t
		}
		out = append(out, MetaApiOrder{
			Ticket:    p.ID,
			Symbol:    p.Symbol,
			Side:      side,
			Volume:    p.Volume,
			OpenPrice: p.OpenPrice,
			Profit:    p.Profit,
			Swap:      p.Swap,
			OpenedAt:  openedAt,
		})
	}
	return out, nil
}

// QuoteSnapshot 查询行情快照（GET /users/{accountId}/symbols/{symbol}/currentPrice）。
func (a *MetaApiAdapter) QuoteSnapshot(ctx context.Context, symbol string) (*MetaApiQuote, error) {
	if !a.Enabled() {
		return nil, ErrMetaApiNotConfigured
	}
	var raw struct {
		Symbol string  `json:"symbol"`
		Bid    float64 `json:"bid"`
		Ask    float64 `json:"ask"`
		Time   string  `json:"time"`
	}
	if err := a.getJSON(ctx, "/users/"+a.venue+"/symbols/"+symbol+"/currentPrice", &raw); err != nil {
		return nil, fmt.Errorf("metaapi quote snapshot: %w", err)
	}
	var qt time.Time
	if t, err := time.Parse(time.RFC3339, raw.Time); err == nil {
		qt = t
	}
	return &MetaApiQuote{
		Symbol: raw.Symbol,
		Bid:    raw.Bid,
		Ask:    raw.Ask,
		Time:   qt,
	}, nil
}

// getJSON 发起带 Bearer token 的 GET 请求并解码 JSON。
func (a *MetaApiAdapter) getJSON(ctx context.Context, path string, out any) error {
	url := a.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// 确保 strconv 在编译期被引用（未来 accountId 数字化时使用，避免 import 抖动）。
var _ = strconv.Itoa
