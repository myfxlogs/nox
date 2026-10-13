package errclass

import (
	"errors"
	"testing"
)

// TestClassifyMT5 MT5 错误码分类（四类 Action）。
func TestClassifyMT5(t *testing.T) {
	tests := []struct {
		code string
		want Action
	}{
		{"REQUOTE", RetryFresh},
		{"PRICE_CHANGED", RetryFresh},
		{"OFF_QUOTES", Retry},
		{"NO_PRICES", Retry},
		{"TOO_MANY_TRADE_REQUESTS", Retry},
		{"MARKET_CLOSED", Halt},
		{"TRADE_DISABLED", Halt},
		{"NO_MONEY", Abort},
		{"INVALID_VOLUME", Abort},
		{"INVALID_PRICE", Abort},
		{"INVALID_STOPS", Abort},
		{"INVALID_TRADE_VOLUME", Abort},
		{"INVALID_TRADE_REQUEST", Abort},
		{"UNKNOWN_ERROR", Abort}, // 兜底
		{"", Abort},              // 空码兜底
	}
	for _, tt := range tests {
		got := ClassifyMT5(tt.code)
		if got != tt.want {
			t.Errorf("ClassifyMT5(%q) want %v, got %v", tt.code, tt.want, got)
		}
	}
}

// TestClassifyByVenue 按 venueKey 路由 MT5/MT4/CEX。
func TestClassifyByVenue(t *testing.T) {
	// MT5 venue
	if got := Classify("vA:mt5", "MARKET_CLOSED"); got != Halt {
		t.Errorf("mt5 MARKET_CLOSED want Halt, got %v", got)
	}
	// mt4 已退役（码表撤除）——历史 mt4 键统一 Abort 兜底
	if got := Classify("vB:mt4", "TRADE_DISABLED"); got != Abort {
		t.Errorf("mt4 TRADE_DISABLED want Abort（已退役）, got %v", got)
	}
	// CEX venue → Abort 兜底（无标准码表）
	if got := Classify("binance:spot", "ANY_CODE"); got != Abort {
		t.Errorf("cex ANY_CODE want Abort, got %v", got)
	}
}

// TestActionString Action.String() 落库 reason 前缀用。
func TestActionString(t *testing.T) {
	tests := []struct {
		a    Action
		want string
	}{
		{Retry, "Retry"},
		{RetryFresh, "RetryFresh"},
		{Abort, "Abort"},
		{Halt, "Halt"},
		{Action(99), "Abort"}, // 未知 → Abort 兜底
	}
	for _, tt := range tests {
		if got := tt.a.String(); got != tt.want {
			t.Errorf("Action(%d).String() want %q, got %q", tt.a, tt.want, got)
		}
	}
}

// TestTruncateNote note 截断（arbcn 同款 120 字）。
func TestTruncateNote(t *testing.T) {
	// 短 note 不截断
	short := "order failed"
	if got := TruncateNote(short, 120); got != short {
		t.Errorf("short note want unchanged, got %q", got)
	}
	// 长英文 note 截断
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := TruncateNote(long, 120)
	if len(got) != 123 { // 120 + "..."
		t.Errorf("long note want 123 chars (120+...), got %d", len(got))
	}
	// 中文 note rune 计数（不按字节截断）
	cn := ""
	for i := 0; i < 200; i++ {
		cn += "中"
	}
	gotCn := TruncateNote(cn, 120)
	runeLen := len([]rune(gotCn))
	if runeLen != 123 { // 120 中文字 + "..."
		t.Errorf("cn note want 123 runes, got %d", runeLen)
	}
	// maxLen=0 → 默认 120
	if got := TruncateNote(long, 0); len(got) != 123 {
		t.Errorf("maxLen=0 default 120, want 123 chars, got %d", len(got))
	}
}

// TestHaltActionStopsStrategy Halt 类应停策略（接线验证：Classify 返回 Halt 即可被上层 DisableStrategy）。
func TestHaltActionStopsStrategy(t *testing.T) {
	// MARKET_CLOSED / TRADE_DISABLED → Halt（上层据此 DisableStrategy + critical）
	haltCodes := []string{"MARKET_CLOSED", "TRADE_DISABLED"}
	for _, code := range haltCodes {
		if ClassifyMT5(code) != Halt {
			t.Errorf("MT5 %s should be Halt (stops strategy)", code)
		}
	}
}

// TestParseCode 从 executor 错误信息提取 [code=XXX] 标记。
func TestParseCode(t *testing.T) {
	tests := []struct {
		errMsg string
		want   string
	}{
		{"mt5 ordersend: [code=MARKET_CLOSED] market is closed", "MARKET_CLOSED"},
		{"mt4 ordersend: [code=TRADE_DISABLED] trade disabled", "TRADE_DISABLED"},
		{"mt5 ordersend: nil result", ""},             // 无标记
		{"mt5 ordersend: [code=] empty code", ""},     // 空码
		{"mt5 ordersend: [code=REQUOTE", ""},          // 无闭合 ]
		{"", ""},                                      // 空串
		{"[code=OFF_QUOTES] no prices", "OFF_QUOTES"}, // 开头标记
	}
	for _, tt := range tests {
		got := ParseCode(tt.errMsg)
		if got != tt.want {
			t.Errorf("ParseCode(%q) want %q, got %q", tt.errMsg, tt.want, got)
		}
	}
}

// TestClassifyErr 按 venueKey + error 分类（便捷入口）。
func TestClassifyErr(t *testing.T) {
	// MT5 MARKET_CLOSED → Halt
	err := errors.New("mt5 ordersend: [code=MARKET_CLOSED] market closed")
	if got := ClassifyErr("vA:mt5", err); got != Halt {
		t.Errorf("ClassifyErr mt5 MARKET_CLOSED want Halt, got %v", got)
	}
	// mt4 已退役 → Abort 兜底（不再走 MT4 码表）
	err = errors.New("mt4 ordersend: [code=REQUOTE] requote")
	if got := ClassifyErr("vB:mt4", err); got != Abort {
		t.Errorf("ClassifyErr mt4 REQUOTE want Abort（已退役）, got %v", got)
	}
	// 无 code 标记 → Abort 兜底
	err = errors.New("mt5 ordersend: nil result")
	if got := ClassifyErr("vA:mt5", err); got != Abort {
		t.Errorf("ClassifyErr no code want Abort, got %v", got)
	}
	// nil error → Abort（不应 panic）
	if got := ClassifyErr("vA:mt5", nil); got != Abort {
		t.Errorf("ClassifyErr nil want Abort, got %v", got)
	}
}
