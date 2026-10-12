// d142_upl_equity_test.go D-142 锚：binance 合约权益=wb+ΣUP（WS 快照/活账本/REST 同口径），
// available=eq−Σiw；本帧无 P 行时 ΣUP/Σiw 取 posCache 现存持仓（B/P 独立到达勿以 0 覆盖）。
package exchange

import (
	"context"
	"testing"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

func newD142WS() (*BinanceUserWS, *uplSnapSink, *bus.BalanceLedger) {
	sink := &uplSnapSink{}
	led := bus.NewBalanceLedger()
	b := NewBinanceUserWS("K", "S", "https://unused.invalid", "perp", "binance:perp", false,
		sink, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.balLedger = led
	return b, sink, led
}

// TestD142_WSFrameWithP B+P 帧：快照/活账本 equity=wb+ΣUP（多腿求和），avail=eq−Σiw。
func TestD142_WSFrameWithP(t *testing.T) {
	b, sink, led := newD142WS()
	// wb=1000；腿1 up=+10 iw=30，腿2 up=−4 iw=20 → eq=1006, avail=1006−50=956。
	frame := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[{"a":"USDT","wb":"1000"}],"P":[` +
		`{"s":"TRXUSDT","pa":"16","ep":"0.334","iw":"30","ps":"BOTH","up":"10"},` +
		`{"s":"ETHUSDT","pa":"-2","ep":"3000","iw":"20","ps":"BOTH","up":"-4"}]}}`
	b.handleFrame(context.Background(), []byte(frame))

	if len(sink.rows) != 1 {
		t.Fatalf("want 1 snapshot row, got %d", len(sink.rows))
	}
	row := sink.rows[0]
	if !feeEq(row.BalanceRaw, 1000) || !feeEq(row.EquityRaw, 1006) {
		t.Fatalf("snapshot want balance=1000 equity=1006（wb+ΣUP）, got %+v", row)
	}
	e, ok := led.Get("binance:perp", 0, b.now())
	if !ok {
		t.Fatal("ledger miss——balPut 未写")
	}
	if !feeEq(e.Equity, 1006) || !feeEq(e.Available, 956) {
		t.Fatalf("ledger want eq=1006 avail=956（eq−Σiw）, got %+v", e)
	}
	if !feeEq(row.EquityRaw, e.Equity) {
		t.Fatalf("快照/账本 equity 不同口径: %v vs %v", row.EquityRaw, e.Equity)
	}
}

// TestD142_WSFrameNoP 无 P 帧：ΣUP/Σiw 用 posCache 现存持仓，勿清零（权益不塌）。
func TestD142_WSFrameNoP(t *testing.T) {
	b, sink, led := newD142WS()
	open := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[{"a":"USDT","wb":"1000"}],"P":[` +
		`{"s":"TRXUSDT","pa":"16","ep":"0.334","iw":"30","ps":"BOTH","up":"10"},` +
		`{"s":"ETHUSDT","pa":"-2","ep":"3000","iw":"20","ps":"BOTH","up":"-4"}]}}`
	b.handleFrame(context.Background(), []byte(open))

	// B-only 帧（P 缺省）：wb 波动到 900 → eq=900+6=906，avail=906−50=856。
	bonly := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[{"a":"USDT","wb":"900"}]}}`
	b.handleFrame(context.Background(), []byte(bonly))

	if len(sink.rows) != 2 || !feeEq(sink.rows[1].EquityRaw, 906) {
		t.Fatalf("B-only 帧 equity want 906（wb+缓存ΣUP）, rows=%+v", sink.rows)
	}
	e, ok := led.Get("binance:perp", 0, b.now())
	if !ok || !feeEq(e.Equity, 906) || !feeEq(e.Available, 856) {
		t.Fatalf("ledger want eq=906 avail=856, ok=%v got %+v", ok, e)
	}
}

// TestD142_WSCloseFrameClearsUPL pa=0 全量替换：posCache 清空后 B-only 帧权益回 wb。
func TestD142_WSCloseFrameClearsUPL(t *testing.T) {
	b, sink, led := newD142WS()
	open := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[{"a":"USDT","wb":"1000"}],"P":[` +
		`{"s":"TRXUSDT","pa":"16","ep":"0.334","iw":"30","ps":"BOTH","up":"10"}]}}`
	b.handleFrame(context.Background(), []byte(open))
	close := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[],"P":[{"s":"TRXUSDT","pa":"0","ep":"0.334","iw":"0","ps":"BOTH","up":"0"}]}}`
	b.handleFrame(context.Background(), []byte(close))
	bonly := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[{"a":"USDT","wb":"1000"}]}}`
	b.handleFrame(context.Background(), []byte(bonly))

	last := sink.rows[len(sink.rows)-1]
	if !feeEq(last.EquityRaw, 1000) {
		t.Fatalf("平仓后 B-only 帧 equity want 1000（缓存已清）, got %+v", last)
	}
	e, ok := led.Get("binance:perp", 0, b.now())
	if !ok || !feeEq(e.Equity, 1000) || !feeEq(e.Available, 1000) {
		t.Fatalf("ledger want eq=avail=1000, ok=%v got %+v", ok, e)
	}
}

// uplSnapSink 捕获 snapshot 写入（balance 通道对拍面）。
type uplSnapSink struct {
	rows []pgstore.VenueAccountSnapshot
}

func (s *uplSnapSink) InsertVenueAccountSnapshots(_ context.Context, r []pgstore.VenueAccountSnapshot) error {
	s.rows = append(s.rows, r...)
	return nil
}
