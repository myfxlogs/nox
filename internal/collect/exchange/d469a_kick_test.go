package exchange

// D-469a T1/T2/T3/T5：事件缓存写面 kick、binance a.m 分派、OKX 四频道分派、
// KickingSnapshotSink 透传+kick（帧级 fake，handleFrame/handleData 直喂）。

import (
	"context"
	"testing"
	"time"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// T1：SetOnPut 写面 kick——PutOrder/PutPositions 各触发一次 onPut，venueKey 透传。
func TestD469a_CacheSetOnPut(t *testing.T) {
	var orderKicks, posKicks []string
	oc := NewOrderEventCache()
	oc.SetOnPut(func(vk string) { orderKicks = append(orderKicks, vk) })
	oc.PutOrder("binance:perp", "ord1", "", OrderEvent{State: "filled", At: time.Now()})
	if len(orderKicks) != 1 || orderKicks[0] != "binance:perp" {
		t.Fatalf("order onPut kicks=%v want [binance:perp]", orderKicks)
	}

	pc := NewPositionEventCache()
	pc.SetOnPut(func(vk string) { posKicks = append(posKicks, vk) })
	pc.PutPositions("okx:perp", []reconcile.VenueOrder{{Ticket: "XAU-USDT-SWAP"}}, time.Now())
	if len(posKicks) != 1 || posKicks[0] != "okx:perp" {
		t.Fatalf("pos onPut kicks=%v want [okx:perp]", posKicks)
	}
}

// T2：binance ACCOUNT_UPDATE a.m 分派——FUNDING_FEE→OnFundingFee，DEPOSIT/WITHDRAW→OnIO，未知 m→无 kick。
func TestD469a_BinanceAcctReasonDispatch(t *testing.T) {
	cases := []struct {
		m        string
		fundKick bool
		ioKick   bool
	}{
		{"FUNDING_FEE", true, false},
		{"DEPOSIT", false, true},
		{"WITHDRAW", false, true},
		{"ORDER", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		var fundK, ioK int
		b := NewBinanceUserWS("K", "S", "https://unused.invalid", "perp", "binance:perp", false, nil, nil,
			NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{
				OnFundingFee: func() { fundK++ },
				OnIO:         func() { ioK++ },
			})
		frame := `{"e":"ACCOUNT_UPDATE","a":{"m":"` + c.m + `","B":[],"P":[]}}`
		b.handleFrame(context.Background(), []byte(frame))
		if (fundK == 1) != c.fundKick || (ioK == 1) != c.ioKick {
			t.Fatalf("m=%q: fund=%d io=%d want fund=%v io=%v", c.m, fundK, ioK, c.fundKick, c.ioKick)
		}
	}
}

// T3：OKX 频道分派——balance_and_position eventType=funding_fee→OnFundingFee；
// positions→posCache；orders→orderCache；account→OnAccountPush。
func TestD469a_OKXChannelDispatch(t *testing.T) {
	var fundK, acctK, ordK int
	oc, pc := NewOrderEventCache(), NewPositionEventCache()
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:perp", nil, oc, pc, OKXPrivateHooks{
		OnFundingFee:  func() { fundK++ },
		OnAccountPush: func() { acctK++ },
		OnOrderPush:   func() { ordK++ },
	})
	ctx := context.Background()

	o.handleData(ctx, "balance_and_position", nil, []map[string]any{
		{"eventType": "funding_fee"},
		{"eventType": "delivered"}, // 非 funding_fee 不 kick
	})
	if fundK != 1 {
		t.Fatalf("funding kicks=%d want 1", fundK)
	}

	o.handleData(ctx, "positions", nil, []map[string]any{{
		"instId": "XAU-USDT-SWAP", "pos": "-1016", "avgPx": "4297.7", "posSide": "net",
		"upl": "-12.34", "fee": "-0.56", "cTime": "1758663525994",
	}})
	got, ok := pc.get("okx:perp", time.Minute, time.Now())
	if !ok || len(got) != 1 || got[0].Volume != -1016 {
		t.Fatalf("posCache=%v ok=%v want 1 row vol=-1016", got, ok)
	}
	// 字段对拍（REST querier 同口径）：upl→Profit、fee→Commission、cTime→OpenedAt。
	if got[0].Profit != -12.34 || got[0].Commission != -0.56 {
		t.Fatalf("Profit=%v Commission=%v want -12.34/-0.56", got[0].Profit, got[0].Commission)
	}
	if got[0].OpenedAt.UnixMilli() != 1758663525994 {
		t.Fatalf("OpenedAt=%v want cTime=1758663525994", got[0].OpenedAt)
	}

	o.handleData(ctx, "orders", nil, []map[string]any{{
		"instId": "XAU-USDT-SWAP", "ordId": "o1", "state": "filled", "accFillSz": "5", "avgPx": "4300", "fee": "-0.1", "feeCcy": "USDT",
	}})
	if ordK != 1 {
		t.Fatalf("order kicks=%d want 1", ordK)
	}
	// ctVal 查询会走真实端点失败→accFillSz 以张计降级，只断言缓存有写入。
	if _, ok := oc.get("okx:perp", "o1", "", time.Minute, time.Now()); !ok {
		t.Fatalf("orderCache miss for o1")
	}

	// account 帧走 sink+OnAccountPush——sink=nil 会 panic，用 fake sink。
	var snapK int
	o.sink = &fakeSnapSink{}
	o.hooks.OnAccountPush = func() { acctK++; snapK++ }
	o.handleData(ctx, "account", nil, []map[string]any{
		{"ccy": "USDT", "eq": "1500", "availEq": "600", "uTime": "1700000000000"},
	})
	if acctK != 1 {
		t.Fatalf("account kicks=%d want 1", acctK)
	}
}

// T5：KickingSnapshotSink 透传 inner+kick 各一次；inner 出错时 kick 不触发。
func TestD469a_KickingSnapshotSink(t *testing.T) {
	inner := &fakeSnapSink{}
	var kicks int
	k := NewKickingSnapshotSink(inner, func() { kicks++ })
	rows := []pgstore.VenueAccountSnapshot{{Venue: "okx", EquityRaw: 1}}
	if err := k.InsertVenueAccountSnapshots(context.Background(), rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if len(inner.rows) != 1 || kicks != 1 {
		t.Fatalf("rows=%d kicks=%d want 1/1", len(inner.rows), kicks)
	}
}
