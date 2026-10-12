package exchange

// d569b_ws_test.go —— D-569 包B S7 验收锚 A13/A14。
// A13：MARGIN_CALL 全帧 payload 同达审计与 OnMarginCall；A14：未知事件一行留痕含类型+venue。
import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// A13：MARGIN_CALL 帧 → onAudit("margin_call", 全帧 data) + hooks.OnMarginCall(全帧 data)。
func TestD569B_A13_MarginCallFullFrame(t *testing.T) {
	sink := newFakeEventSink()
	var hookPayload []byte
	b := &BinanceUserWS{
		venueType: "perp",
		events:    sink,
		hooks: BinanceUserHooks{
			OnMarginCall: func(p []byte) { hookPayload = append([]byte(nil), p...) },
		},
	}
	frame := `{"e":"MARGIN_CALL","E":123,"o":{"s":"BTCUSDT","p":"1","mt":"CROSSED","mp":"90000"}}`
	if b.handleFrame(context.Background(), []byte(frame)) {
		t.Fatalf("MARGIN_CALL 不得触发断连")
	}
	rows := sink.snapshot()
	if len(rows) != 1 || rows[0] != "binance:perp:margin_call:" {
		t.Fatalf("audit 行=%v want binance:perp:margin_call:", rows)
	}
	sink.lk.Lock()
	got := sink.payloads
	sink.lk.Unlock()
	if len(got) != 1 || got[0] != frame {
		t.Fatalf("audit payload 须=全帧: %q", got)
	}
	if string(hookPayload) != frame {
		t.Fatalf("OnMarginCall payload 须=全帧: %q", hookPayload)
	}
	// nil hook 安全（不 panic）。
	b.hooks = BinanceUserHooks{}
	b.handleFrame(context.Background(), []byte(frame))
}

// A14：未知事件类型 → 一行日志含事件类型+venueType。
func TestD569B_A14_UnknownEventLogged(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	b := &BinanceUserWS{venueType: "spot"}
	b.handleFrame(context.Background(), []byte(`{"e":"SOME_FUTURE_EVENT","x":1}`))
	out := buf.String()
	if !strings.Contains(out, "SOME_FUTURE_EVENT") || !strings.Contains(out, "spot") {
		t.Fatalf("未知事件日志须含类型+venue: %q", out)
	}
}
