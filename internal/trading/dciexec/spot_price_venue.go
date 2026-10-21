// spot_price_venue.go —— 现货参考价 venue 分派面（D-651 A 段收敛 venuepricer
// 注册表；薄壳保留 errSpotNoBase 哨兵与 PriceFn/SigmaFn 测试注口语义）。
// 病灶锚：okx 产品拿 binance 价评估=strike 界失真（alert#3373）；新所接入
// =venuepricer/<venue>.go init() 注册零改本件。
package dciexec

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"nox/internal/data/collect/exchange"
	"nox/internal/platform/venuepricer"
)

// errSpotNoBase 现货价读径 base URL/凭据行缺哨兵——venue 无 APIBase/EarnCreds 行
// 时返回，调用方按 credential 缺口落请求箱（禁裸 scheme/网络错冒 runtime）。
var errSpotNoBase = errors.New("现货价读径 venue base/凭据行缺")

// pricerCred venue→venuepricer.Cred 投影：binance=APIBase；其余=EarnCreds
// 行 base_url（exchange_configs 装配注入）。σ 引擎注入 exchange.SigmaDaily
// （venuepricer 为 platform 叶包禁逆向 import data 层）。
func (e *Executor) pricerCred(venue string) venuepricer.Cred {
	c := venuepricer.Cred{Client: e.client(), SigmaFn: exchange.SigmaDaily}
	if venue == "binance" {
		c.BaseURL = e.APIBase
	} else if ec, ok := e.EarnCreds[venue]; ok {
		c.BaseURL = ec.BaseURL
	}
	return c
}

// venuePricer 注册表取件：无注册行/base 缺 → errSpotNoBase（凭证缺口语义——
// For 哨兵归一到本域哨兵，调用方缺口落箱径不变）。
func (e *Executor) venuePricer(venue string) (venuepricer.Adapter, error) {
	a, err := venuepricer.For(venue, e.pricerCred(venue))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errSpotNoBase, err)
	}
	return a, nil
}

// spotPriceVenue 现货参考价按 venue 分派（薄壳委托 venuepricer.For——
// symbol 统一收 binance 形（BTCUSDT），剥 USDT 尾得基币交适配器原生符号）。
func (e *Executor) spotPriceVenue(ctx context.Context, venue, symbol string) (float64, error) {
	if e.PriceFn != nil {
		return e.PriceFn(ctx, symbol)
	}
	base := strings.TrimSuffix(strings.ToUpper(symbol), "USDT")
	a, err := e.venuePricer(venue)
	if err != nil {
		return 0, err
	}
	return a.SpotPrice(ctx, base)
}
