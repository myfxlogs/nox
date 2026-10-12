package exec

// d573_jargon_test.go —— D-573 验收锚 A3：执行类告警白话头+订单号/venue 保留。

import (
	"strings"
	"testing"
)

// A3a：gate 订单事件快照 canceled 零成交 → 中文白话+订单号保留。
func TestD573_A3_GateCancelCN(t *testing.T) {
	e := &CEXExecutor{}
	_, done, err := e.gateSnapEval(Order{PosID: 42}, "12345678", GateOrderSnap{State: "canceled"})
	if !done || err == nil {
		t.Fatalf("canceled 零成交应终态错误: %v %v", done, err)
	}
	if !strings.Contains(err.Error(), "gate 订单 12345678 未成交") {
		t.Fatalf("白话头+订单号: %q", err.Error())
	}
	if strings.Contains(err.Error(), "canceled 零成交") {
		t.Fatalf("黑话残留: %q", err.Error())
	}
}

// A3b：okx 终态求值 rejected → 白话头+原始明细尾（state/accFillSz/avgPx 保留）。
func TestD573_A3_OKXRejectedCN(t *testing.T) {
	e := &CEXExecutor{}
	_, done, err := e.evalOKXStatus(Order{PosID: 7}, "ord-1",
		okxOrderStatusResult{State: "rejected", AccFillSz: "0", AvgPx: ""}, 0)
	if !done || err == nil {
		t.Fatalf("rejected 应终态错误: %v %v", done, err)
	}
	if !strings.Contains(err.Error(), "okx 订单 ord-1 未成交") ||
		!strings.Contains(err.Error(), "原始明细：state=rejected accFillSz=0") {
		t.Fatalf("白话头+原始明细尾: %q", err.Error())
	}
}
