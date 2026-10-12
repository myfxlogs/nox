// gate_contract_meta.go —— D-509 S3：Gate 合约元数据日轮采集器。
// funding_interval 属合约元数据（/api/v4/futures/usdt/contracts/{c} detail），
// 近静态字段日轮即可；funding rate/next_apply 已由 futures.tickers WS 原生帧承载
// （S3 实测帧内同名字段）——REST 保留唯一职责=元数据，S5 退役 gate_funding 5min 轮询后
// 本器成为 interval fact 唯一写源。
package exchange

import (
	"context"
	"fmt"
	"time"

	"arb/internal/data/fact"
)

// GateContractMeta Gate 合约元数据轮询器（日频）。
type GateContractMeta struct{ cfg Config }

// NewGateContractMeta 创建 Gate 合约元数据采集器。
func NewGateContractMeta(cfg Config) *GateContractMeta { return &GateContractMeta{cfg: cfg} }

// Kind funding_interval 键（fact 域）。
func (g *GateContractMeta) Kind() string { return fact.KindFundingInterval }

// Poll 逐符号拉 contract detail → KindFundingInterval（venue 原生 funding_interval 秒）。
func (g *GateContractMeta) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range g.cfg.effectiveSymbols("gate") {
		contract := sym + "_USDT"
		url := fmt.Sprintf("%s/api/v4/futures/usdt/contracts/%s", g.cfg.GateBaseURL, contract)
		body, err := getJSON(ctx, g.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:gate] contract meta %s: %v (skip)\n", contract, err)
			continue
		}
		iv, ok := body["funding_interval"].(float64)
		if !ok || iv <= 0 {
			continue // 缺字段=缺省不造（venue 禁借用律）
		}
		out = append(out, fact.Fact{Kind: fact.KindFundingInterval, Venue: VenueGate,
			Symbol: sym + "USDT", Value: iv, Unit: fact.UnitSeconds, Ts: time.Now(), Src: "gate_v4_contract_meta"})
	}
	return out, nil
}
