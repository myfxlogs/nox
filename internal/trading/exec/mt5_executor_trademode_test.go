// mt5_executor_trademode_test.go —— D-004 P1#2：MT5Executor.TradeModeNow
// 即时探针（SymbolParams 单符号直查；RPC 错/空返回/venue 错误 → error）。
package exec

import (
	"context"
	"errors"
	"testing"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
)

func TestMT5TradeModeNow_ReturnsMode(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{spMode: mt5pb.TradeMode_TradeMode_ShortOnly})
	mode, err := e.TradeModeNow(context.Background(), "XAUUSDm")
	if err != nil {
		t.Fatalf("TradeModeNow: %v", err)
	}
	if mode != 2 {
		t.Fatalf("mode=%d, want 2(ShortOnly)", mode)
	}
}

func TestMT5TradeModeNow_RPCError(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{spErr: errors.New("conn refused")})
	if _, err := e.TradeModeNow(context.Background(), "XAUUSDm"); err == nil {
		t.Fatal("RPC 错误应透传为 error")
	}
}

func TestMT5TradeModeNow_EnvelopeError(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{spEnvErrMsg: "symbol not found"})
	if _, err := e.TradeModeNow(context.Background(), "XAUUSDm"); err == nil {
		t.Fatal("GetError 非空应返 error")
	}
}

func TestMT5TradeModeNow_EmptyResult(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{spNilResult: true})
	if _, err := e.TradeModeNow(context.Background(), "XAUUSDm"); err == nil {
		t.Fatal("空 result 应返 error")
	}
}
