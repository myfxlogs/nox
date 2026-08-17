// Package collect 定义采集层统一接口（§3.1）。
// 三种源类型：QuoteSource（订阅式报价）、FactSource（推送型事实）、Collector（轮询型事实）。
package collect

import (
	"context"

	"arb/internal/bus"
	"arb/internal/fact"
)

// QuoteSource 是订阅式报价源（MT4/MT5，§3.2）。
type QuoteSource interface {
	Name() string
	Subscribe(symbols []string) (<-chan bus.Quote, func())
}

// FactSource 是推送型 Fact 源（WS funding 流，§3.1/D-028）。
// Run 阻塞运行，内部重连；sink 写 FactSink 并触发检测器 Fact 事件。
type FactSource interface {
	Name() string
	Run(ctx context.Context, sink func(fact.Fact)) error
}

// Collector 是轮询型 Fact 源（DeFi/fx/mt_swap，§3.4-3.6）。
type Collector interface {
	Kind() string
	Poll(ctx context.Context) ([]fact.Fact, error)
}
