package dciexec

// d207_test.go —— D-207 交割观察锚：过期单翻态/方向额入账/未到期不翻/行缺跳过。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func d207Order() pgstore.FilledDCIOrder {
	return pgstore.FilledDCIOrder{
		ID: 7, RequestID: "prop-7-put", ProductRef: "PUTP", OptType: "PUT",
		InvestCcy: "USDT", Underlying: "BTC", Qty: 541,
		Response: `{"orderId":1,"settleDate":` + fmt.Sprint(time.Now().Add(-time.Hour).UnixMilli()) + `}`,
	}
}

// 过期单 venue 确认 SETTLED→翻态+方向额入账+表象行收口+warn 推送。
func TestD207_SettleConfirmed(t *testing.T) {
	st := newFake()
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	st.cfgID = 42
	ex := &Executor{
		Store: st,
		Now:   time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{
				ID: "PUTP", OptionType: "PUT", Status: "SETTLED",
				ExercisedCoin: "BTC", ExercisedAmt: `0.0045`, InterestAmt: `1.2`,
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
	}
	n, err := ex.SettleExpiredOrders(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if len(st.settled) != 1 || st.settled[0].id != 7 || st.settled[0].ccy != "BTC" {
		t.Fatalf("翻态行对拍败: %+v", st.settled)
	}
	if st.settled[0].qty != 0.0045 || st.settled[0].income != 1.2 {
		t.Fatalf("方向额对拍败: %+v", st.settled[0])
	}
	var kinds []string
	for _, ev := range st.incomeEvs {
		kinds = append(kinds, ev.IncomeType)
		if ev.ConfigID != 42 {
			t.Fatalf("config id 透传败: %d", ev.ConfigID)
		}
	}
	if len(kinds) != 2 || kinds[0] != "dci_interest" || kinds[1] != "dci_exercise" {
		t.Fatalf("income kinds 分记对拍败: %v", kinds)
	}
	// BTC 行权量按 spot 折 USD；USDT 息 1:1。
	for _, ev := range st.incomeEvs {
		if ev.IncomeType == "dci_interest" && ev.AmountUSD != 1.2 {
			t.Fatalf("利息 USD 对拍: %v", ev.AmountUSD)
		}
		if ev.IncomeType == "dci_exercise" && ev.AmountUSD != 0.0045*80000 {
			t.Fatalf("接货 USD 对拍: %v", ev.AmountUSD)
		}
	}
	if len(st.closedMarkers) != 1 || !strings.Contains(st.closedMarkers[0], "prop-7-put") {
		t.Fatalf("表象行 marker 对拍败: %v", st.closedMarkers)
	}
	var warn string
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "已交割") {
			warn = a
		}
	}
	if !strings.Contains(warn, "已交割") || !strings.Contains(warn, "BTC") {
		t.Fatalf("warn 推送对拍败: %q", warn)
	}
}

// 未到期（HOLDING）/行未查得/未过期三分支零翻态。
func TestD207_SettleSkips(t *testing.T) {
	mk := func(pos []DCIPosition, settleAgo time.Duration) (*fakeStore, *Executor) {
		st := newFake()
		o := d207Order()
		o.Response = `{"settleDate":` + fmt.Sprint(time.Now().Add(settleAgo).UnixMilli()) + `}`
		st.filledOrders = []pgstore.FilledDCIOrder{o}
		return st, &Executor{
			Store: st, Now: time.Now,
			PositionsFn: func(context.Context) ([]DCIPosition, error) { return pos, nil },
		}
	}
	// HOLDING 行在→不翻。
	st, ex := mk([]DCIPosition{{ID: "PUTP", OptionType: "PUT", Status: "HOLDING"}}, -time.Hour)
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("HOLDING 不应翻: n=%d", n)
	}
	// 行未查得→跳过不翻。
	st, ex = mk(nil, -time.Hour)
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("行缺不应翻: n=%d", n)
	}
	// settleDate 未到→不查 venue 不翻。
	st, ex = mk(nil, time.Hour)
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("未过期不应翻: n=%d", n)
	}
	// 无过期单→PositionsFn 不调用（短路在 venue 读前）。
	called := false
	ex.PositionsFn = func(context.Context) ([]DCIPosition, error) { called = true; return nil, nil }
	if _, err := ex.SettleExpiredOrders(context.Background()); err != nil || called {
		t.Fatalf("无到期单不应查 venue: called=%v err=%v", called, err)
	}
}

// settleDateOf：ms epoch/数字串/缺键/坏 JSON 四态。
func TestD207_SettleDateParse(t *testing.T) {
	if ts, ok := settleDateOf(`{"settleDate":1728633600000}`); !ok || ts.UnixMilli() != 1728633600000 {
		t.Fatalf("ms epoch 解析败")
	}
	if _, ok := settleDateOf(`{"settleDate":"1728633600000"}`); !ok {
		t.Fatalf("数字串解析败")
	}
	if _, ok := settleDateOf(`{"orderId":1}`); ok {
		t.Fatalf("缺键不应 ok")
	}
	if _, ok := settleDateOf(`not-json`); ok {
		t.Fatalf("坏 JSON 不应 ok")
	}
}
