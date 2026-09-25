package adapter

// D-469b T5：MT4 订单/余额事件源装配——S0 实证服务端流面实装（grpcurl 见施工报告），
// 组件级断言 publish 路径（流消费层与 MT5 同构已由先例覆盖）。

import (
	"testing"

	"arb/internal/bus"
)

// T5a：OrderEventSink 注入 → publishOrder 透传 {Venue,Ticket}；nil sink 不 panic。
func TestD469b_MT4OrderEventPublish(t *testing.T) {
	var got []bus.OrderEvent
	a := &MT4Adapter{venue: "mt4demo", OrderEventSink: func(e bus.OrderEvent) { got = append(got, e) }}
	src := &MT4OrderEventSource{a: a}
	src.publishOrder(bus.OrderEvent{Venue: "mt4demo", Ticket: 42})
	src.publishOrder(bus.OrderEvent{Venue: "mt4demo", Ticket: 0}) // ticket=0 通用通知
	if len(got) != 2 || got[0].Ticket != 42 || got[0].Venue != "mt4demo" {
		t.Fatalf("got=%+v", got)
	}
	// nil sink 安全。
	src2 := &MT4OrderEventSource{a: &MT4Adapter{venue: "x"}}
	src2.publishOrder(bus.OrderEvent{})
	// runMT4OrderEventSource 双 nil → no-op（不阻塞）。
	(&MT4Adapter{}).RunOrderEventSource(t.Context())
}

// T5b：MarginKick 注入 → publishKick 触发（acctMon.Kick 装配语义）。
func TestD469b_MT4ProfitKick(t *testing.T) {
	var n int
	a := &MT4Adapter{venue: "mt4demo", MarginKick: func() { n++ }}
	src := &MT4OrderEventSource{a: a}
	src.publishKick(a.MarginKick)
	src.publishKick(a.MarginKick)
	if n != 2 {
		t.Fatalf("kicks=%d want 2", n)
	}
}
