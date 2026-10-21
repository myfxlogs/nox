// Package venuepricer —— D-651 A 段：venue 定价面注册层。
// 病灶锚：dciexec 按 binance 单所建，多所化后 spotPrice/sigmaDaily/usdOf/
// disposal_sweep 散裸写钉 binance——okx 产品拿 binance 价评估病灶。
// 原则：新所接入=加 venuepricer/<venue>.go+init() 注册，零改共享径；
// 只收「读」面（价/σ），写面（下单/划转）留各 executor 不归本包。
package venuepricer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Adapter venue 定价面契约（读面；调用方 fail-closed——err 如实上抛不兜别所）。
type Adapter interface {
	// SpotPrice 现货参考价（交割锚产品用 Index 口径 venue 原生价）；base=基币（BTC）。
	SpotPrice(ctx context.Context, base string) (float64, error)
	// SigmaDaily 日收益 σ；Cred.SigmaFn 注入 venue 日 K 实算件（无注=err 不虚算）。
	SigmaDaily(ctx context.Context, base string, days int) (float64, error)
	// SpotSymbol venue 原生符号（BTCUSDT/BTC-USDT/BTC_USDT——符号格式归适配器内）。
	SpotSymbol(base string) string
}

// Cred 适配器构造凭据/底座（工厂注口）。
type Cred struct {
	BaseURL string
	Client  *http.Client
	// SigmaFn 日 K σ 实算件（internal/data/collect/exchange.SigmaDaily 形——
	// platform 叶包禁逆向 import data 层，σ 引擎由调用面注入）。
	SigmaFn func(ctx context.Context, client *http.Client, baseURL, venue, symbol string, days int) (float64, error)
}

// ErrNoAdapter 无注册哨兵（凭证缺口语义——调用方按缺口落请求箱，非裸错）。
var ErrNoAdapter = errors.New("venuepricer: venue 无注册适配器")

// Factory venue→适配器构造（凭据/base 由调用方注入）。
type Factory func(c Cred) Adapter

var registry = map[string]Factory{} // venue → 工厂（init() 注册——新所零改共享径）

// Register 装配点（各 venue 文件 init() 自注册）。同键重复注册=panic 防静默盖。
func Register(venue string, f Factory) {
	if _, dup := registry[venue]; dup {
		panic("venuepricer: duplicate register " + venue)
	}
	registry[venue] = f
}

// For venue 适配器取件：无注册→ErrNoAdapter（fail-closed 非静默回落别所）；
// base_url 缺=同哨兵（凭据缺口语义——设计 A.3 errSpotNoBase 复用点）。
func For(venue string, c Cred) (Adapter, error) {
	f, ok := registry[venue]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoAdapter, venue)
	}
	if c.BaseURL == "" {
		return nil, fmt.Errorf("%w: %s（base_url 缺）", ErrNoAdapter, venue)
	}
	return f(c), nil
}
